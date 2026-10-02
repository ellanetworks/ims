package pcscf

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

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
)

type Config struct {
	Layer     *transaction.Layer
	Proxy     *proxy.Proxy
	Port      uint16
	ICSCFPort uint16
	IPsec     IPsec
	Fallback  transaction.Handler
	Logger    *slog.Logger
}

type PCSCF struct {
	cfg Config
	log *slog.Logger
	sas *associations
}

func New(cfg Config) *PCSCF {
	p := &PCSCF{cfg: cfg, log: cfg.Logger}

	if p.log == nil {
		p.log = slog.Default()
	}

	if cfg.IPsec.Kernel != nil {
		p.sas = newAssociations(cfg.IPsec, p.log)

		if cfg.Layer != nil {
			p.sas.closeFlow = cfg.Layer.CloseFlow
		}
	}

	return p
}

func (p *PCSCF) Restore(ctx context.Context) error {
	if p.sas == nil {
		return nil
	}

	return p.sas.restore(ctx)
}

func (p *PCSCF) Close() {
	if p.sas != nil {
		p.sas.close()
	}
}

func (p *PCSCF) Filter(m sip.Message) error {
	if p.sas == nil {
		return nil
	}

	f := m.Env().Flow

	req, isRequest := m.(*sip.Request)

	if p.sas.protected(f.Local.Port()) {
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

		return nil
	}

	if isRequest && f.Local.Port() == p.cfg.Port && req.Method != "REGISTER" && p.sas.hasEstablished(f.Remote.Addr().Unmap()) {
		return errUnprotected
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
	if req.Method != "REGISTER" {
		p.cfg.Fallback.HandleRequest(tx, req)
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
	in        *view
	offer     *ipsec.Offer
	client    []sip.SecurityMechanism
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

	offers, err := ipsec.ParseOffers(clients)
	ipsecOffered := err == nil

	for _, m := range clients {
		if _, err := ipsec.ParseOffer(m); err != nil && strings.EqualFold(m.Name, ipsec.Mechanism) {
			p.log.Info("skipped Security-Client mechanism", slog.String("impi", r.impi), slog.Any("error", err))
		}
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
		case !sameIPsecMechanisms(clients, r.in.client):
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
	removeOptionTag(out, "Require", secAgree)
	removeOptionTag(out, "Proxy-Require", secAgree)

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

	if p.sas == nil {
		return proxy.Relay
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
		p.sas.registered(r.in.s, registrationOutcome(req, res))
	case r.in != nil && r.in.state == temporary:
		p.sas.failed(r.in.s)
	}

	return proxy.Relay
}

func (p *PCSCF) replace(tx *transaction.ServerTransaction, req *sip.Request, code int) proxy.Verdict {
	if err := p.cfg.Proxy.Relay(tx, sip.NewResponse(req, code, "")); err != nil {
		p.log.Debug("P-CSCF response failed", slog.Int("code", code), slog.Any("error", err))
	}

	return proxy.Hold
}

func (p *PCSCF) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	p.cfg.Fallback.HandleCancel(tx, cancel)
}

func (p *PCSCF) HandleAck(ack *sip.Request) {
	p.cfg.Fallback.HandleAck(ack)
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

func viaMatches(req *sip.Request, source netip.Addr) bool {
	vias, err := req.Header.Vias()
	if err != nil || len(vias) != 1 {
		return false
	}

	a, ok := vias[0].Addr()

	return ok && a.Unmap() == source
}

func sameIPsecMechanisms(a, b []sip.SecurityMechanism) bool {
	x, y := ipsecMechanisms(a), ipsecMechanisms(b)
	if len(x) != len(y) {
		return false
	}

	used := make([]bool, len(y))

next:
	for _, m := range x {
		for i, o := range y {
			if !used[i] && m.Equal(o) {
				used[i] = true
				continue next
			}
		}

		return false
	}

	return true
}

func ipsecMechanisms(ms []sip.SecurityMechanism) []sip.SecurityMechanism {
	var out []sip.SecurityMechanism

	for _, m := range ms {
		if strings.EqualFold(m.Name, ipsec.Mechanism) {
			m.Params = m.Params.Clone()
			m.Params.Del("q")
			out = append(out, m)
		}
	}

	return out
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

func removeOptionTag(req *sip.Request, name, tag string) {
	var kept []string

	for _, t := range req.Header.Elements(name) {
		if !strings.EqualFold(t, tag) {
			kept = append(kept, t)
		}
	}

	req.Header.Del(name)

	if len(kept) > 0 {
		req.Header.Add(name, strings.Join(kept, ", "))
	}
}

func addOptionTag(res *sip.Response, name, tag string) {
	for _, t := range res.Header.Elements(name) {
		if strings.EqualFold(t, tag) {
			return
		}
	}

	res.Header.Add(name, tag)
}

func fromUE(req *sip.Request) {
	trust.StripRequest(req)

	for _, name := range []string{"P-Charging-Vector", "P-Charging-Function-Addresses", "P-Visited-Network-ID", "Path"} {
		req.Header.Del(name)
	}
}

func toUE(res *sip.Response) {
	trust.StripResponse(res)
	res.Header.Del("P-Charging-Vector")

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
