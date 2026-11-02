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
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/lifetime"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

const contentTypeIMS = "application/3gpp-ims+xml"

// TS 24.229 clause 5.4.1.7
// TS 23.218 clause 5.2.3
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

type thirdParty struct {
	r     *Registrar
	as    cx.ApplicationServer
	to    string
	impi  string
	ev    registration
	held  <-chan struct{}
	cause trace.SpanContext

	callID  string
	once    sync.Once
	done    chan struct{}
	err     error
	timer   transaction.Timer
	expired chan struct{}
}

var errNoResponse = errors.New("no response in time")

// TS 24.229 clause 5.4.1.7
// TS 23.218 clause 6.3
func (r *Registrar) thirdParties(ctx context.Context, ev registration) []*thirdParty {
	sub, err := cx.ParseUserData(ev.reg.UserData)
	if err != nil {
		r.log.WarnContext(ctx, "invalid user data of a registration: no third-party REGISTER", slog.String("impi", ev.reg.IMPI),
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
					r: r, as: c.ApplicationServer, to: to, impi: ev.reg.IMPI, ev: ev, cause: trace.SpanContextFromContext(ctx),
					done: make(chan struct{}), expired: make(chan struct{}),
				})
			}
		}
	}

	return out
}

// TS 24.229 clause 5.4.1.7
// TS 24.229 clause 5.4.1.7A
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

	for _, name := range []string{"P-Access-Network-Info", "P-Visited-Network-ID"} {
		for _, v := range ev.req.Header.Values(name) {
			req.Header.Add(name, v)
		}
	}

	if ev.regType != cx.RegistrationTypeDeregistration {
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

// TS 24.229 clause 7.6
func serviceInfo(info string) []byte {
	var b strings.Builder

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<ims-3gpp version="1"><service-info>`)
	_ = xml.EscapeText(&b, []byte(info))
	b.WriteString("</service-info></ims-3gpp>\n")

	return []byte(b.String())
}

// TS 24.229 clause 5.4.1.7
// TS 24.229 clause 5.4.1.5
func (r *Registrar) notifyASs(ctx context.Context, ev registration, held <-chan struct{}) {
	for _, tp := range r.thirdParties(ctx, ev) {
		tp.held = held
		r.queue(ctx, tp)
	}

	if ev.ended {
		r.forgetASs(setKeys(ev.reg))
	}
}

// RFC 3261 section 10.2
func (r *Registrar) queue(ctx context.Context, tp *thirdParty) {
	id := identityKeyOf(tp.to)

	r.mu.Lock()

	p := r.peerLocked(id, tp.as.ServerName)
	p.pending = append(p.pending, peerJob{tp: tp})

	start := !p.sending
	p.sending = true

	r.mu.Unlock()

	if start {
		r.drain(ctx, id, tp.as.ServerName, p, tp)
	}
}

func (r *Registrar) drain(ctx context.Context, id, serverName string, p *asPeer, first *thirdParty) {
	if !r.start() {
		r.mu.Lock()
		p.pending, p.sending = nil, false
		r.dropLocked(id, serverName, p)
		r.mu.Unlock()

		return
	}

	ctx = lifetime.With(r.ctx, ctx)

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

			switch job.tp {
			case nil:
			case first:
				if !job.tp.run(ctx) {
					return
				}
			default:
				if !job.tp.runQueued() {
					return
				}
			}
		}
	}()
}

func (tp *thirdParty) runQueued() bool {
	ctx, span := tp.r.detached.Start(tp.r.ctx, "third-party registration", tp.cause)

	ok := tp.run(ctx)

	err := context.Cause(ctx)
	if ok {
		err = tp.err
	}

	tracing.End(span, err)

	return ok
}

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
	case <-tp.expired:
		tp.timer.Stop()
		tp.failed(ctx, errNoResponse)

		return true
	case <-ctx.Done():
		return false
	}
}

func (tp *thirdParty) send(ctx context.Context) {
	r := tp.r

	to, err := sip.ParseURI(tp.to)
	if err != nil {
		tp.failed(ctx, err)
		return
	}

	srv, err := r.asServer(ctx, to, tp.as.ServerName)
	if err != nil {
		r.log.WarnContext(ctx, "unusable AS for a third-party REGISTER", slog.String("as", tp.as.ServerName),
			slog.String("impu", tp.to), slog.Any("error", err))
		tp.failed(ctx, err)

		return
	}

	req, err := r.thirdPartyRegister(tp.ev, to, tp.as, srv)
	if err != nil {
		tp.failed(ctx, err)
		return
	}

	tp.callID = req.Header.CallID()

	r.log.DebugContext(ctx, "third-party REGISTER", slog.String("as", tp.as.ServerName), slog.String("to", tp.to),
		slog.String("type", tp.ev.regType.String()))

	tp.timer = r.clock.AfterFunc(r.cfg.ASTimeout, func() { close(tp.expired) })

	if _, err := r.cfg.Layer.Request(ctx, req, tp); err != nil {
		tp.timer.Stop()
		tp.failed(ctx, err)
	}
}

// TS 24.229 clause 5.4.1.7
func (tp *thirdParty) HandleResponse(ctx context.Context, res *sip.Response) {
	tp.timer.Stop()

	if ioi := termIOIOf(res); ioi != "" {
		tp.r.storeTermIOI(tp.to, tp.as.ServerName, tp.callID, ioi)
	}

	switch {
	case res.IsProvisional():
	case res.StatusCode == 408 || res.StatusCode >= 500:
		tp.failed(ctx, fmt.Errorf("%d %s", res.StatusCode, res.Reason))
	default:
		tp.once.Do(func() { close(tp.done) })
	}
}

func (tp *thirdParty) HandleError(ctx context.Context, err error) {
	tp.timer.Stop()
	tp.failed(ctx, err)
}

// TS 24.229 clause 5.4.1.7
func (tp *thirdParty) failed(ctx context.Context, err error) {
	tp.once.Do(func() {
		tp.err = err

		defer close(tp.done)

		r := tp.r

		if tp.ev.regType == cx.RegistrationTypeDeregistration || !terminates(tp.as) {
			r.log.InfoContext(ctx, "third-party REGISTER failed", slog.String("as", tp.as.ServerName),
				slog.String("impi", tp.impi), slog.Any("error", err))

			return
		}

		r.log.InfoContext(ctx, "third-party REGISTER failed: deregistering", slog.String("as", tp.as.ServerName),
			slog.String("impi", tp.impi), slog.Any("error", err))

		r.background(ctx, tp.impi, func(ctx context.Context) {
			r.send(ctx, r.deregisterBindings(ctx, tp.impi, tp.ev.reg.ID, tp.ev.bindings))
		})
	})
}

// TS 24.229 clause 5.4.1.5
func (r *Registrar) deregisterBindings(ctx context.Context, impi string, regID int64, ids []int64) []*outgoing {
	st, err := r.load(ctx, impi)
	if err != nil {
		r.log.WarnContext(ctx, "failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
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

	rm, deleted, err := r.removeBindings(ctx, st, reg, removed, reginfo.Rejected)
	if err != nil {
		r.log.WarnContext(ctx, "failed to store the registration", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	if deleted {
		r.deregisterAtHSS(ctx, reg.HSS, impi, reg.IMPU, assignAdministrative)
	}

	r.log.InfoContext(ctx, "contacts deregistered by the network", slog.String("impi", impi), slog.String("impu", reg.IMPU),
		slog.Int("contacts", len(removed)))

	r.releaseCalls(ctx, st, []removal{rm})

	r.deregistered(ctx, rm)

	return r.notifyChange(ctx, impi, change{removed: []removal{rm}, reason: reasonRejected})
}

// TS 23.218 clause 5.2.3
// TS 24.229 clause 5.4.1.5
func (r *Registrar) deregistered(ctx context.Context, rm removal) {
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

	r.notifyASs(ctx, registration{
		reg: rm.reg, impu: rm.reg.IMPU, regType: cx.RegistrationTypeDeregistration, req: req, bindings: ids, ended: rm.ended,
	}, nil)
}

func setKeys(reg db.Registration) []string {
	keys := make([]string, 0, len(reg.Identities))
	for _, id := range reg.Identities {
		keys = append(keys, id.Key)
	}

	return keys
}
