package icscf

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

type session struct {
	tx       *transaction.ServerTransaction
	req      *sip.Request
	identity string

	orig     bool
	breakout bool

	deadline time.Time
}

// TS 23.228 clause 4.6.1
// TS 24.229 clause 4.4
// TS 24.229 clause 5.3.2.1
// TS 24.229 clause 5.3.2.1A
func (i *ICSCF) initial(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	trusted := i.trusted(req)
	if !trusted && i.rejectOrig(tx, req) {
		return
	}

	out := req.Clone()
	out.Header.Del("P-Profile-Key")

	if !trusted {
		out.Header.Del("P-Charging-Vector")
		out.Header.Del("P-Charging-Function-Addresses")
		trust.StripRequest(out)
	}

	out, removed, err := i.cfg.Proxy.Preprocess(out)
	if err != nil {
		i.respond(tx, sip.NewResponse(req, 400, "Bad Route"))
		return
	}

	if !trusted && out.Header.Has("Route") {
		i.log.DebugContext(ctx, "routes of a request from outside the trust domain dropped", slog.Any("route", out.Header.Values("Route")),
			slog.String("source", req.Flow.Remote.String()))
		out.Header.Del("Route")
	}

	switch {
	case slices.ContainsFunc(removed, func(u sip.URI) bool { return u.Params.Has("orig") }):
		i.originating(ctx, tx, out)
	case out.Header.Has("Route"):
		i.forwardOnRoute(ctx, tx, out)
	case out.Method == "OPTIONS" && out.URI.User == "" && i.cfg.Proxy.IsLocal(out.URI):
		i.respond(tx, sip.NewResponse(req, 200, ""))
	default:
		i.terminating(ctx, tx, out, out.Method == "INVITE" && i.fromSCSCF(req) &&
			slices.ContainsFunc(removed, func(u sip.URI) bool { return u.Params.Has(BreakoutParam) }))
	}
}

func (i *ICSCF) fromSCSCF(req *sip.Request) bool {
	src := netip.AddrPortFrom(req.Flow.Remote.Addr().Unmap(), req.Flow.Remote.Port())

	return slices.ContainsFunc(i.cfg.SCSCF.Listeners, func(l netip.AddrPort) bool {
		return netip.AddrPortFrom(l.Addr().Unmap(), l.Port()) == src
	})
}

func (i *ICSCF) terminating(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, breakout bool) {
	if !out.URI.IsSIP() && !out.URI.IsTel() {
		i.respond(tx, sip.NewResponse(tx.Request(), 416, ""))
		return
	}

	tel, converted, err := telURI(out.URI)
	if err != nil {
		i.respond(tx, sip.NewResponse(tx.Request(), 400, "Bad Request-URI"))
		return
	}

	if converted {
		out.URI = tel
	}

	identity, err := sessionIdentity(out.URI)
	if err != nil {
		i.respond(tx, sip.NewResponse(tx.Request(), 400, "Bad Request-URI"))
		return
	}

	s := &session{tx: tx, req: out, identity: identity, breakout: breakout && out.URI.IsTel(), deadline: i.deadline()}

	if out.URI.IsSIP() && i.cfg.PSISubdomain != nil && i.cfg.PSISubdomain(out.URI.Host) {
		i.spawn(ctx, tx, func(ctx context.Context) { i.toPSISubdomain(ctx, s) })
		return
	}

	i.spawn(ctx, tx, func(ctx context.Context) { i.queryLocation(ctx, s) })
}

// TS 24.229 clause 5.3.2.1
// TS 23.228 clause 5.4.12.3
func (i *ICSCF) toPSISubdomain(ctx context.Context, s *session) {
	to, err := i.serverTarget(ctx, s.req.Flow, s.req.URI)
	if err != nil {
		i.log.WarnContext(ctx, "no AS for the PSI subdomain", slog.String("psi", s.identity), slog.Any("error", err))
		i.answer(ctx, s.tx, 480)

		return
	}

	opts := proxy.Options{OnReply: func(ctx context.Context, r proxy.Reply) proxy.Verdict { return i.sessionReply(ctx, s, r, false) }}

	if s.req.Method != "INVITE" {
		opts.Timeout = i.branchTimeout(s.deadline)
	}

	i.log.DebugContext(ctx, "request routed to the AS of its PSI subdomain", slog.String("psi", s.identity))
	i.forward(ctx, s.tx, s.req.Clone(), to, opts)
}

// TS 24.229 clause 5.3.2.1A
// TS 24.229 clause 5.7.3
// TS 23.228 clause 5.4.12.5
func (i *ICSCF) originating(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request) {
	served, err := out.Header.Addresses("P-Served-User")
	if err == nil && len(served) == 0 {
		served, err = out.Header.Addresses("P-Asserted-Identity")
	}

	if err != nil || len(served) == 0 {
		i.log.DebugContext(ctx, "originating request on behalf of no identity", slog.String("request", out.StartLine()))
		i.respond(tx, sip.NewResponse(tx.Request(), 403, ""))

		return
	}

	identity, err := sessionIdentity(served[0].URI)
	if err != nil {
		i.respond(tx, sip.NewResponse(tx.Request(), 403, ""))
		return
	}

	s := &session{tx: tx, req: out, identity: identity, orig: true, deadline: i.deadline()}
	i.spawn(ctx, tx, func(ctx context.Context) { i.queryLocation(ctx, s) })
}

// TS 24.229 clause 5.3.2.1
// TS 24.229 clause 5.3.2.1A
// TS 29.228 clause 6.1.4.1
// TS 29.229 clause 6.3.59
func (i *ICSCF) queryLocation(ctx context.Context, s *session) {
	lia, err := i.locationInfo(ctx, cx.LocationInfoRequest{
		PublicIdentity: s.identity, Originating: s.orig, Features: cx.FeatureIMSRestoration,
	})

	switch {
	case err != nil && s.breakout && userUnknown(err):
		i.toIBCF(ctx, s)
		return
	case err != nil:
		i.log.DebugContext(ctx, "user location query failed", slog.String("impu", s.identity), slog.Any("error", err))

		i.answer(ctx, s.tx, locationFailure(err))

		return
	}

	orig := s.orig && !lia.PSIDirectRouting

	switch {
	case lia.ServerName != "" && !i.ours(lia.ServerName):
		i.toServer(ctx, s, lia.ServerName, orig, lia.Features&cx.FeatureIMSRestoration != 0 && !lia.PSIDirectRouting)
		return
	case lia.ServerName != "":
	case !i.capable(lia.Capabilities):
		i.log.DebugContext(ctx, "no capable S-CSCF for the user", slog.String("impu", s.identity), slog.Any("result", lia.Result))
		i.answer(ctx, s.tx, 480)

		return
	default:
		i.log.DebugContext(ctx, "no S-CSCF assigned to the user", slog.String("impu", s.identity), slog.Any("result", lia.Result))
	}

	i.toLocal(ctx, s, orig)
}

func (i *ICSCF) toLocal(ctx context.Context, s *session, orig bool) {
	name := i.cfg.SCSCF.Name

	out := s.req.Clone()

	route := name.Clone()
	route.Params.Set("lr", "")

	if orig {
		route.Params.Set("orig", "")
	}

	out.Header.Prepend("Route", "<"+route.String()+">")

	to, ok := i.target(s.req.Flow, name)
	if !ok {
		i.log.WarnContext(ctx, "no listener for the S-CSCF", slog.String("scscf", name.String()))
		i.answer(ctx, s.tx, 480)

		return
	}

	opts := proxy.Options{OnReply: func(ctx context.Context, r proxy.Reply) proxy.Verdict { return i.sessionReply(ctx, s, r, false) }}

	if out.Method != "INVITE" {
		opts.Timeout = i.branchTimeout(s.deadline)
	}

	i.forward(ctx, s.tx, out, to, opts)
}

// TS 24.229 clause 5.3.2.1
// TS 24.229 clause 5.3.2.1A
// TS 23.228 clause 5.4.12.2
func (i *ICSCF) toServer(ctx context.Context, s *session, name string, orig, restorable bool) {
	u, err := sip.ParseURI(name)

	var to proxy.Target

	if err == nil {
		u.Params.Set("lr", "")

		if orig {
			u.Params.Set("orig", "")
		}

		to, err = i.serverTarget(ctx, s.req.Flow, u)
	}

	if err != nil {
		i.log.WarnContext(ctx, "unusable server from the HSS", slog.String("impu", s.identity), slog.String("server-name", name),
			slog.Any("error", err))

		if restorable {
			i.reassignSession(ctx, s)
			return
		}

		i.answer(ctx, s.tx, 480)

		return
	}

	out := s.req.Clone()
	out.Header.Prepend("Route", "<"+u.String()+">")

	opts := proxy.Options{OnReply: func(ctx context.Context, r proxy.Reply) proxy.Verdict {
		return i.sessionReply(ctx, s, r, restorable)
	}}

	switch {
	case out.Method != "INVITE" && restorable:
		opts.Timeout = max(i.branchTimeout(s.deadline)/2, i.t1)
	case out.Method != "INVITE":
		opts.Timeout = i.branchTimeout(s.deadline)
	case restorable:
		opts.Timeout = contactTimeout * i.t1
	}

	i.log.DebugContext(ctx, "request routed to the server the HSS names", slog.String("impu", s.identity), slog.String("server-name", name))
	i.forward(ctx, s.tx, out, to, opts)
}

// TS 23.380 clause 4.3.3
// TS 23.380 clause 4.5.3
func (i *ICSCF) reassignSession(ctx context.Context, s *session) {
	lia, err := i.locationInfo(ctx, cx.LocationInfoRequest{
		PublicIdentity: s.identity, Originating: s.orig, AuthorizationType: cx.AuthorizationRegistrationAndCapabilities,
		Features: cx.FeatureIMSRestoration,
	})
	if err != nil {
		i.log.InfoContext(ctx, "S-CSCF capabilities query failed", slog.String("impu", s.identity), slog.Any("error", err))
		i.answer(ctx, s.tx, locationFailure(err))

		return
	}

	if !i.capable(lia.Capabilities) {
		i.answer(ctx, s.tx, 480)
		return
	}

	i.log.InfoContext(ctx, "user reassigned to this S-CSCF", slog.String("impu", s.identity),
		slog.String("scscf", i.cfg.SCSCF.Name.String()))
	i.toLocal(ctx, s, s.orig)
}

// TS 24.229 clause 5.3.2.1
// TS 23.228 clause 4.6.2.0
func (i *ICSCF) toIBCF(ctx context.Context, s *session) {
	to, ok := i.ibcf(s.req.Flow)
	if !ok {
		i.log.DebugContext(ctx, "number not in the home network and no IBCF", slog.String("request-uri", s.req.URI.String()))
		i.answer(ctx, s.tx, 404)

		return
	}

	route := sip.URI{Scheme: "sip", Host: sip.FormatHost(to.Flow.Remote.Addr()), Port: to.Flow.Remote.Port()}
	if to.Flow.Transport != sip.UDP {
		route.Params.Set("transport", strings.ToLower(string(to.Flow.Transport)))
	}

	route.Params.Set("lr", "")

	out := s.req.Clone()
	out.Header.Prepend("Route", "<"+route.String()+">")

	opts := proxy.Options{OnReply: func(ctx context.Context, r proxy.Reply) proxy.Verdict { return i.sessionReply(ctx, s, r, false) }}

	i.log.DebugContext(ctx, "number not in the home network routed to the IBCF", slog.String("request-uri", s.req.URI.String()))
	i.forward(ctx, s.tx, out, to, opts)
}

func (i *ICSCF) ibcf(in sip.Flow) (proxy.Target, bool) {
	local := in.Local.Addr().Unmap()

	remote, ok := pick(i.cfg.IBCF, func(a netip.Addr) bool { return a.Unmap() == local })
	if !ok {
		remote, ok = pick(i.cfg.IBCF, func(a netip.Addr) bool { return a.Unmap().Is4() == local.Is4() })
	}

	if !ok {
		return proxy.Target{}, false
	}

	tr := sip.UDP
	if in.Transport == sip.TCP {
		tr = sip.TCP
	}

	return proxy.Target{Flow: sip.Flow{Transport: tr, Local: netip.AddrPortFrom(in.Local.Addr(), i.cfg.Port), Remote: remote}}, true
}

func (i *ICSCF) sessionReply(ctx context.Context, s *session, r proxy.Reply, restorable bool) proxy.Verdict {
	res := r.Response

	switch {
	case unresponsive(r, s.req.Method == "INVITE") && restorable:
		i.reassignSession(ctx, s)
		return proxy.Hold
	case unresponsive(r, s.req.Method == "INVITE") && (res == nil || res.StatusCode == 500):
		i.answer(ctx, s.tx, 480)
		return proxy.Hold
	}

	if res != nil {
		i.outgoing(s.req, res)
	}

	return proxy.Relay
}

func (i *ICSCF) rejectOrig(tx *transaction.ServerTransaction, req *sip.Request) bool {
	routes, _ := req.Header.Routes()

	if !slices.ContainsFunc(routes, func(r sip.Address) bool { return r.URI.Params.Has("orig") }) {
		return false
	}

	i.log.Warn("originating request from outside the trust domain", slog.String("source", req.Flow.Remote.String()))
	i.respond(tx, sip.NewResponse(req, 403, ""))

	return true
}

func (i *ICSCF) subsequent(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	if !i.trusted(req) {
		i.log.DebugContext(ctx, "request within a dialog from outside the trust domain", slog.String("request", req.StartLine()),
			slog.String("source", req.Flow.Remote.String()))
		i.respond(tx, sip.NewResponse(req, 403, ""))

		return
	}

	out := req.Clone()
	out.Header.Del("P-Profile-Key")

	out, _, err := i.cfg.Proxy.Preprocess(out)
	if err != nil {
		i.respond(tx, sip.NewResponse(req, 400, "Bad Route"))
		return
	}

	i.forwardOnRoute(ctx, tx, out)
}

func (i *ICSCF) forwardOnRoute(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request) {
	route, err := out.Header.TopRoute()
	if err != nil {
		i.respond(tx, sip.NewResponse(tx.Request(), 480, ""))
		return
	}

	to, err := i.scscfTarget(ctx, out.Flow, route.URI)
	if err != nil {
		i.log.DebugContext(ctx, "no next hop for the route", slog.String("route", route.URI.String()), slog.Any("error", err))
		i.respond(tx, sip.NewResponse(tx.Request(), 480, ""))

		return
	}

	i.forward(ctx, tx, out, to, proxy.Options{OnReply: func(ctx context.Context, r proxy.Reply) proxy.Verdict {
		if r.Response != nil {
			i.outgoing(tx.Request(), r.Response)
		}

		return proxy.Relay
	}})
}
