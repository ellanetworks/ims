package scscf

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	messageRejectCode   = 403
	messageRejectReason = "SMS over IP Not Supported"
)

const icsiMMTel = "urn:urn-7:3gpp-service.ims.icsi.mmtel"

const (
	rrOriginating = "mo"
	rrTerminating = "mt"
)

type SessionConfig struct {
	Proxy *proxy.Proxy

	ICSCF []netip.AddrPort

	Numbering Numbering
}

type Sessions struct {
	r     *Registrar
	cfg   SessionConfig
	proxy *proxy.Proxy
	log   *slog.Logger
}

func (r *Registrar) Sessions(cfg SessionConfig) *Sessions {
	return &Sessions{r: r, cfg: cfg, proxy: cfg.Proxy, log: r.log}
}

func (s *Sessions) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	if res := s.proxy.Check(req); res != nil {
		s.respond(tx, res)
		return
	}

	to, err := req.Header.To()
	if err != nil {
		s.respond(tx, sip.NewResponse(req, 400, "Bad To"))
		return
	}

	out, removed, err := s.proxy.Preprocess(req)
	if err != nil {
		s.respond(tx, sip.NewResponse(req, 400, "Bad Route"))
		return
	}

	if to.Tag() != "" {
		s.inDialog(tx, out, removed)
		return
	}

	contactID, orig, err := originatingContact(removed)
	if err != nil {
		s.log.Debug("originating request on a malformed Service-Route", slog.String("request", req.StartLine()), slog.Any("error", err))
		s.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	err = s.r.cfg.Layer.Go(func(ctx context.Context) {
		ctx, done, ok := s.r.begin(ctx)
		if !ok {
			s.answer(tx, retryLater(req))
			return
		}

		defer done()

		if orig {
			s.originating(ctx, tx, out, contactID)
		} else {
			s.terminating(ctx, tx, out)
		}
	})
	if err != nil {
		s.log.Debug("dropped a SIP request", slog.String("request", req.StartLine()), slog.Any("error", err))
		s.respond(tx, retryLater(req))
	}
}

func (s *Sessions) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	s.proxy.Cancel(tx, cancel)
}

func (s *Sessions) HandleAck(ack *sip.Request) {
	out, removed, err := s.proxy.Preprocess(ack)
	if err != nil || len(removed) == 0 {
		s.log.Debug("dropped an ACK not routed through the S-CSCF", slog.String("call-id", ack.Header.CallID()))
		return
	}

	to, err := s.nextHop(out, ack.Flow)
	if err != nil {
		s.log.Debug("no next hop for an ACK", slog.String("call-id", ack.Header.CallID()), slog.Any("error", err))
		return
	}

	if err := s.proxy.ForwardAck(out, to, s.proxy.Dialog(removed)); err != nil {
		s.log.Debug("ACK not forwarded", slog.String("call-id", ack.Header.CallID()), slog.Any("error", err))
	}
}

func originatingContact(removed []sip.URI) (int64, bool, error) {
	for _, u := range removed {
		if id, ok := strings.CutPrefix(u.User, "orig-"); ok {
			contactID, err := strconv.ParseInt(id, 10, 64)
			return contactID, true, err
		}
	}

	return 0, false, nil
}

func (s *Sessions) originating(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, contactID int64) {
	req := tx.Request()

	asserted, err := out.Header.Addresses("P-Asserted-Identity")
	if err != nil {
		s.answer(tx, sip.NewResponse(req, 400, "Bad P-Asserted-Identity"))
		return
	}

	served, reg, res := s.servedUser(ctx, req, asserted, contactID)
	if res != nil {
		s.answer(tx, res)
		return
	}

	if out.Method == "MESSAGE" {
		s.log.Debug("MESSAGE refused: no IP-SM-GW", slog.String("impu", served.URI.String()))
		s.answer(tx, sip.NewResponse(req, messageRejectCode, messageRejectReason))

		return
	}

	out.Header.Del("P-Served-User")
	out.Header.Del("P-Access-Network-Info")
	icsi, ok := s.assertedService(out, served)

	out.Header.Del("P-Preferred-Service")
	out.Header.Del("P-Asserted-Service")

	if ok {
		out.Header.Add("P-Asserted-Service", icsi)
	}

	out.Header.Del("P-Asserted-Identity")
	out.Header.Add("P-Asserted-Identity", served.String())

	if alias, ok := assertedAlias(served, reg, s.r.cfg.HomeDomain); ok {
		out.Header.Add("P-Asserted-Identity", alias.String())
	}

	if u, ok := s.cfg.Numbering.normalise(out.URI, s.r.cfg.HomeDomain); ok {
		s.log.Debug("home-local number normalised", slog.String("dialled", out.URI.String()), slog.String("number", u.String()))
		out.URI = u
	}

	if localNumber(out.URI, s.r.cfg.HomeDomain) {
		s.log.Debug("local number not translated to a global number", slog.String("impu", served.URI.String()), slog.String("dialled", out.URI.String()))
		s.answer(tx, sip.NewResponse(req, 404, ""))

		return
	}

	to, ok := s.icscf(req.Flow)
	if !ok {
		s.log.Warn("no I-CSCF listener for an originating request", slog.String("local", req.Flow.Local.String()))
		s.answer(tx, sip.NewResponse(req, 500, ""))

		return
	}

	if out.Header.Has("Route") {
		s.log.Debug("routes after the Service-Route dropped", slog.Any("route", out.Header.Values("Route")))
		out.Header.Del("Route")
	}

	route := sip.URI{Scheme: "sip", Host: sip.FormatHost(to.Flow.Remote.Addr()), Port: to.Flow.Remote.Port()}
	if to.Flow.Transport != sip.UDP {
		route.Params.Set("transport", strings.ToLower(string(to.Flow.Transport)))
	}

	route.Params.Set("lr", "")
	out.Header.Prepend("Route", "<"+route.String()+">")

	s.forward(tx, out, to, s.initialOptions(out, rrOriginating, nil))
}

// TS 24.229 §5.4.3.2 steps 4C and 4D, TS 24.173 §5.2
func (s *Sessions) assertedService(out *sip.Request, served sip.Address) (string, bool) {
	preferred := out.Header.Elements("P-Preferred-Service")

	for _, v := range preferred {
		if strings.EqualFold(strings.TrimSpace(v), icsiMMTel) {
			return icsiMMTel, true
		}
	}

	if len(preferred) > 0 {
		s.log.Debug("unsupported preferred service dropped", slog.String("impu", served.URI.String()), slog.Any("service", preferred))
		return "", false
	}

	return icsiMMTel, out.Method == "INVITE"
}

// TS 24.229 §5.4.3.2 step 1
func (s *Sessions) servedUser(ctx context.Context, req *sip.Request, asserted []sip.Address, contactID int64,
) (sip.Address, db.Registration, *sip.Response) {
	now := s.r.clock.Now()

	for _, a := range asserted {
		for _, key := range identityKeys(a.URI, s.r.cfg.HomeDomain) {
			regs, err := s.r.cfg.DB.ListRegistrationsByIdentity(ctx, key)
			if err != nil {
				s.log.Warn("failed to read the registrations", slog.String("impu", a.URI.String()), slog.Any("error", err))
				return sip.Address{}, db.Registration{}, retryLater(req)
			}

			for _, reg := range regs {
				if !slices.ContainsFunc(liveAt(reg.Bindings, now), func(b db.Binding) bool { return b.Contact.ID == contactID }) {
					continue
				}

				i := slices.IndexFunc(reg.Identities, func(id db.PublicIdentity) bool { return id.Key == key })
				if i < 0 {
					continue
				}

				if reg.Identities[i].Barred {
					s.log.Debug("originating request from a barred identity", slog.String("impu", a.URI.String()))
					return sip.Address{}, db.Registration{}, sip.NewResponse(req, 403, "Barred")
				}

				served := sip.Address{Display: a.Display, URI: a.URI}

				return served, reg, nil
			}
		}
	}

	s.log.Debug("originating request from an unregistered identity", slog.Any("p-asserted-identity", req.Header.Values("P-Asserted-Identity")),
		slog.Int64("contact", contactID))

	return sip.Address{}, db.Registration{}, sip.NewResponse(req, 403, "Not Registered")
}

func (s *Sessions) terminating(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request) {
	req := tx.Request()
	called := out.URI

	keys := identityKeys(called, s.r.cfg.HomeDomain)

	var regs []db.Registration

	for _, key := range keys {
		found, err := s.r.cfg.DB.ListRegistrationsByIdentity(ctx, key)
		if err != nil {
			s.log.Warn("failed to read the registrations", slog.String("impu", called.String()), slog.Any("error", err))
			s.answer(tx, retryLater(req))

			return
		}

		for _, reg := range found {
			if !slices.ContainsFunc(regs, func(r db.Registration) bool { return r.ID == reg.ID }) {
				regs = append(regs, reg)
			}
		}
	}

	for _, reg := range regs {
		if slices.ContainsFunc(reg.Identities, func(id db.PublicIdentity) bool { return id.Barred && slices.Contains(keys, id.Key) }) {
			s.log.Debug("request to a barred identity", slog.String("impu", called.String()))
			s.answer(tx, sip.NewResponse(req, 404, ""))

			return
		}
	}

	if out.Header.Has("Route") {
		to, err := s.nextHop(out, req.Flow)
		if err != nil {
			s.answer(tx, sip.NewResponse(req, 404, "No Route"))
			return
		}

		var onReply func(proxy.Reply) proxy.Verdict

		if len(regs) > 0 {
			onReply = s.aliasReply(regs[0])
		}

		s.forward(tx, out, to, s.initialOptions(out, rrTerminating, onReply))

		return
	}

	now := s.r.clock.Now()

	var bindings []db.Binding

	for _, reg := range regs {
		bindings = append(bindings, liveAt(reg.Bindings, now)...)
	}

	b, ok := selectBinding(bindings, preferences(out.Header))
	if !ok {
		s.log.Debug("request to an unreachable user", slog.String("impu", called.String()), slog.Int("bindings", len(bindings)))
		s.answer(tx, sip.NewResponse(req, 480, ""))

		return
	}

	contact, err := sip.ParseURI(b.Contact.URI)
	if err != nil {
		s.log.Warn("unusable registered contact", slog.String("contact", b.Contact.URI), slog.Any("error", err))
		s.answer(tx, sip.NewResponse(req, 480, ""))

		return
	}

	reg := regs[slices.IndexFunc(regs, func(r db.Registration) bool {
		return slices.ContainsFunc(r.Bindings, func(rb db.Binding) bool { return rb.Contact.ID == b.Contact.ID })
	})]

	out.URI = contact
	out.Header.Del("P-Called-Party-ID")
	out.Header.Add("P-Called-Party-ID", "<"+calledPartyID(called).String()+">")
	out.Header.Del("P-User-Database")
	out.Header.Del("P-Served-User")

	if b.Contact.Path != "" {
		out.Header.Prepend("Route", b.Contact.Path)
	}

	to, err := s.nextHop(out, req.Flow)
	if err != nil {
		s.log.Warn("no route to the registered contact", slog.String("contact", b.Contact.URI),
			slog.String("path", b.Contact.Path), slog.Any("error", err))
		s.answer(tx, sip.NewResponse(req, 480, ""))

		return
	}

	s.forward(tx, out, to, s.initialOptions(out, rrTerminating, s.aliasReply(reg)))
}

func (s *Sessions) aliasReply(reg db.Registration) func(proxy.Reply) proxy.Verdict {
	return func(r proxy.Reply) proxy.Verdict {
		if res := r.Response; res != nil && res.StatusCode > 100 && res.StatusCode < 300 {
			s.assertAlias(res, reg)
		}

		return proxy.Relay
	}
}

// TS 24.229 §5.4.3.3
func (s *Sessions) assertAlias(res *sip.Response, reg db.Registration) {
	asserted, err := res.Header.Addresses("P-Asserted-Identity")
	if err != nil || len(asserted) != 1 {
		return
	}

	if alias, ok := assertedAlias(asserted[0], reg, s.r.cfg.HomeDomain); ok {
		res.Header.Add("P-Asserted-Identity", alias.String())
	}
}

// TS 24.229 §5.4.3.2 step 13, §5.4.3.3 step 13
func (s *Sessions) initialOptions(out *sip.Request, user string, onReply func(proxy.Reply) proxy.Verdict) proxy.Options {
	opts := proxy.Options{OnReply: onReply}

	switch out.Method {
	case "INVITE":
		opts.Dialog = s.proxy.NewDialog(proxy.DialogConfig{})
		fallthrough
	case "SUBSCRIBE", "REFER":
		opts.RecordRoute = &proxy.RecordRoute{User: user}
	}

	return opts
}

// TS 24.229 §5.4.3.2, §5.4.3.3
func (s *Sessions) inDialog(tx *transaction.ServerTransaction, out *sip.Request, removed []sip.URI) {
	req := tx.Request()

	if len(removed) == 0 {
		s.log.Debug("in-dialog request not routed through the S-CSCF", slog.String("request", req.StartLine()),
			slog.String("source", req.Flow.Remote.String()))
		s.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	d := s.proxy.Dialog(removed)
	if d != nil && d.Released() {
		s.respond(tx, sip.NewResponse(req, 481, ""))
		return
	}

	to, err := s.nextHop(out, req.Flow)
	if err != nil {
		s.respond(tx, sip.NewResponse(req, 404, "No Route"))
		return
	}

	opts := proxy.Options{Dialog: d}

	switch out.Method {
	case "INVITE", "UPDATE", "SUBSCRIBE", "NOTIFY":
		opts.RecordRoute = recordRouteOf(removed)
	}

	s.forward(tx, out, to, opts)
}

func recordRouteOf(removed []sip.URI) *proxy.RecordRoute {
	i := slices.IndexFunc(removed, func(u sip.URI) bool { return u.IsLooseRouter() })
	if i < 0 {
		i = len(removed) - 1
	}

	u := removed[i]

	rr := &proxy.RecordRoute{User: u.User}

	for _, p := range u.Params {
		switch strings.ToLower(p.Name) {
		case "lr", "r2", "transport":
		default:
			rr.Params = append(rr.Params, p)
		}
	}

	return rr
}

func (s *Sessions) nextHop(out *sip.Request, in sip.Flow) (proxy.Target, error) {
	tr, dest, err := sip.NextHop(out)
	if err != nil {
		return proxy.Target{}, err
	}

	local, ok := s.local(in, dest.Addr())
	if !ok {
		return proxy.Target{}, errors.New("no S-CSCF listener in the address family of " + dest.Addr().String())
	}

	return proxy.Target{Flow: sip.Flow{Transport: tr, Local: local, Remote: dest}}, nil
}

func (s *Sessions) local(in sip.Flow, dest netip.Addr) (netip.AddrPort, bool) {
	if in.Local.Addr().Unmap().Is4() == dest.Unmap().Is4() {
		return in.Local, true
	}

	for _, l := range s.r.cfg.Listeners {
		if l.Addr().Unmap().Is4() == dest.Unmap().Is4() {
			return l, true
		}
	}

	return netip.AddrPort{}, false
}

func (s *Sessions) icscf(in sip.Flow) (proxy.Target, bool) {
	local := in.Local.Addr().Unmap()

	i := slices.IndexFunc(s.cfg.ICSCF, func(l netip.AddrPort) bool { return l.Addr().Unmap() == local })
	if i < 0 {
		i = slices.IndexFunc(s.cfg.ICSCF, func(l netip.AddrPort) bool { return l.Addr().Unmap().Is4() == local.Is4() })
	}

	if i < 0 {
		return proxy.Target{}, false
	}

	tr := sip.UDP
	if in.Transport == sip.TCP {
		tr = sip.TCP
	}

	return proxy.Target{Flow: sip.Flow{Transport: tr, Local: in.Local, Remote: s.cfg.ICSCF[i]}}, true
}

func (s *Sessions) forward(tx *transaction.ServerTransaction, out *sip.Request, to proxy.Target, opts proxy.Options) {
	err := s.proxy.Forward(tx, out, to, opts)
	if err == nil || errors.Is(err, proxy.ErrAnswered) {
		return
	}

	code := 500

	if serr, ok := errors.AsType[*sip.StatusError](err); ok {
		code = serr.StatusCode
	}

	s.log.Debug("forwarding failed", slog.String("request", out.StartLine()), slog.Any("error", err))
	s.answer(tx, sip.NewResponse(tx.Request(), code, ""))
}

func (s *Sessions) answer(tx *transaction.ServerTransaction, res *sip.Response) {
	if err := s.proxy.Relay(tx, res); err == nil {
		return
	}

	s.respond(tx, res)
}

func (s *Sessions) respond(tx *transaction.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		s.log.Debug("SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}
