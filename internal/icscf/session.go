package icscf

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

type session struct {
	tx       *transaction.ServerTransaction
	req      *sip.Request
	identity string

	// orig marks a request an AS sends on behalf of the served user identity (TS 24.229 §5.3.2.1A).
	orig bool

	deadline time.Time
}

// initial serves an initial request. The I-CSCF is the contact point of the network, which takes requests from
// outside the trust domain (TS 23.228 §4.6.1, TS 24.229 §5.3.2.1): those are only ever terminating, without the header
// fields that the trust domain asserts and any Route past the I-CSCF; with "orig", they are refused
// (TS 24.229 §4.4, §5.3.2.1, §5.3.2.1A).
func (i *ICSCF) initial(tx *transaction.ServerTransaction, req *sip.Request) {
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
		i.log.Debug("routes of a request from outside the trust domain dropped", slog.Any("route", out.Header.Values("Route")),
			slog.String("source", req.Flow.Remote.String()))
		out.Header.Del("Route")
	}

	switch {
	case slices.ContainsFunc(removed, func(u sip.URI) bool { return u.Params.Has("orig") }):
		i.originating(tx, out)
	case out.Header.Has("Route"):
		i.forwardOnRoute(tx, out)
	case out.Method == "OPTIONS" && out.URI.User == "" && i.cfg.Proxy.IsLocal(out.URI):
		i.respond(tx, sip.NewResponse(req, 200, ""))
	default:
		i.terminating(tx, out)
	}
}

func (i *ICSCF) terminating(tx *transaction.ServerTransaction, out *sip.Request) {
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

	s := &session{tx: tx, req: out, identity: identity, deadline: i.deadline()}
	i.spawn(tx, func(ctx context.Context) { i.queryLocation(ctx, s) })
}

// originating serves a request that an AS of the trust domain sends on behalf of a user or a PSI to the I-CSCF, with
// "orig", as an AS that does not know the S-CSCF of the served user does: to that S-CSCF, or to the AS hosting the PSI,
// as the HSS says. The served user is in P-Served-User, or else P-Asserted-Identity (TS 24.229 §5.3.2.1A, §5.7.3;
// TS 23.228 §5.4.12.5 c).
func (i *ICSCF) originating(tx *transaction.ServerTransaction, out *sip.Request) {
	served, err := out.Header.Addresses("P-Served-User")
	if err == nil && len(served) == 0 {
		served, err = out.Header.Addresses("P-Asserted-Identity")
	}

	if err != nil || len(served) == 0 {
		i.log.Debug("originating request on behalf of no identity", slog.String("request", out.StartLine()))
		i.respond(tx, sip.NewResponse(tx.Request(), 403, ""))

		return
	}

	identity, err := sessionIdentity(served[0].URI)
	if err != nil {
		i.respond(tx, sip.NewResponse(tx.Request(), 403, ""))
		return
	}

	s := &session{tx: tx, req: out, identity: identity, orig: true, deadline: i.deadline()}
	i.spawn(tx, func(ctx context.Context) { i.queryLocation(ctx, s) })
}

// queryLocation routes a request where the HSS says: to the server it names, ours or another, an AS hosting a PSI
// or an S-CSCF, whether or not it sets the PSI direct routing flag, which is optional; or to our S-CSCF, capable of
// what the HSS requires (TS 24.229 §5.3.2.1, §5.3.2.1A; TS 29.228 §6.1.4.1 step 2, TS 29.229 §6.3.59). An originating
// request has the "orig" parameter in the Route to an S-CSCF.
func (i *ICSCF) queryLocation(ctx context.Context, s *session) {
	lia, err := i.locationInfo(ctx, s.identity, s.orig)
	if err != nil {
		i.log.Debug("user location query failed", slog.String("impu", s.identity), slog.Any("error", err))
		i.answer(s.tx, locationFailure(err))

		return
	}

	name := i.cfg.SCSCF.Name

	switch {
	case lia.ServerName != "" && !i.ours(lia.ServerName):
		// With Originating-Request, the HSS names an S-CSCF, never an AS (TS 29.228 §6.1.4.1 step 2).
		i.toServer(ctx, s, lia.ServerName, s.orig && !lia.PSIDirectRouting)
		return
	case lia.ServerName != "":
		name, _ = sip.ParseURI(lia.ServerName)
	case !i.capable(lia.Capabilities):
		i.log.Debug("no capable S-CSCF for the user", slog.String("impu", s.identity), slog.Any("result", lia.Result))
		i.answer(s.tx, 480)

		return
	default:
		i.log.Debug("no S-CSCF assigned to the user", slog.String("impu", s.identity), slog.Any("result", lia.Result))
	}

	out := s.req.Clone()

	route := name.Clone()
	route.Params.Set("lr", "")

	if s.orig && !lia.PSIDirectRouting {
		route.Params.Set("orig", "")
	}

	out.Header.Prepend("Route", "<"+route.String()+">")

	to, ok := i.target(s.req.Flow, name)
	if !ok {
		i.log.Warn("no listener for the S-CSCF", slog.String("scscf", name.String()))
		i.answer(s.tx, 480)

		return
	}

	opts := proxy.Options{OnReply: func(r proxy.Reply) proxy.Verdict { return i.sessionReply(s, r) }}

	if out.Method != "INVITE" {
		opts.Timeout = i.branchTimeout(s.deadline)
	}

	i.forward(s.tx, out, to, opts)
}

// toServer forwards a request to a server the HSS names that is not our S-CSCF: the AS hosting the PSI, for direct
// routing, or another S-CSCF, with "orig" for an originating request (TS 24.229 §5.3.2.1, §5.3.2.1A; TS 23.228
// §5.4.12.2 b).
func (i *ICSCF) toServer(ctx context.Context, s *session, name string, orig bool) {
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
		i.log.Warn("unusable server from the HSS", slog.String("impu", s.identity), slog.String("server-name", name),
			slog.Any("error", err))
		i.answer(s.tx, 480)

		return
	}

	out := s.req.Clone()
	out.Header.Prepend("Route", "<"+u.String()+">")

	opts := proxy.Options{OnReply: func(r proxy.Reply) proxy.Verdict { return i.sessionReply(s, r) }}

	if out.Method != "INVITE" {
		opts.Timeout = i.branchTimeout(s.deadline)
	}

	i.log.Debug("request routed to the server the HSS names", slog.String("impu", s.identity), slog.String("server-name", name))
	i.forward(s.tx, out, to, opts)
}

func (i *ICSCF) sessionReply(s *session, r proxy.Reply) proxy.Verdict {
	res := r.Response

	if unresponsive(r, s.req.Method == "INVITE") && (res == nil || res.StatusCode == 500) {
		i.answer(s.tx, 480)
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

// subsequent forwards a request within a dialog to the S-CSCF. The I-CSCF does not record-route: the dialogs of the
// S-CSCF do not go through it, and one from outside the trust domain would reach the S-CSCF, which takes none from
// there, as if from the trust domain.
func (i *ICSCF) subsequent(tx *transaction.ServerTransaction, req *sip.Request) {
	if !i.trusted(req) {
		i.log.Debug("request within a dialog from outside the trust domain", slog.String("request", req.StartLine()),
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

	i.forwardOnRoute(tx, out)
}

func (i *ICSCF) forwardOnRoute(tx *transaction.ServerTransaction, out *sip.Request) {
	route, err := out.Header.TopRoute()
	if err != nil || !i.cfg.SCSCF.Name.Equivalent(route.URI) {
		i.respond(tx, sip.NewResponse(tx.Request(), 480, ""))
		return
	}

	to, ok := i.target(out.Flow, route.URI)
	if !ok {
		i.respond(tx, sip.NewResponse(tx.Request(), 480, ""))
		return
	}

	i.forward(tx, out, to, proxy.Options{OnReply: func(r proxy.Reply) proxy.Verdict {
		if r.Response != nil {
			i.outgoing(tx.Request(), r.Response)
		}

		return proxy.Relay
	}})
}
