package pcscf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

const secAgree = "sec-agree"

var (
	errNoSecurityAssociation = errors.New("no security association for this flow")
	errUnprotected           = &sip.StatusError{
		StatusCode: 403, Err: errors.New("unprotected request from a UE with security associations"),
	}
	errTemporary = errors.New("request other than REGISTER on a temporary set of security associations")

	errForeignCancel = errors.New("CANCEL from another address than its Via sent-by")

	// TS 24.229 §5.2.6.3.2A: discarded, without a 100 (Trying).
	errUnregistered = errors.New("initial request from a UE without a registration")
)

type Config struct {
	Layer      *transaction.Layer
	Proxy      *proxy.Proxy
	Port       uint16
	ICSCFPort  uint16
	HomeDomain string
	SCSCF      SCSCF
	IPsec      IPsec
	Rx         Rx

	Registrations RegistrationStore

	// Trust holds the core's addresses: requests toward a UE are accepted
	// only from them, on the unprotected port.
	Trust *trust.Domain

	Fallback transaction.Handler
	Clock    Clock
	Logger   *slog.Logger
}

type PCSCF struct {
	cfg   Config
	log   *slog.Logger
	clock Clock
	sas   *associations
	regs  *registrations
	subs  *subscriptions
	rx    *rxClient
}

func New(cfg Config) *PCSCF {
	p := &PCSCF{cfg: cfg, log: cfg.Logger, clock: cfg.Clock}

	if p.log == nil {
		p.log = slog.Default()
	}

	if p.clock == nil {
		p.clock = systemClock{}
	}

	grace := cfg.IPsec.Grace
	if grace <= 0 {
		grace = DefaultGrace
	}

	p.regs = newRegistrations(cfg.Registrations, p.clock, grace, p.log)
	p.subs = newSubscriptions(p)
	p.rx = newRxClient(cfg.Rx, p.log)
	p.regs.expired = p.expired

	if cfg.IPsec.Kernel != nil {
		p.sas = newAssociations(cfg.IPsec, p.log)

		if cfg.Layer != nil {
			p.sas.closeFlow = cfg.Layer.CloseFlow
		}
	}

	return p
}

func (p *PCSCF) Restore(ctx context.Context) error {
	if p.sas != nil {
		if err := p.sas.restore(ctx); err != nil {
			return err
		}
	}

	expired, err := p.regs.restore(ctx)
	if err != nil {
		return fmt.Errorf("restore registrations: %w", err)
	}

	p.restoreRx(expired)

	if err := p.subs.restore(ctx); err != nil {
		return fmt.Errorf("restore reg event subscriptions: %w", err)
	}

	return nil
}

func (p *PCSCF) Close() {
	p.subs.close()
	p.regs.close()

	if p.rx != nil {
		p.rx.close()
	}

	if p.sas != nil {
		p.sas.close()
	}
}

func (p *PCSCF) expired(r db.PCSCFRegistration) {
	if p.sas != nil {
		p.sas.deregistered(r.IMPI, r.UEAddress.Addr())
	}

	p.endRx(r, rx.TerminationAuthExpired, 0)
}

func (p *PCSCF) anyRegistration(impi string) (db.PCSCFRegistration, bool) {
	return p.regs.forIMPI(impi)
}

func (p *PCSCF) Filter(m sip.Message) error {
	f := m.Env().Flow

	req, isRequest := m.(*sip.Request)

	// A CANCEL matches its INVITE by Via branch and sent-by alone (RFC 3261
	// §17.2.3). One from a UE whose sent-by is not its source could cancel,
	// or block the CANCEL of, another UE's INVITE, so it is dropped before
	// matching.
	if isRequest && req.Method == "CANCEL" && p.ownPort(f.Local.Port()) && !p.trusted(f.Remote.Addr()) &&
		!topViaFrom(req, f.Remote.Addr()) {
		return errForeignCancel
	}

	switch {
	case p.sas == nil:
	case p.sas.protected(f.Local.Port()):
		v, ok := p.sas.lookup(f)
		if !ok {
			if f.Transport == sip.TCP && p.cfg.Layer != nil {
				go p.cfg.Layer.CloseFlow(f)
			}

			return errNoSecurityAssociation
		}

		if v.state == temporary && isRequest && req.Method != "REGISTER" {
			return errTemporary
		}

		p.sas.received(v.s)
	case isRequest && f.Local.Port() == p.cfg.Port && req.Method != "REGISTER" && p.sas.hasEstablished(f.Remote.Addr().Unmap()):
		return errUnprotected
	}

	if isRequest && p.unregisteredOrigin(req) {
		return errUnregistered
	}

	return nil
}

func (p *PCSCF) ResponseFlow(req *sip.Request, res *sip.Response) (sip.Flow, bool, error) {
	if p.sas == nil || !p.sas.protected(req.Flow.Local.Port()) {
		return sip.Flow{}, false, nil
	}

	f, err := p.sas.responseFlow(req.Flow, res)
	if err != nil {
		return sip.Flow{}, false, err
	}

	return f, true, nil
}

func (p *PCSCF) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	p.signallingRestored(req)

	if req.Method != "REGISTER" {
		switch top, ok := p.terminatingRoute(req); {
		case toTag(req) != "":
			p.inDialog(tx, req)
		case ok:
			p.terminating(tx, req, top)
		case p.fromCore(req):
			p.cfg.Fallback.HandleRequest(tx, req)
		default:
			p.originating(tx, req)
		}

		return
	}

	if res := p.cfg.Proxy.Check(req); res != nil {
		p.respond(tx, res)
		return
	}

	out, _, err := p.cfg.Proxy.Preprocess(req)
	if err != nil {
		p.respond(tx, sip.NewResponse(req, 400, "Bad Route"))
		return
	}

	r := &registration{
		ue:    req.Flow.Remote.Addr().Unmap(),
		local: req.Flow.Local.Addr().Unmap(),
		impi:  privateIdentity(req),
	}

	if p.sas != nil {
		if v, ok := p.sas.lookup(req.Flow); ok {
			r.in = &v
		}
	}

	if res := p.secAgree(req, out, r); res != nil {
		if r.in != nil {
			p.sas.failed(r.in.s)
		}

		p.respond(tx, res)

		return
	}

	fromUE(out)

	r.token = p.regs.token(r.impi, r.ue)
	out.Header.Prepend("Path", "<"+pathURI(r.token, r.local, p.cfg.Port, contactParam(req, "reg-id")).String()+">")
	addRequestOptionTag(out, "Require", "path")

	if p.cfg.HomeDomain != "" {
		out.Header.Set("P-Visited-Network-ID", p.cfg.HomeDomain)
	}

	to := proxy.Target{Flow: sip.Flow{
		Transport: req.Flow.Transport,
		Local:     netip.AddrPortFrom(req.Flow.Local.Addr(), p.cfg.Port),
		Remote:    netip.AddrPortFrom(req.Flow.Local.Addr(), p.cfg.ICSCFPort),
	}}

	err = p.cfg.Proxy.Forward(tx, out, to, proxy.Options{OnReply: func(rep proxy.Reply) proxy.Verdict {
		return p.reply(tx, req, r, rep)
	}})
	if err == nil || errors.Is(err, proxy.ErrAnswered) {
		return
	}

	code := 500

	if serr, ok := errors.AsType[*sip.StatusError](err); ok {
		code = serr.StatusCode
	}

	p.respond(tx, sip.NewResponse(req, code, ""))
}

type registration struct {
	ue, local netip.Addr
	impi      string
	token     string
	in        *view
	offer     *ipsec.Offer
	client    []sip.SecurityMechanism

	// removed is the record the 200 deregistered, whose Rx session ends once
	// the 200 is relayed.
	removed *db.PCSCFRegistration
}

func (p *PCSCF) secAgree(req, out *sip.Request, r *registration) *sip.Response {
	clients, err := req.Header.SecurityMechanisms("Security-Client")
	if err != nil {
		return sip.NewResponse(req, 400, "Bad Security-Client")
	}

	verify, err := req.Header.SecurityMechanisms("Security-Verify")
	if err != nil {
		return sip.NewResponse(req, 400, "Bad Security-Verify")
	}

	offers, skipped, err := ipsec.ParseOffers(clients)
	ipsecOffered := err == nil

	for _, err := range skipped {
		p.log.Info("skipped Security-Client mechanism", slog.String("impi", r.impi), slog.Any("error", err))
	}

	if p.sas != nil && r.in == nil && !ipsecOffered {
		p.log.Info("REGISTER without IPsec in Security-Client", slog.String("impi", r.impi),
			slog.String("security-client", req.Header.Get("Security-Client")))

		return p.secAgreeRequired(req)
	}

	if ipsecOffered && p.sas != nil {
		o, err := p.cfg.IPsec.Policy.Select(offers)
		if err != nil {
			p.log.Info("no acceptable IPsec algorithm", slog.String("impi", r.impi),
				slog.String("security-client", req.Header.Get("Security-Client")))

			return sip.NewResponse(req, 403, "No Acceptable IPsec Algorithm")
		}

		r.offer, r.client = &o, clients
	}

	if r.in != nil && !viaMatches(req, r.ue) {
		p.log.Info("protected REGISTER whose Via is not its source address alone", slog.String("impi", r.impi),
			slog.String("source", r.ue.String()), slog.Any("via", req.Header.Values("Via")))

		return sip.NewResponse(req, 403, "")
	}

	integrity := "no"

	switch {
	case r.in == nil:
	case r.in.state == temporary:
		switch {
		case len(verify) == 0 || len(clients) == 0:
			return sip.NewResponse(req, 400, "Missing Security-Verify or Security-Client")
		case !verifies(verify, r.in.server):
			p.log.Info("Security-Verify differs from the Security-Server", slog.String("impi", r.impi),
				slog.String("security-verify", req.Header.Get("Security-Verify")), slog.String("security-server", r.in.server.String()))

			return sip.NewResponse(req, 403, "Security Agreement Mismatch")
		case !sameOffers(clients, r.in.client):
			p.log.Info("Security-Client differs from the challenged REGISTER's", slog.String("impi", r.impi),
				slog.String("security-client", req.Header.Get("Security-Client")),
				slog.String("challenged", mechanismsString(r.in.client)))

			return sip.NewResponse(req, 403, "Security Agreement Mismatch")
		case r.impi != r.in.impi:
			return sip.NewResponse(req, 403, "")
		}

		if hasChallengeResponse(req) {
			integrity = "yes"
		}

		r.offer, r.client = nil, nil
	default:
		if r.impi != r.in.impi {
			return sip.NewResponse(req, 403, "")
		}

		if r.offer == nil {
			p.log.Info("protected REGISTER without Security-Client", slog.String("impi", r.impi))
		}

		if r.in.state == established {
			integrity = "yes"
		}
	}

	out.Header.Del("Security-Client")
	out.Header.Del("Security-Verify")
	removeSecAgree(out, "Require")
	removeSecAgree(out, "Proxy-Require")

	if err := setIntegrityProtected(out, integrity); err != nil {
		return sip.NewResponse(req, 400, "Bad Authorization")
	}

	return nil
}

func (p *PCSCF) reply(tx *transaction.ServerTransaction, req *sip.Request, r *registration, rep proxy.Reply) proxy.Verdict {
	res := rep.Response

	if res == nil || res.StatusCode < 200 {
		if res == nil && r.in != nil && p.sas != nil {
			p.sas.failed(r.in.s)
		}

		if res != nil {
			toUE(res)
		}

		return proxy.Relay
	}

	keys, keysErr := ipsec.KeysFromChallenge(challengeOf(res))

	toUE(res)

	ended, open := false, false
	if res.IsSuccess() {
		ended = p.registered(req, res, r)
		open = !ended && p.rx != nil && !emergency(req, res) && p.regs.withoutRx(r.impi, r.ue)
	}

	// The Rx session is opened once the 200 has been relayed (TS 29.213
	// Annex B.1), and closed after the 200 to a deregistration (TS 23.228
	// §5.3.1).
	relay := func() proxy.Verdict {
		if !open && r.removed == nil {
			return proxy.Relay
		}

		if err := p.cfg.Proxy.Relay(tx, res); err != nil {
			p.log.Debug("P-CSCF response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}

		if r.removed != nil {
			p.endRx(*r.removed, rx.TerminationLogout, 0)
		}

		if open {
			p.openRx(regKey{r.impi, r.ue}, 0)
		}

		return proxy.Hold
	}

	if p.sas == nil {
		return relay()
	}

	switch {
	case res.StatusCode == 401 && r.offer == nil && r.in != nil && r.in.state != temporary:
		p.log.Info("re-authentication of a UE that sent no Security-Client", slog.String("impi", r.impi))
		return p.replace(tx, req, 403)
	case res.StatusCode == 401 && r.offer != nil:
		if keysErr != nil {
			p.log.Error("401 without usable ck and ik", slog.String("impi", r.impi), slog.Any("error", keysErr))
			return p.replace(tx, req, 500)
		}

		var origin *saSet
		if r.in != nil {
			origin = r.in.s
		}

		s, err := p.sas.challenged(challenge{
			impi: r.impi, local: r.local, ue: r.ue, offer: *r.offer, client: r.client, origin: origin,
		}, keys)
		if err != nil {
			p.log.Error("setting up security associations failed", slog.String("impi", r.impi), slog.Any("error", err))

			if errors.Is(err, errSAConflict) || errors.Is(err, errTooManySAs) {
				return p.replace(tx, req, 403)
			}

			return p.replace(tx, req, 500)
		}

		res.Header.Add("Security-Server", s.String())
		addOptionTag(res, "Supported", secAgree)
	case res.IsSuccess() && r.in != nil:
		o := registrationOutcome(req, res)
		o.dereg = o.dereg && ended
		p.sas.registered(r.in.s, o)
	case r.in != nil && r.in.state == temporary:
		p.sas.failed(r.in.s)
	}

	return relay()
}

func (p *PCSCF) registered(req *sip.Request, res *sip.Response, r *registration) bool {
	o := registrationOutcome(req, res)
	impu := registeredIdentity(req)

	existing, known := p.regs.get(r.impi, r.ue)

	if o.dereg {
		if known {
			delete(existing.Sets, impu)

			if len(existing.Sets) > 0 {
				existing.AssociatedURIs = union(existing.AssociatedURIs, existing.Sets)
				p.regs.save(existing)

				p.log.Info("public identity deregistered", slog.String("impi", r.impi), slog.String("impu", impu))

				return false
			}
		}

		if old, ok := p.regs.remove(r.impi, r.ue); ok {
			p.log.Info("UE deregistered", slog.String("impi", r.impi), slog.String("ue", r.ue.String()))

			if old.RxSessionID != "" {
				r.removed = &old
			}
		}

		p.unsubscribeIfIdle(r.impi)

		return true
	}

	if o.lifetime <= 0 {
		return false
	}

	var associated []string

	identities, _ := res.Header.Addresses("P-Associated-URI")
	for _, a := range identities {
		associated = append(associated, a.URI.String())
	}

	reg := db.PCSCFRegistration{
		IMPI:         r.impi,
		FlowToken:    r.token,
		Transport:    string(req.Flow.Transport),
		Protected:    r.in != nil,
		UEAddress:    netip.AddrPortFrom(r.ue, req.Flow.Remote.Port()),
		PCSCFAddress: r.local,
		Sets:         map[string][]string{},
		ServiceRoute: res.Header.Elements("Service-Route"),
		ExpiresAt:    p.clock.Now().Add(o.lifetime),
	}

	if known {
		reg.Contacts = existing.Contacts
		reg.Sets = existing.Sets
		reg.AssociatedURIs = existing.AssociatedURIs
		reg.ExpiresAt = later(reg.ExpiresAt, existing.ExpiresAt)
	}

	requested, _ := req.Header.Contacts()
	for _, c := range requested {
		uri := c.URI.String()
		reg.Contacts = slices.DeleteFunc(reg.Contacts, func(k string) bool { return k == uri })

		if v, ok := c.Params.Get("expires"); !ok || v != "0" {
			reg.Contacts = append(reg.Contacts, uri)
		}
	}

	reg.Sets[impu] = associated
	reg.AssociatedURIs = union(append(slices.Clone(reg.AssociatedURIs), associated...), reg.Sets)

	p.regs.save(reg)

	if len(associated) == 0 || p.cfg.Layer == nil {
		return false
	}

	if !known {
		p.subs.stop(r.impi)
	}

	if !p.subs.has(r.impi) {
		p.subs.start(r.impi)
	}

	return false
}

// emergency reports whether the 200 completes an emergency registration: the
// Contact the REGISTER registered carries the "sos" parameter in the 200 (TS
// 24.229 §5.2.10.1). It gets no Rx session (TS 29.214 Annex A.4 NOTE 2; TS
// 29.213 Annex B.0).
func emergency(req *sip.Request, res *sip.Response) bool {
	requested, _ := req.Header.Contacts()
	granted, _ := res.Header.Contacts()

	return slices.ContainsFunc(granted, func(g sip.Address) bool {
		return g.URI.Params.Has("sos") && slices.ContainsFunc(requested, func(c sip.Address) bool {
			return c.URI.String() == g.URI.String()
		})
	})
}

func registeredIdentity(req *sip.Request) string {
	to, err := req.Header.To()
	if err != nil {
		return ""
	}

	return to.URI.String()
}

func union(list []string, sets map[string][]string) []string {
	var out []string

	for _, a := range list {
		if slices.Contains(out, a) {
			continue
		}

		for _, set := range sets {
			if slices.Contains(set, a) {
				out = append(out, a)
				break
			}
		}
	}

	return out
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}

	return b
}

func (p *PCSCF) unsubscribeIfIdle(impi string) {
	if _, ok := p.regs.forIMPI(impi); !ok {
		p.subs.stop(impi)
	}
}

func (p *PCSCF) replace(tx *transaction.ServerTransaction, req *sip.Request, code int) proxy.Verdict {
	if err := p.cfg.Proxy.Relay(tx, sip.NewResponse(req, code, "")); err != nil {
		p.log.Debug("P-CSCF response failed", slog.Int("code", code), slog.Any("error", err))
	}

	return proxy.Hold
}

// HandleCancel cancels an INVITE the P-CSCF proxies. The layer delivers the
// CANCEL after HandleRequest returned, by which time the INVITE was forwarded
// or handed to the fallback.
// A CANCEL from a UE that another host could have forged is dropped earlier,
// by Filter.
func (p *PCSCF) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	if p.cfg.Proxy.Proxied(tx) {
		p.cfg.Proxy.Cancel(tx, cancel)
		return
	}

	p.cfg.Fallback.HandleCancel(tx, cancel)
}

func (p *PCSCF) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	p.cfg.Fallback.HandleTransactionError(tx, err)
}

func (p *PCSCF) respond(tx *transaction.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		p.log.Debug("P-CSCF response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}

func setIntegrityProtected(req *sip.Request, value string) error {
	values := req.Header.Values("Authorization")

	auths := make([]string, 0, len(values))

	for _, v := range values {
		a, err := sip.ParseAuth(v)
		if err != nil {
			return err
		}

		a.Params.Del("integrity-protected")
		a.Params.Set("integrity-protected", sip.Quote(value))
		auths = append(auths, a.String())
	}

	req.Header.Del("Authorization")

	for _, a := range auths {
		req.Header.Add("Authorization", a)
	}

	return nil
}

func authorization(req *sip.Request) (sip.Auth, bool) {
	a, err := sip.ParseAuth(req.Header.Get("Authorization"))
	return a, err == nil && req.Header.Has("Authorization")
}

func privateIdentity(req *sip.Request) string {
	if a, ok := authorization(req); ok {
		if v, ok := a.Params.Get("username"); ok && sip.Unquote(v) != "" {
			return sip.Unquote(v)
		}
	}

	to, err := req.Header.To()
	if err != nil {
		return ""
	}

	u := to.URI
	if u.User == "" {
		return u.Host
	}

	return u.User + "@" + u.Host
}

func hasChallengeResponse(req *sip.Request) bool {
	a, ok := authorization(req)
	if !ok {
		return false
	}

	v, ok := a.Params.Get("response")

	return ok && sip.Unquote(v) != ""
}

func verifies(verify []sip.SecurityMechanism, server sip.SecurityMechanism) bool {
	want, err := ipsec.ParseOffer(server)
	if err != nil {
		return false
	}

	n := 0

	for _, m := range verify {
		if !strings.EqualFold(m.Name, ipsec.Mechanism) {
			continue
		}

		got, err := ipsec.ParseOffer(m)
		if err != nil || got != want {
			return false
		}

		n++
	}

	return n == 1
}

func topViaFrom(req *sip.Request, source netip.Addr) bool {
	via, err := req.Header.TopVia()
	if err != nil {
		return false
	}

	a, ok := via.Addr()

	return ok && a.Unmap() == source.Unmap()
}

func viaMatches(req *sip.Request, source netip.Addr) bool {
	vias, err := req.Header.Vias()
	if err != nil || len(vias) != 1 {
		return false
	}

	a, ok := vias[0].Addr()

	return ok && a.Unmap() == source
}

func sameOffers(a, b []sip.SecurityMechanism) bool {
	x, xSkipped, _ := ipsec.ParseOffers(a)
	y, ySkipped, _ := ipsec.ParseOffers(b)

	if len(x) != len(y) || len(xSkipped) != len(ySkipped) {
		return false
	}

	n := make(map[ipsec.Offer]int, len(x))
	for _, o := range x {
		n[o]++
	}

	for _, o := range y {
		if n[o] == 0 {
			return false
		}

		n[o]--
	}

	return true
}

func mechanismsString(ms []sip.SecurityMechanism) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = m.String()
	}

	return strings.Join(parts, ", ")
}

func challengeOf(res *sip.Response) sip.Auth {
	for _, v := range res.Header.Values("WWW-Authenticate") {
		if a, err := sip.ParseAuth(v); err == nil && a.Params.Has("ck") {
			return a
		}
	}

	return sip.Auth{}
}

type outcome struct {
	lifetime time.Duration
	dereg    bool
}

func registrationOutcome(req *sip.Request, res *sip.Response) outcome {
	if strings.TrimSpace(req.Header.Get("Contact")) == "*" {
		return outcome{dereg: true}
	}

	requested, err := req.Header.Contacts()
	if err != nil || len(requested) == 0 {
		return outcome{}
	}

	granted, _ := res.Header.Contacts()

	var (
		longest uint64
		matched bool
	)

	for _, g := range granted {
		if !slices.ContainsFunc(requested, func(c sip.Address) bool { return c.URI.String() == g.URI.String() }) {
			continue
		}

		v, _ := g.Params.Get("expires")
		if n, err := parseSeconds(v); err == nil {
			matched = true
			longest = max(longest, n)
		}
	}

	if !matched {
		for _, c := range requested {
			v, ok := c.Params.Get("expires")
			if !ok {
				v = req.Header.Get("Expires")
			}

			if n, err := parseSeconds(v); err == nil {
				matched = true
				longest = max(longest, n)
			}
		}
	}

	return outcome{lifetime: time.Duration(longest) * time.Second, dereg: matched && longest == 0}
}

func parseSeconds(s string) (uint64, error) {
	var n uint64

	if s == "" || len(s) > 10 {
		return 0, errors.New("bad seconds")
	}

	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("bad seconds")
		}

		n = n*10 + uint64(c-'0')
	}

	return n, nil
}

// secAgreeRequired answers a REGISTER the P-CSCF cannot protect (RFC 3329
// §2.3.1): 421 when it lacks the sec-agree option tag, 494 when it has one, both
// with the P-CSCF's Security-Server list and Require: sec-agree.
func (p *PCSCF) secAgreeRequired(req *sip.Request) *sip.Response {
	code := 421

	for _, name := range []string{"Require", "Proxy-Require", "Supported"} {
		if slices.ContainsFunc(req.Header.Elements(name), func(t string) bool { return strings.EqualFold(t, secAgree) }) {
			code = 494
		}
	}

	res := sip.NewResponse(req, code, "")

	for _, m := range p.cfg.IPsec.Policy.Mechanisms() {
		res.Header.Add("Security-Server", m.String())
	}

	res.Header.Add("Require", secAgree)

	return res
}

func removeSecAgree(req *sip.Request, name string) {
	var kept []string

	for _, t := range req.Header.Elements(name) {
		if !strings.EqualFold(t, secAgree) {
			kept = append(kept, t)
		}
	}

	req.Header.Del(name)

	if len(kept) > 0 {
		req.Header.Add(name, strings.Join(kept, ", "))
	}
}

func addRequestOptionTag(req *sip.Request, name, tag string) {
	for _, t := range req.Header.Elements(name) {
		if strings.EqualFold(t, tag) {
			return
		}
	}

	req.Header.Add(name, tag)
}

func addOptionTag(res *sip.Response, name, tag string) {
	for _, t := range res.Header.Elements(name) {
		if strings.EqualFold(t, tag) {
			return
		}
	}

	res.Header.Add(name, tag)
}

// fromUE prepares a request from the UE for the core (TS 24.229 §5.2.1, RFC
// 3325 §6, RFC 3329 §2.3.1). The trust-domain header fields go, but the UE's
// own P-Access-Network-Info stays: step 3 removes only values with
// "network-provided". So does its P-Early-Media "supported" (Decision 7).
func fromUE(req *sip.Request) {
	pani := ueAccessNetworkInfo(req.Header)
	early := earlyMediaSupported(req.Header)

	trust.StripRequest(req)
	dropFromAll(req.Header.Del)
	stripSecAgree(req)
	removeLocationSource(req)

	for _, name := range []string{"P-Preferred-Identity", "P-Visited-Network-ID", "Path"} {
		req.Header.Del(name)
	}

	for _, v := range pani {
		req.Header.Add("P-Access-Network-Info", v)
	}

	if early {
		req.Header.Add("P-Early-Media", sip.EarlyMediaSupported)
	}
}

// fromUEResponse does the same for a response from the UE (§5.2.1 steps 1-3).
// Its P-Asserted-Identity and P-Early-Media go with the rest: the P-CSCF
// asserts the identity itself (§5.2.6.4.4), and authorises no early media
// (§5.2.6.4.2, Decision 7).
func fromUEResponse(res *sip.Response) {
	pani := ueAccessNetworkInfo(res.Header)

	trust.StripResponse(res)
	dropFromAll(res.Header.Del)
	res.Header.Del("P-Preferred-Identity")

	for _, v := range pani {
		res.Header.Add("P-Access-Network-Info", v)
	}
}

func ueAccessNetworkInfo(h sip.Header) []string {
	var out []string

	for _, v := range h.Elements("P-Access-Network-Info") {
		if _, ps, err := sip.ParseTokenParams(v); err == nil && !ps.Has("network-provided") {
			out = append(out, v)
		}
	}

	return out
}

func toUE(res *sip.Response) {
	trust.StripResponse(res)
	dropFromAll(res.Header.Del)

	if res.StatusCode == 401 {
		removeAKAKeys(res)
	}
}

func removeAKAKeys(res *sip.Response) {
	values := res.Header.Values("WWW-Authenticate")
	res.Header.Del("WWW-Authenticate")

	for _, v := range values {
		a, err := sip.ParseAuth(v)
		if err != nil {
			continue
		}

		a.Params.Del("ck")
		a.Params.Del("ik")
		res.Header.Add("WWW-Authenticate", a.String())
	}
}
