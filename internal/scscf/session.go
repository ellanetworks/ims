package scscf

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	messageRejectCode   = 403
	messageRejectReason = "SMS over IP Not Supported"
)

const (
	icsiMMTel  = "urn:urn-7:3gpp-service.ims.icsi.mmtel"
	icsiPrefix = "urn:urn-7:3gpp-service."
)

const contentTypeSMS = "application/vnd.3gpp.sms"

const (
	rrOriginating = "mo"
	rrTerminating = "mt"
)

type SessionConfig struct {
	Proxy *proxy.Proxy

	ICSCF []netip.AddrPort

	// Numbering returns the numbering plan when a request is routed, so that it can change at runtime. Without
	// it, no dial string is normalised.
	Numbering func() Numbering

	// GroupNoAnswer is how long the contacts of a group ring before the next group gets the request;
	// zero is DefaultGroupNoAnswer.
	GroupNoAnswer time.Duration
}

type Sessions struct {
	r     *Registrar
	cfg   SessionConfig
	proxy *proxy.Proxy
	log   *slog.Logger

	mu   sync.Mutex
	hops map[string]*hop
}

func (s *Sessions) numbering() Numbering {
	if s.cfg.Numbering == nil {
		return Numbering{}
	}

	return s.cfg.Numbering()
}

func (r *Registrar) Sessions(cfg SessionConfig) *Sessions {
	return &Sessions{r: r, cfg: cfg, proxy: cfg.Proxy, log: r.log, hops: make(map[string]*hop)}
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

	// An original dialog identifier of the S-CSCF's that names nothing: the request would otherwise run iFCs the
	// AS's request was not sent for, or none (TS 24.229 §5.4.3.2 step 3, §5.4.3.3 step 1).
	h, odi := s.hopOf(removed)
	if odi && h == nil {
		s.log.Warn("request with an unknown original dialog identifier", slog.String("request", req.StartLine()),
			slog.String("source", req.Flow.Remote.String()))
		s.respond(tx, sip.NewResponse(req, 403, "Unknown Original Dialog Identifier"))

		return
	}

	bindingID, orig, err := originatingBinding(removed)
	if err != nil {
		s.log.Debug("originating request on a malformed Service-Route", slog.String("request", req.StartLine()), slog.Any("error", err))
		s.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	s.spawn(tx, func(ctx context.Context) {
		switch {
		case h != nil:
			s.resume(ctx, tx, out, h)
		case orig:
			s.originating(ctx, tx, out, bindingID)
		case slices.ContainsFunc(removed, func(u sip.URI) bool { return u.Params.Has("orig") }):
			s.onBehalf(ctx, tx, out)
		default:
			s.terminating(ctx, tx, out)
		}
	})
}

// spawn runs f for a request on a goroutine of its own while the S-CSCF runs, and answers the request with a 500
// and a Retry-After when it cannot.
func (s *Sessions) spawn(tx *transaction.ServerTransaction, f func(ctx context.Context)) {
	req := tx.Request()

	err := s.r.cfg.Layer.Go(func(ctx context.Context) {
		ctx, done, ok := s.r.begin(ctx)
		if !ok {
			s.answer(tx, retryLater(req))
			return
		}

		defer done()

		f(ctx)
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

	forward := func(ctx context.Context) {
		to, err := s.nextHop(ctx, out, ack.Flow)
		if err != nil {
			s.log.Debug("no next hop for an ACK", slog.String("call-id", ack.Header.CallID()), slog.Any("error", err))
			return
		}

		if err := s.proxy.ForwardAck(out, to, s.proxy.Dialog(removed)); err != nil {
			s.log.Debug("ACK not forwarded", slog.String("call-id", ack.Header.CallID()), slog.Any("error", err))
		}
	}

	if !needsDNS(out) {
		forward(context.Background())
		return
	}

	err = s.r.cfg.Layer.Go(func(ctx context.Context) {
		ctx, done, ok := s.r.begin(ctx)
		if !ok {
			return
		}

		defer done()

		forward(ctx)
	})
	if err != nil {
		s.log.Debug("dropped an ACK", slog.String("call-id", ack.Header.CallID()), slog.Any("error", err))
	}
}

// originatingBinding is the binding the Service-Route the request came with names (TS 24.229 §5.4.1.2.2F c).
func originatingBinding(removed []sip.URI) (int64, bool, error) {
	for _, u := range removed {
		if id, ok := strings.CutPrefix(u.User, "orig-"); ok {
			bindingID, err := strconv.ParseInt(id, 10, 64)
			return bindingID, true, err
		}
	}

	return 0, false, nil
}

// originating serves a request of a registered UE (TS 24.229 §5.4.3.2).
func (s *Sessions) originating(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, routeBinding int64) {
	req := tx.Request()

	asserted, err := out.Header.Addresses("P-Asserted-Identity")
	if err != nil {
		s.answer(tx, sip.NewResponse(req, 400, "Bad P-Asserted-Identity"))
		return
	}

	served, reg, binding, res := s.servedUser(ctx, req, asserted, routeBinding)
	if res != nil {
		s.answer(tx, res)
		return
	}

	out.Header.Del("P-Served-User")
	out.Header.Del("P-Access-Network-Info")
	s.assertService(out, served, false)

	sub, err := cx.ParseUserData(reg.UserData)
	if err != nil {
		s.log.Warn("invalid user data of a registration: no service", slog.String("impu", served.URI.String()), slog.Any("error", err))
	}

	out.Header.Del("P-Asserted-Identity")
	out.Header.Add("P-Asserted-Identity", served.String())

	if alias, ok := assertedAlias(served, sub, s.r.cfg.HomeDomain); ok {
		out.Header.Add("P-Asserted-Identity", alias.String())
	}

	if out.Header.Has("Route") {
		s.log.Debug("routes after the Service-Route dropped", slog.Any("route", out.Header.Values("Route")))
		out.Header.Del("Route")
	}

	c := &chain{
		served: served, sescase: cx.SessionCaseOriginatingRegistered, sub: sub,
		binding: binding, token: bindingToken(binding),
	}

	if err == nil {
		p, ok := serviceProfile(sub, served.URI, s.r.cfg.HomeDomain)
		if !ok {
			s.log.Warn("no service profile holds the identity: no service", slog.String("impu", served.URI.String()))
		}

		c.ifcs = s.r.criteria(p, true)
	}

	s.next(ctx, tx, out, c, 0, true)
}

// onBehalf serves a request an AS of the trust domain sends on behalf of a user or a PSI, its served user in
// P-Served-User or else P-Asserted-Identity (TS 24.229 §5.4.3.1, §5.4.3.2 step 1, §5.7.3).
func (s *Sessions) onBehalf(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request) {
	req := tx.Request()

	served, err := out.Header.Addresses("P-Served-User")
	if err == nil && len(served) == 0 {
		served, err = out.Header.Addresses("P-Asserted-Identity")
	}

	if err != nil || len(served) == 0 {
		s.log.Debug("request on behalf of no identity", slog.String("request", req.StartLine()))
		s.answer(tx, sip.NewResponse(req, 403, ""))

		return
	}

	u := served[0].URI

	c, res := s.servedChain(ctx, req, u, cx.SessionCaseOriginatingRegistered, cx.SessionCaseOriginatingUnregistered, 403)
	if res != nil {
		s.answer(tx, res)
		return
	}

	c.served = sip.Address{Display: served[0].Display, URI: u}

	out.Header.Del("P-Served-User")
	s.assertService(out, c.served, true)

	// TS 24.229 §5.4.3.2 step 9: the alias of the identity the AS asserts, which no S-CSCF added yet.
	if asserted, err := out.Header.Addresses("P-Asserted-Identity"); err == nil && len(asserted) == 1 {
		if alias, ok := assertedAlias(asserted[0], c.sub, s.r.cfg.HomeDomain); ok {
			out.Header.Add("P-Asserted-Identity", alias.String())
		}
	}

	if out.Header.Has("Route") {
		s.log.Debug("routes after the S-CSCF dropped", slog.Any("route", out.Header.Values("Route")))
		out.Header.Del("Route")
	}

	s.next(ctx, tx, out, c, 0, true)
}

// servedChain is the chain of the iFCs of the served identity u, registered or not, in the session case for either;
// a barred or unknown identity gets the response refused (TS 24.229 §5.4.3.2 step 1, §5.4.3.3 step 1).
func (s *Sessions) servedChain(ctx context.Context, req *sip.Request, u sip.URI, registered, unregistered cx.SessionCase,
	refused int,
) (*chain, *sip.Response) {
	regs, barred, err := Recipients(ctx, s.r.cfg.DB, u, s.r.cfg.HomeDomain)

	switch {
	case err != nil:
		s.log.Warn("failed to read the registrations", slog.String("impu", u.String()), slog.Any("error", err))
		return nil, retryLater(req)
	case barred:
		s.log.Debug("request of a barred identity", slog.String("impu", u.String()))
		return nil, sip.NewResponse(req, refused, "")
	}

	p, sub, isRegistered, err := s.r.profileOf(ctx, u, regs)

	// An HSS that cannot be reached answers as the I-CSCF does when its query cannot be completed: a 480
	// (TS 24.229 §5.3.1.3).
	switch {
	case refusedIdentity(err):
		s.log.Debug("the HSS does not know the identity", slog.String("impu", u.String()), slog.Any("error", err))
		return nil, sip.NewResponse(req, refused, "")
	case errors.Is(err, errNoUserData), errors.Is(err, cx.ErrInvalidUserData), permanent(err):
		s.log.Info("no service profile for the identity", slog.String("impu", u.String()), slog.Any("error", err))
		return nil, sip.NewResponse(req, 480, "")
	case err != nil:
		s.log.Warn("failed to get the service profile", slog.String("impu", u.String()), slog.Any("error", err))
		return nil, sip.NewResponse(req, 480, "")
	case barredIn(p, u, s.r.cfg.HomeDomain):
		s.log.Debug("request of a barred identity", slog.String("impu", u.String()))
		return nil, sip.NewResponse(req, refused, "")
	}

	c := &chain{
		served: sip.Address{URI: u}, sescase: unregistered, ruri: u, regs: regs, sub: sub,
		ifcs: s.r.criteria(p, isRegistered),
	}
	if isRegistered {
		c.sescase = registered
	}

	return c, nil
}

// toICSCF routes a request that leaves the chain of the served user through the I-CSCF (TS 24.229 §5.4.3.2 steps
// 7 to 15).
func (s *Sessions) toICSCF(tx *transaction.ServerTransaction, out *sip.Request, c *chain, first bool) {
	req := tx.Request()

	if out.Method == "MESSAGE" && out.Content().MediaType() == contentTypeSMS {
		s.log.Debug("MESSAGE refused: no IP-SM-GW", slog.String("impu", c.served.URI.String()))
		s.answer(tx, sip.NewResponse(req, messageRejectCode, messageRejectReason))

		return
	}

	out.Header.Del("P-Served-User")
	withoutRegistrationToken(&out.Header)

	if u, ok := s.numbering().normalise(out.URI, s.r.cfg.HomeDomain); ok {
		s.log.Debug("home-local number normalised", slog.String("dialled", out.URI.String()), slog.String("number", u.String()))
		out.URI = u
	}

	if localNumber(out.URI, s.r.cfg.HomeDomain) {
		s.log.Debug("local number not translated to a global number", slog.String("impu", c.served.URI.String()),
			slog.String("dialled", out.URI.String()))
		s.answer(tx, sip.NewResponse(req, 404, ""))

		return
	}

	to, ok := s.icscf(req.Flow)
	if !ok {
		s.log.Warn("no I-CSCF listener for an originating request", slog.String("local", req.Flow.Local.String()))
		s.answer(tx, sip.NewResponse(req, 500, ""))

		return
	}

	route := sip.URI{Scheme: "sip", Host: sip.FormatHost(to.Flow.Remote.Addr()), Port: to.Flow.Remote.Port()}
	if to.Flow.Transport != sip.UDP {
		route.Params.Set("transport", strings.ToLower(string(to.Flow.Transport)))
	}

	route.Params.Set("lr", "")

	r := out.Clone()
	r.Header.Prepend("Route", "<"+route.String()+">")

	rr := rrOriginating
	if !c.originating() {
		rr = rrTerminating
	}

	opts := s.initialOptions(r, rr, first)
	if opts.Dialog != nil && c.binding != 0 {
		s.r.calls.add(opts.Dialog, bindingKey(c.binding), proxy.Caller, c.servedKeys(s.r.cfg.HomeDomain))
	}

	// The call as it leaves the S-CSCF, through any AS (TS 24.229 §5.4.3.2 step 15).
	if r.Method == "INVITE" && c.originating() {
		asserted, _ := r.Header.Addresses("P-Asserted-Identity")

		ids := make([]string, 0, len(asserted))
		for _, a := range asserted {
			ids = append(ids, a.URI.String())
		}

		s.r.cfg.Records.Routed(r.Header.ICID(), callrecords.Routing{Asserted: ids, RequestURI: r.URI.String()})
	}

	s.forward(tx, r, to, opts)
}

// assertService puts the ICSIs of the request in P-Asserted-Service (TS 24.229 §5.4.3.2 steps 4C, 4D): those the
// request prefers, as the profile restricts no service (TS 29.228 Annex B.2.1A); else, from a trusted AS, those it
// asserted; else those its Accept-Contact requires; else MMTel for an INVITE (TS 24.173 §5.2).
func (s *Sessions) assertService(out *sip.Request, served sip.Address, trusted bool) {
	preferred, err := out.Header.PreferredServices()
	if err != nil {
		s.log.Debug("malformed preferred service dropped", slog.String("impu", served.URI.String()), slog.Any("error", err))
	}

	out.Header.Del("P-Preferred-Service")

	var icsis []string

	for _, id := range preferred {
		if strings.HasPrefix(id, icsiPrefix) {
			icsis = append(icsis, id)
		}
	}

	if len(icsis) == 0 && len(preferred) > 0 {
		s.log.Debug("preferred service without an ICSI dropped", slog.String("impu", served.URI.String()), slog.Any("service", preferred))
	}

	if len(icsis) == 0 && len(preferred) == 0 {
		if asserted, err := out.Header.AssertedServices(); trusted && err == nil && len(asserted) > 0 {
			return
		}

		icsis = requiredICSIs(out.Header)
	}

	if len(icsis) == 0 && len(preferred) == 0 && out.Method == "INVITE" {
		icsis = []string{icsiMMTel}
	}

	out.Header.Del("P-Asserted-Service")

	for _, id := range icsis {
		out.Header.Add("P-Asserted-Service", id)
	}
}

// requiredICSIs are the ICSIs of the g.3gpp.icsi-ref of the Accept-Contact values that require them (RFC 3841 §9.2,
// TS 24.229 §7.2A.8).
func requiredICSIs(h sip.Header) []string {
	prefs, _ := h.AcceptContact()

	var out []string

	for _, p := range prefs {
		if !p.Require {
			continue
		}

		for _, id := range p.Features.Tokens("g.3gpp.icsi-ref") {
			if id = strings.ToLower(id); sip.IsServiceID(id) && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}

	return out
}

// TS 24.229 §5.4.3.2 step 1: the served user is an asserted identity registered with the contact that the
// Service-Route's binding binds. The UE may assert an identity of another of its registration sets than the one
// whose Service-Route it uses. The binding of the served user's set is returned.
func (s *Sessions) servedUser(ctx context.Context, req *sip.Request, asserted []sip.Address, bindingID int64,
) (sip.Address, db.Registration, int64, *sip.Response) {
	route, impi, err := s.r.cfg.DB.GetBinding(ctx, bindingID)

	switch {
	case errors.Is(err, db.ErrNotFound):
		s.log.Debug("originating request on an unknown Service-Route", slog.Int64("binding", bindingID))
		return sip.Address{}, db.Registration{}, 0, sip.NewResponse(req, 403, "Not Registered")
	case err != nil:
		s.log.Warn("failed to read the binding", slog.Int64("binding", bindingID), slog.Any("error", err))
		return sip.Address{}, db.Registration{}, 0, retryLater(req)
	}

	now := s.r.clock.Now()

	for _, a := range asserted {
		for _, key := range identityKeys(a.URI, s.r.cfg.HomeDomain) {
			regs, err := s.r.cfg.DB.ListRegistrationsByIdentity(ctx, key)
			if err != nil {
				s.log.Warn("failed to read the registrations", slog.String("impu", a.URI.String()), slog.Any("error", err))
				return sip.Address{}, db.Registration{}, 0, retryLater(req)
			}

			for _, reg := range regs {
				if reg.IMPI != impi {
					continue
				}

				live := liveAt(reg.Bindings, now)

				b := slices.IndexFunc(live, func(b db.Binding) bool { return SameContact(b.Contact, route.Contact) })
				if b < 0 {
					continue
				}

				i := slices.IndexFunc(reg.Identities, func(id db.PublicIdentity) bool { return id.Key == key })
				if i < 0 {
					continue
				}

				if reg.Identities[i].Barred {
					s.log.Debug("originating request from a barred identity", slog.String("impu", a.URI.String()))
					return sip.Address{}, db.Registration{}, 0, sip.NewResponse(req, 403, "Barred")
				}

				served := sip.Address{Display: a.Display, URI: a.URI}

				return served, reg, live[b].ID, nil
			}
		}
	}

	s.log.Debug("originating request from an unregistered identity", slog.Any("p-asserted-identity", req.Header.Values("P-Asserted-Identity")),
		slog.Int64("binding", bindingID))

	return sip.Address{}, db.Registration{}, 0, sip.NewResponse(req, 403, "Not Registered")
}

// terminating serves a request to a served user, registered or not, or to a PSI (TS 24.229 §5.4.3.3).
func (s *Sessions) terminating(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request) {
	req := tx.Request()

	if out.Header.Has("Route") {
		regs, barred, err := Recipients(ctx, s.r.cfg.DB, out.URI, s.r.cfg.HomeDomain)

		switch {
		case err != nil:
			s.log.Warn("failed to read the registrations", slog.String("impu", out.URI.String()), slog.Any("error", err))
			s.answer(tx, retryLater(req))

			return
		case barred:
			s.log.Debug("request to a barred identity", slog.String("impu", out.URI.String()))
			s.answer(tx, sip.NewResponse(req, 404, ""))

			return
		}

		to, err := s.nextHop(ctx, out, req.Flow)
		if err != nil {
			s.log.Debug("no next hop", slog.String("request", out.StartLine()), slog.Any("error", err))
			s.answer(tx, sip.NewResponse(req, 404, "No Route"))

			return
		}

		var onReply func(proxy.Reply) proxy.Verdict

		if len(regs) > 0 {
			onReply = s.aliasReply(regs[0], "", out.Method)
		}

		opts := s.initialOptions(out, rrTerminating, true)
		opts.OnReply = onReply

		s.forward(tx, out, to, opts)

		return
	}

	c, res := s.servedChain(ctx, req, out.URI, cx.SessionCaseTerminatingRegistered, cx.SessionCaseTerminatingUnregistered, 404)
	if res != nil {
		s.answer(tx, res)
		return
	}

	s.next(ctx, tx, out, c, 0, true)
}

// deliver forks the request to the registered contacts of the served user (TS 24.229 §5.4.3.3 steps 8 to 14); viaAS
// tells that it comes back from an AS.
func (s *Sessions) deliver(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, c *chain, viaAS bool) {
	req := tx.Request()
	called := out.URI

	out.Header.Del("P-Served-User")

	opts := s.initialOptions(out, rrTerminating, true)
	set, step := targetSet(c.regs, s.r.clock.Now(), out.Header)

	var groups [][]proxy.Branch

	for _, g := range set {
		var bs []proxy.Branch

		for _, t := range g {
			b, ok := s.branch(ctx, out, called, t, opts, req.Flow, viaAS)
			if !ok {
				continue
			}

			for _, other := range t.others {
				if r, ok := s.branch(ctx, out, called, other, opts, req.Flow, viaAS); ok {
					b.Retry = append(b.Retry, r)
				}
			}

			if opts.Dialog != nil {
				for _, r := range append([]proxy.Branch{b}, b.Retry...) {
					s.r.calls.add(opts.Dialog, r.Key.(bindingKey), proxy.Callee, c.servedKeys(s.r.cfg.HomeDomain))
				}
			}

			bs = append(bs, b)
		}

		if len(bs) > 0 {
			groups = append(groups, bs)
		}
	}

	// Every group but the last that is left rings for a while before the next one gets the request.
	if step {
		for _, g := range groups[:max(len(groups)-1, 0)] {
			for i := range g {
				g[i].Options.NoAnswer = cmp.Or(s.cfg.GroupNoAnswer, DefaultGroupNoAnswer)

				for j := range g[i].Retry {
					g[i].Retry[j].Options.NoAnswer = g[i].Options.NoAnswer
				}
			}
		}
	}

	if len(groups) == 0 {
		s.log.Debug("request to an unreachable user", slog.String("impu", called.String()))
		s.answer(tx, sip.NewResponse(req, 480, ""))

		return
	}

	err := s.proxy.Fork(tx, groups)

	// A fork that failed, or found the request answered already (a CANCEL came first), never began
	// the dialog: it would never end.
	if err != nil && opts.Dialog != nil {
		s.r.calls.forget(opts.Dialog)
	}

	if err == nil || errors.Is(err, proxy.ErrAnswered) {
		return
	}

	code := 500
	if serr, ok := errors.AsType[*sip.StatusError](err); ok {
		code = serr.StatusCode
	}

	s.log.Debug("forking failed", slog.String("request", out.StartLine()), slog.Any("error", err))
	s.answer(tx, sip.NewResponse(req, code, ""))
}

// DefaultGroupNoAnswer is how long the contacts of a group ring before the next group gets the
// request.
const DefaultGroupNoAnswer = 20 * time.Second

// branch is the request to one registered contact (TS 24.229 §5.4.3.3 step 10).
func (s *Sessions) branch(ctx context.Context, out *sip.Request, called sip.URI, t target, opts proxy.Options, in sip.Flow,
	viaAS bool,
) (proxy.Branch, bool) {
	contact, err := sip.ParseURI(t.binding.Contact.URI)
	if err != nil {
		s.log.Warn("unusable registered contact", slog.String("contact", t.binding.Contact.URI), slog.Any("error", err))
		return proxy.Branch{}, false
	}

	r := out.Clone()
	r.URI = contact
	r.Header.Del("P-Called-Party-ID")
	r.Header.Add("P-Called-Party-ID", "<"+calledPartyID(called).String()+">")
	r.Header.Del("P-User-Database")
	r.Header.Del("P-Served-User")

	if t.binding.Contact.Path != "" {
		r.Header.Prepend("Route", t.binding.Contact.Path)
	}

	to, err := s.nextHop(ctx, r, in)
	if err != nil {
		s.log.Warn("no route to the registered contact", slog.String("contact", t.binding.Contact.URI),
			slog.String("path", t.binding.Contact.Path), slog.Any("error", err))

		return proxy.Branch{}, false
	}

	token := ""
	if viaAS {
		token = bindingToken(t.binding.ID)
	}

	opts.OnReply = s.pcscfFailure(t, s.aliasReply(t.reg, token, out.Method))

	return proxy.Branch{Request: r, Target: to, Options: opts, Key: bindingKey(t.binding.ID)}, true
}

// aliasReply completes the 1xx and 2xx responses of the served user: the alias of its asserted identity, and the token
// of the registration that answered, for the ASs they go back through, in the 1xx and 2xx responses that begin a
// dialog and in the 200 of a standalone transaction (TS 24.229 §5.4.3.3 steps 2b, 3 and 6, steps 1A and 1B).
func (s *Sessions) aliasReply(reg db.Registration, token, method string) func(proxy.Reply) proxy.Verdict {
	// The subscription of the registration, parsed for the first response that asserts an identity: an invalid one
	// holds no alias.
	sub := sync.OnceValue(func() cx.IMSSubscription {
		sub, _ := cx.ParseUserData(reg.UserData)
		return sub
	})

	return func(r proxy.Reply) proxy.Verdict {
		res := r.Response
		if res == nil || res.StatusCode <= 100 || res.StatusCode >= 300 {
			return proxy.Relay
		}

		s.assertAlias(res, sub)
		withoutRegistrationToken(&res.Header)

		if token != "" && (dialogCreating(method) || res.StatusCode == 200) {
			withRegistrationToken(&res.Header, token)
		}

		return proxy.Relay
	}
}

// pcscfFailure takes the 404 (Not Found) of a P-CSCF that has no binding for the UE (TS 24.229 §5.2.6.4.1) as the
// failure of the flow: the UE's other flows are tried, and the response upstream is a 480 (Temporarily Unavailable)
// when none is left (RFC 5626 §7, §11.5). The registrar removes the binding, and starts the HSS based P-CSCF
// restoration when the UE has no other (TS 24.229 §5.4.3.3, TS 23.380 §5.4.1).
func (s *Sessions) pcscfFailure(t target, next func(proxy.Reply) proxy.Verdict) func(proxy.Reply) proxy.Verdict {
	return func(r proxy.Reply) proxy.Verdict {
		if res := r.Response; res != nil && res.StatusCode == 404 && pcscfNotFound(t.binding.Contact.Path, res) {
			s.log.Info("the P-CSCF has no binding for the contact", slog.String("impi", t.reg.IMPI),
				slog.String("contact", t.binding.Contact.URI))

			res.StatusCode, res.Reason = 430, sip.ReasonPhrase(430)

			s.r.pcscfLost(t.reg.IMPI, t.reg.ID, t.binding.ID)
		}

		return next(r)
	}
}

// pcscfNotFound reports whether a 404 (Not Found) toward a binding with the Path is the P-CSCF's, for no binding: the
// P-CSCF gives itself as its Response-Source, which tells it from a 404 of the UE (TS 24.229 §5.2.6.4.1, §7.2.17.5).
// A binding without a Path has no P-CSCF.
func pcscfNotFound(path string, res *sip.Response) bool {
	if path == "" {
		return false
	}

	src, ok, err := res.Header.ResponseSource()

	return ok && err == nil && src.FunctionalEntity() == sip.EntityPCSCF
}

// dialogCreating reports a method whose initial request creates a dialog (RFC 3261 §12, RFC 6665 §4.1, RFC 3515 §2.4.4).
func dialogCreating(method string) bool {
	switch method {
	case "INVITE", "SUBSCRIBE", "REFER":
		return true
	}

	return false
}

// TS 24.229 §5.4.3.3
func (s *Sessions) assertAlias(res *sip.Response, sub func() cx.IMSSubscription) {
	asserted, err := res.Header.Addresses("P-Asserted-Identity")
	if err != nil || len(asserted) != 1 {
		return
	}

	if alias, ok := assertedAlias(asserted[0], sub(), s.r.cfg.HomeDomain); ok {
		res.Header.Add("P-Asserted-Identity", alias.String())
	}
}

// initialOptions record-route the initial request of a dialog; track adds the S-CSCF's own record of an INVITE
// dialog, kept on the leg of the served UE (TS 24.229 §5.4.3.2 steps 13 and 16, §5.4.3.3 steps 11 and 13).
func (s *Sessions) initialOptions(out *sip.Request, user string, track bool) proxy.Options {
	var opts proxy.Options

	switch out.Method {
	case "INVITE":
		if track {
			opts.Dialog = s.proxy.NewDialog(proxy.DialogConfig{
				Target: s.dialogTarget(out.Flow), Value: &call{icid: out.Header.ICID()}, OnEvent: s.r.dialogEvent,
			})
		}

		fallthrough
	case "SUBSCRIBE", "REFER":
		opts.RecordRoute = &proxy.RecordRoute{User: user}
	}

	return opts
}

// dialogTarget is where the requests the S-CSCF generates within a dialog go, such as the BYEs that release it, from
// the S-CSCF's address on in, the flow of the initial request: their next hop, whatever its form (RFC 3261 §8.1.2).
func (s *Sessions) dialogTarget(in sip.Flow) func(proxy.Side, *sip.Request) (proxy.Target, error) {
	return func(_ proxy.Side, req *sip.Request) (proxy.Target, error) {
		return s.nextHop(s.r.ctx, req, in)
	}
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

	if !needsDNS(out) {
		s.forwardInDialog(context.Background(), tx, out, removed, d)
		return
	}

	s.spawn(tx, func(ctx context.Context) { s.forwardInDialog(ctx, tx, out, removed, d) })
}

// forwardInDialog forwards a request within a dialog to its next hop (RFC 3261 §16.12).
func (s *Sessions) forwardInDialog(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, removed []sip.URI,
	d *proxy.Dialog,
) {
	req := tx.Request()

	to, err := s.nextHop(ctx, out, req.Flow)
	if err != nil {
		s.log.Debug("no next hop", slog.String("request", out.StartLine()), slog.Any("error", err))
		s.respond(tx, sip.NewResponse(req, 404, "No Route"))

		return
	}

	opts := proxy.Options{Dialog: d}

	switch out.Method {
	case "INVITE", "UPDATE", "SUBSCRIBE", "NOTIFY":
		opts.RecordRoute = recordRouteOf(removed)
		refreshToken(out, removed, &opts)
	}

	s.forward(tx, out, to, opts)
}

// refreshToken puts the registration token of the initial request in a target refresh request towards the AS it
// went to, and in the 1xx and 2xx responses to one from it (TS 24.229 §5.4.3.2); a request that goes elsewhere loses
// any (§5.4.3.2 step 18). The S-CSCF's Record-Route entry that faces the AS holds it: the second entry the request
// names when it goes there, the first when it comes from there.
func refreshToken(out *sip.Request, removed []sip.URI, opts *proxy.Options) {
	if len(removed) > 1 {
		if token, ok := removed[1].Params.Get(tokenParam); ok {
			withRegistrationToken(&out.Header, token)
			return
		}
	}

	withoutRegistrationToken(&out.Header)

	if len(removed) < 2 {
		return
	}

	if token, ok := removed[0].Params.Get(tokenParam); ok {
		opts.OnReply = func(r proxy.Reply) proxy.Verdict {
			if res := r.Response; res != nil && res.StatusCode > 100 && res.StatusCode < 300 {
				withRegistrationToken(&res.Header, token)
			}

			return proxy.Relay
		}
	}
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

// resolveTimeout bounds the DNS queries that locate the next hop of a request.
const resolveTimeout = 4 * time.Second

// hopURI is the URI a request goes to next: its first Route, or else its Request-URI (RFC 3261 §8.1.2, §16.12).
func hopURI(out *sip.Request) (sip.URI, error) {
	route, err := out.Header.TopRoute()

	switch {
	case err == nil:
		return route.URI, nil
	case errors.Is(err, sip.ErrMissingHeader):
		return out.URI, nil
	default:
		return sip.URI{}, err
	}
}

// needsDNS reports whether the next hop of a request is named by a domain name, which only the DNS locates.
func needsDNS(out *sip.Request) bool {
	u, err := hopURI(out)
	if err != nil {
		return false
	}

	_, literal := u.Addr()

	return !literal
}

// nextHop is where the S-CSCF sends a request: the first server its next hop resolves to (RFC 3261 §8.1.2, §16.12,
// RFC 3263 §4) that a listener of the S-CSCF shares the address family of. A next hop named by its IP address
// takes no DNS query.
func (s *Sessions) nextHop(ctx context.Context, out *sip.Request, in sip.Flow) (proxy.Target, error) {
	u, err := hopURI(out)
	if err != nil {
		return proxy.Target{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	servers, err := s.r.cfg.Resolver.Resolve(ctx, u)
	if err != nil {
		return proxy.Target{}, err
	}

	err = fmt.Errorf("no server for %s", u)

	for _, srv := range servers {
		var to proxy.Target

		if to, err = s.target(srv.Transport, srv.Addr, in); err == nil {
			return to, nil
		}
	}

	return proxy.Target{}, err
}

func (s *Sessions) target(tr sip.Transport, dest netip.AddrPort, in sip.Flow) (proxy.Target, error) {
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

	if err != nil && opts.Dialog != nil {
		s.r.calls.forget(opts.Dialog)
	}

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

// answer answers a request with a response of the S-CSCF's, through the proxy if it has begun forwarding it.
func (s *Sessions) answer(tx *transaction.ServerTransaction, res *sip.Response) {
	s.r.cfg.Records.RejectingRequest(tx.Request(), res.StatusCode, tx.ToTag())

	if err := s.proxy.Relay(tx, res); err == nil {
		return
	}

	s.send(tx, res)
}

// respond answers a request with a response of the S-CSCF's.
func (s *Sessions) respond(tx *transaction.ServerTransaction, res *sip.Response) {
	s.r.cfg.Records.RejectingRequest(tx.Request(), res.StatusCode, tx.ToTag())
	s.send(tx, res)
}

func (s *Sessions) send(tx *transaction.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		s.log.Debug("SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}
