package scscf

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

const contentTypeIMS = "application/3gpp-ims+xml"

// registration is what the third-party REGISTERs of a registration event carry: the REGISTER of the UE, or one
// standing for a network-initiated deregistration, and its response (TS 24.229 §5.4.1.7, TS 23.218 §5.2.3). ended
// tells that the registration set has no contact left.
type registration struct {
	reg      db.Registration
	impu     string
	regType  cx.RegistrationType
	req      *sip.Request
	res      *sip.Response
	expires  int
	bindings []int64
	ended    bool
}

// thirdParty is a third-party REGISTER to an AS, for the identity to, and the default handling of its iFC. It waits
// for held, if any, to close before it goes; done closes once it has its final response, has timed out or has failed.
type thirdParty struct {
	r    *Registrar
	as   cx.ApplicationServer
	to   string
	impi string
	ev   registration
	held <-chan struct{}

	callID string
	once   sync.Once
	done   chan struct{}
	timer  transaction.Timer
}

// thirdParties are the third-party REGISTERs of a registration event: one for each iFC that the REGISTER matches in
// each service profile of the registration set, to a non-barred identity of the profile (TS 24.229 §5.4.1.7, TS
// 23.218 §6.3).
func (r *Registrar) thirdParties(ev registration) []*thirdParty {
	sub, err := cx.ParseUserData(ev.reg.UserData)
	if err != nil {
		r.log.Warn("invalid user data of a registration: no third-party REGISTER", slog.String("impi", ev.reg.IMPI),
			slog.Any("error", err))

		return nil
	}

	impu, _ := sip.ParseURI(ev.impu)
	impuKey := identityKey(impu)

	var out []*thirdParty

	for _, p := range sub.ServiceProfiles {
		to := ""

		for _, id := range p.PublicIdentities {
			switch {
			case id.Barred || wildcarded(id):
			case identityKeyOf(id.Identity) == impuKey:
				to = id.Identity
			case to == "":
				to = id.Identity
			}
		}

		if to == "" {
			continue
		}

		t := trigger{req: ev.req, sescase: cx.SessionCaseOriginatingRegistered, regType: ev.regType}

		for _, c := range r.criteria(p, true) {
			if t.matches(c.Trigger) {
				out = append(out, &thirdParty{
					r: r, as: c.ApplicationServer, to: to, impi: ev.reg.IMPI, ev: ev, done: make(chan struct{}),
				})
			}
		}
	}

	return out
}

// TS 24.229 §5.4.1.7, §5.4.1.7A
func (r *Registrar) thirdPartyRegister(ev registration, to sip.URI, as cx.ApplicationServer, srv sip.Server) (*sip.Request, error) {
	asURI, err := sip.ParseURI(as.ServerName)
	if err != nil {
		return nil, err
	}

	local, ok := r.listener(srv.Addr.Addr())
	if !ok {
		return nil, fmt.Errorf("no S-CSCF listener for %s", srv.Addr.Addr())
	}

	callID, cseq := r.registerSequence(to, as.ServerName, local)

	req := sip.NewRequest("REGISTER", asURI)
	req.Header = nil
	req.Header.Add("Via", sip.NewVia(srv.Transport, local).String())
	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("From", "<"+r.cfg.Name.String()+">;tag="+sip.NewTag())
	req.Header.Add("To", "<"+to.String()+">")
	req.Header.Add("Call-ID", callID)
	req.Header.Add("CSeq", strconv.FormatUint(uint64(cseq), 10)+" REGISTER")

	contact, err := sip.ParseAddress(contactURI(local, srv.Transport))
	if err != nil {
		return nil, err
	}

	if ev.regType != cx.RegistrationTypeDeregistration && len(ev.bindings) > 0 {
		if p, err := sip.NewStringFeature(registrationToken, bindingToken(ev.bindings[0])).Param(); err == nil {
			contact.Params = append(contact.Params, p)
		}
	}

	req.Header.Add("Contact", contact.String())
	req.Header.Add("Expires", strconv.Itoa(ev.expires))

	// The AS is in the trust domain (TS 24.229 §5.7.3).
	for _, name := range []string{"P-Access-Network-Info", "P-Visited-Network-ID"} {
		for _, v := range ev.req.Header.Values(name) {
			req.Header.Add(name, v)
		}
	}

	if ev.regType != cx.RegistrationTypeDeregistration {
		// TS 24.229 §5.4.1.7 h: the icid-value of the REGISTER, and a type 3 orig-ioi in place of the one received,
		// without term-ioi.
		if icid := icidOf(ev.req.Header); icid != "" {
			req.Header.Add(pChargingVector, r.chargingVector(icid))
		}

		var parts []sip.BodyPart

		if as.ServiceInfo != nil {
			parts = append(parts, sip.NewBodyPart(contentTypeIMS, serviceInfo(*as.ServiceInfo)))
		}

		if as.IncludeRegisterRequest {
			parts = append(parts, sip.NewBodyPart("message/sip", ev.req.Bytes()))
		}

		if as.IncludeRegisterResponse && ev.res != nil {
			parts = append(parts, sip.NewBodyPart("message/sip", ev.res.Bytes()))
		}

		req.SetBodies(parts...)
	}

	req.Flow = sip.Flow{Transport: srv.Transport, Local: local, Remote: srv.Addr}

	return req, nil
}

// serviceInfo is the 3GPP IM CN subsystem XML body of the service information of an iFC (TS 24.229 §7.6).
func serviceInfo(info string) []byte {
	var b strings.Builder

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<ims-3gpp version="1"><service-info>`)
	_ = xml.EscapeText(&b, []byte(info))
	b.WriteString("</service-info></ims-3gpp>\n")

	return []byte(b.String())
}

// notifyASs sends the third-party REGISTERs of a registration event (TS 24.229 §5.4.1.7), held until held closes,
// if not nil. It is called with the private identity locked, so that the third-party REGISTERs to an AS for an
// identity follow the order of the events. Once those of a deregistration are out, what the S-CSCF keeps of the ASs
// of the deregistered identities goes (TS 24.229 §5.4.1.5).
func (r *Registrar) notifyASs(ev registration, held <-chan struct{}) {
	for _, tp := range r.thirdParties(ev) {
		tp.held = held
		r.queue(tp)
	}

	if ev.ended {
		r.forgetASs(setKeys(ev.reg))
	}
}

// queue appends a third-party REGISTER to those to its AS for its identity, which go one at a time, in order, each
// with the next CSeq once the previous has its final response or has timed out (RFC 3261 §10.2).
func (r *Registrar) queue(tp *thirdParty) {
	id := identityKeyOf(tp.to)

	r.mu.Lock()

	p := r.peerLocked(id, tp.as.ServerName)
	p.pending = append(p.pending, peerJob{tp: tp})

	start := !p.sending
	p.sending = true

	r.mu.Unlock()

	if start {
		r.drain(id, tp.as.ServerName, p)
	}
}

// drain sends the third-party REGISTERs pending to an AS for an identity, off the caller's goroutine.
func (r *Registrar) drain(id, serverName string, p *asPeer) {
	if !r.start() {
		r.mu.Lock()
		p.pending, p.sending = nil, false
		r.dropLocked(id, serverName, p)
		r.mu.Unlock()

		return
	}

	go func() {
		defer r.wg.Done()

		for {
			r.mu.Lock()

			if len(p.pending) == 0 {
				p.sending = false
				r.dropLocked(id, serverName, p)
				r.mu.Unlock()

				return
			}

			job := p.pending[0]
			p.pending = p.pending[1:]

			if job.forget {
				p.forget()
			}

			r.mu.Unlock()

			if job.tp != nil && !job.tp.run(r.ctx) {
				return
			}
		}
	}()
}

// run sends the third-party REGISTER once it is no longer held, and waits for it to be done, unless the registrar
// closes first.
func (tp *thirdParty) run(ctx context.Context) bool {
	if tp.held != nil {
		select {
		case <-tp.held:
		case <-ctx.Done():
			return false
		}
	}

	tp.send(ctx)

	select {
	case <-tp.done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (tp *thirdParty) send(ctx context.Context) {
	r := tp.r

	to, err := sip.ParseURI(tp.to)
	if err != nil {
		tp.failed(err)
		return
	}

	srv, err := r.asServer(ctx, to, tp.as.ServerName)
	if err != nil {
		r.log.Warn("unusable AS for a third-party REGISTER", slog.String("as", tp.as.ServerName), slog.String("impu", tp.to),
			slog.Any("error", err))
		tp.failed(err)

		return
	}

	req, err := r.thirdPartyRegister(tp.ev, to, tp.as, srv)
	if err != nil {
		tp.failed(err)
		return
	}

	tp.callID = req.Header.CallID()

	r.log.Debug("third-party REGISTER", slog.String("as", tp.as.ServerName), slog.String("to", tp.to),
		slog.String("type", tp.ev.regType.String()))

	tp.timer = r.clock.AfterFunc(r.cfg.ASTimeout, func() { tp.failed(errors.New("no response in time")) })

	if _, err := r.cfg.Layer.Request(req, tp); err != nil {
		tp.timer.Stop()
		tp.failed(err)
	}
}

// HandleResponse takes any response in time as the AS's, and keeps its term-ioi; a 408 or a 5xx fails the REGISTER
// (TS 24.229 §5.4.1.7).
func (tp *thirdParty) HandleResponse(res *sip.Response) {
	tp.timer.Stop()

	if ioi := termIOIOf(res); ioi != "" {
		tp.r.storeTermIOI(tp.to, tp.as.ServerName, tp.callID, ioi)
	}

	switch {
	case res.IsProvisional():
	case res.StatusCode == 408 || res.StatusCode >= 500:
		tp.failed(fmt.Errorf("%d %s", res.StatusCode, res.Reason))
	default:
		tp.once.Do(func() { close(tp.done) })
	}
}

func (tp *thirdParty) HandleError(err error) {
	tp.timer.Stop()
	tp.failed(err)
}

// failed applies the default handling of the iFC: SESSION_TERMINATED deregisters the contacts of the REGISTER
// (TS 24.229 §5.4.1.7). The timer is the caller's to stop, if it runs: failed is its function too.
func (tp *thirdParty) failed(err error) {
	tp.once.Do(func() {
		defer close(tp.done)

		r := tp.r

		if tp.ev.regType == cx.RegistrationTypeDeregistration || !terminates(tp.as) {
			r.log.Info("third-party REGISTER failed", slog.String("as", tp.as.ServerName), slog.String("impi", tp.impi),
				slog.Any("error", err))

			return
		}

		r.log.Info("third-party REGISTER failed: deregistering", slog.String("as", tp.as.ServerName),
			slog.String("impi", tp.impi), slog.Any("error", err))

		r.background(tp.impi, func(ctx context.Context) {
			r.send(r.deregisterBindings(ctx, tp.impi, tp.ev.reg.ID, tp.ev.bindings))
		})
	})
}

// deregisterBindings is the network-initiated deregistration of bindings of a registration set, which the UE is not
// expected to register again: it would fail the same way. Their contacts are then "rejected", rather than
// "deactivated", which would have the UE register again at once (TS 24.229 §5.4.1.5). The private identity is locked.
func (r *Registrar) deregisterBindings(ctx context.Context, impi string, regID int64, ids []int64) []*outgoing {
	st, err := r.load(ctx, impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	i := slices.IndexFunc(st.regs, func(reg db.Registration) bool { return reg.ID == regID })
	if i < 0 {
		return nil
	}

	reg := st.regs[i]

	removed := slices.DeleteFunc(st.live(reg.Bindings), func(b db.Binding) bool { return !slices.Contains(ids, b.ID) })
	if len(removed) == 0 {
		return nil
	}

	rm, deleted, err := r.removeBindings(ctx, st, reg, removed, regevent.Rejected)
	if err != nil {
		r.log.Warn("failed to store the registration", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	if deleted {
		r.deregisterAtHSS(ctx, reg.HSS, impi, reg.IMPU, assignAdministrative)
	}

	r.log.Info("contacts deregistered by the network", slog.String("impi", impi), slog.String("impu", reg.IMPU),
		slog.Int("contacts", len(removed)))

	r.releaseCalls(st, []removal{rm})

	r.deregistered(rm)

	return r.notifyChange(ctx, impi, change{removed: []removal{rm}, reason: reasonRejected})
}

// deregistered sends the third-party REGISTERs of a network-initiated deregistration, as if the UE had sent the
// equivalent REGISTER: its contacts, those removed, with expires 0 and the parameters they registered with, so that
// the iFCs match as they did on registration (TS 23.218 §5.2.3, TS 24.229 §5.4.1.5). The third-party deregistration
// on expiry is the one of TS 23.218 §5.2.3 too. The private identity is locked.
func (r *Registrar) deregistered(rm removal) {
	impu, err := sip.ParseURI(rm.reg.IMPU)
	if len(rm.bindings) == 0 || rm.reg.IMPU == "" || err != nil {
		if rm.ended {
			r.forgetASs(setKeys(rm.reg))
		}

		return
	}

	req := sip.NewRequest("REGISTER", sip.URI{Scheme: "sip", Host: r.cfg.HomeDomain})
	req.Header = nil
	req.Header.Add("From", "<"+impu.String()+">")
	req.Header.Add("To", "<"+impu.String()+">")

	ids := make([]int64, 0, len(rm.bindings))

	var paths []string

	for _, b := range rm.bindings {
		ids = append(ids, b.ID)
		req.Header.Add("Contact", contactValue(b.Contact, 0))

		if b.Contact.Path != "" && !slices.Contains(paths, b.Contact.Path) {
			paths = append(paths, b.Contact.Path)
		}
	}

	for _, p := range paths {
		req.Header.Add("Path", p)
	}

	req.Header.Add("Expires", "0")

	r.notifyASs(registration{
		reg: rm.reg, impu: rm.reg.IMPU, regType: cx.RegistrationTypeDeregistration, req: req, bindings: ids, ended: rm.ended,
	}, nil)
}

// setKeys are the keys of the public identities of a registration set.
func setKeys(reg db.Registration) []string {
	keys := make([]string, 0, len(reg.Identities))
	for _, id := range reg.Identities {
		keys = append(keys, id.Key)
	}

	return keys
}
