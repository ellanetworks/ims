package icscf

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
)

type session struct {
	tx          *transaction.ServerTransaction
	req         *sip.Request
	identity    string
	originating bool

	// deadline is when a non-INVITE request is answered at the latest.
	deadline time.Time

	caps     *cx.ServerCapabilities
	assigned bool
	tried    []*SCSCF
	redirect bool
}

// initial handles an initial request for a dialog or a standalone transaction
// (TS 24.229 §5.3.2.1).
func (i *ICSCF) initial(tx *transaction.ServerTransaction, req *sip.Request) {
	trusted := i.trusted(req)
	originating := false

	if route, err := req.Header.TopRoute(); err == nil {
		originating = route.URI.Params.Has("orig") && i.cfg.Proxy.IsLocal(route.URI)
	}

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

	out, _, err := i.cfg.Proxy.Preprocess(out)
	if err != nil {
		i.respond(tx, sip.NewResponse(req, 400, "Bad Route"))
		return
	}

	switch {
	case originating:
		i.originating(tx, out)
	case out.Header.Has("Route"):
		i.forwardOnRoute(tx, out)
	case out.Method == "OPTIONS" && out.URI.User == "" && i.cfg.Proxy.IsLocal(out.URI):
		i.respond(tx, sip.NewResponse(req, 200, ""))
	default:
		i.terminating(tx, out)
	}
}

// rejectOrig answers 403 to a request from outside the trust domain with
// "orig" in any Route (§5.3.2.1A): below the top one, it would reach the
// S-CSCF from the I-CSCF, which the S-CSCF trusts.
func (i *ICSCF) rejectOrig(tx *transaction.ServerTransaction, req *sip.Request) bool {
	routes, _ := req.Header.Routes()

	if !slices.ContainsFunc(routes, func(r sip.Address) bool { return r.URI.Params.Has("orig") }) {
		return false
	}

	i.log.Info("originating request from outside the trust domain", slog.String("source", req.Flow.Remote.String()))
	i.respond(tx, sip.NewResponse(req, 403, ""))

	return true
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

// originating handles a request with "orig" in its top Route: the S-CSCF of
// the calling user is located (TS 24.229 §5.3.2.1A).
func (i *ICSCF) originating(tx *transaction.ServerTransaction, out *sip.Request) {
	identity, err := servedUser(out.Header)
	if err != nil {
		i.respond(tx, sip.NewResponse(tx.Request(), 403, "No Served User"))
		return
	}

	s := &session{tx: tx, req: out, identity: identity, originating: true, deadline: i.deadline()}
	i.spawn(tx, func(ctx context.Context) { i.queryLocation(ctx, s) })
}

func servedUser(h sip.Header) (string, error) {
	for _, name := range []string{"P-Served-User", "P-Asserted-Identity"} {
		users, err := h.Addresses(name)
		if err != nil || len(users) == 0 {
			continue
		}

		return sessionIdentity(users[0].URI)
	}

	return "", errNoIdentity
}

func (i *ICSCF) queryLocation(ctx context.Context, s *session) {
	lia, err := i.locationInfo(ctx, s.identity, s.originating)
	if err != nil {
		i.log.Info("user location query failed", slog.String("impu", s.identity),
			slog.Bool("originating", s.originating), slog.Any("error", err))
		i.answer(s.tx, locationFailure(err, s.originating))

		return
	}

	var scscf *SCSCF

	if lia.ServerName != "" {
		s.assigned = true

		if scscf = i.lookupName(lia.ServerName); scscf == nil {
			i.log.Warn("the HSS located the user on an unknown server", slog.String("impu", s.identity),
				slog.String("server-name", lia.ServerName))
			i.answer(s.tx, 480)

			return
		}
	} else if scscf = i.choose(lia.Capabilities, nil); scscf == nil {
		i.answer(s.tx, 480)
		return
	}

	s.caps = lia.Capabilities
	i.forwardSession(s, scscf, false)
}

// forwardSession forwards the request to an S-CSCF and reports whether it
// did.
func (i *ICSCF) forwardSession(s *session, scscf *SCSCF, reselected bool) bool {
	s.tried = append(s.tried, scscf)

	out := s.req.Clone()

	if reselected && out.URI.IsSIP() {
		out.URI.Params.Set("scscf-reselection", "")
	}

	route := scscf.Name.Clone()
	route.Params.Set("lr", "")

	if s.originating {
		route.Params.Set("orig", "")
	}

	out.Header.Prepend("Route", "<"+route.String()+">")

	to, ok := i.target(scscf, s.req.Flow, scscf.Name)
	if !ok {
		i.log.Warn("no listener for the S-CSCF", slog.String("scscf", scscf.Name.String()))
		i.answer(s.tx, 480)

		return false
	}

	opts := proxy.Options{OnReply: func(r proxy.Reply) proxy.Verdict { return i.sessionReply(s, r) }}

	// A non-INVITE transaction times out upstream when it does here: the
	// S-CSCF gets less time so that another can be selected, or so that
	// the request is answered before then.
	if out.Method != "INVITE" {
		opts.Timeout = i.branchTimeout(s.deadline, i.replaceableSession(s))
	}

	return i.forward(s.tx, out, to, opts)
}

// replaceableSession reports whether another S-CSCF can be selected for the
// request (TS 24.229 §5.3.2.2). An assigned S-CSCF is not replaced: the
// I-CSCF does not support S-CSCF restoration.
func (i *ICSCF) replaceableSession(s *session) bool {
	return !s.assigned && i.choose(s.caps, s.tried) != nil
}

// sessionReply holds a response only when the request goes on to another
// S-CSCF. When it cannot, after a CANCEL for one (RFC 3261 §16.10), the
// response is relayed.
func (i *ICSCF) sessionReply(s *session, r proxy.Reply) proxy.Verdict {
	res := r.Response
	invite := s.req.Method == "INVITE"
	failed := unresponsive(r, invite)

	switch {
	case res != nil && res.StatusCode == 305 && invite && !s.redirect:
		s.redirect = true

		if i.useProxy(s, res) {
			return proxy.Hold
		}
	case failed && i.replaceableSession(s) && (invite || i.timeLeft(s.deadline)):
		scscf := i.choose(s.caps, s.tried)
		i.log.Info("reselecting the S-CSCF", slog.String("impu", s.identity), slog.String("scscf", scscf.Name.String()))

		if i.forwardSession(s, scscf, true) {
			return proxy.Hold
		}
	}

	// An S-CSCF that cannot be reached makes the user unavailable, but an
	// INVITE keeps the 408 or 487 the proxy generated.
	if failed && (res == nil || res.StatusCode == 500) {
		i.answer(s.tx, 480)
		return proxy.Hold
	}

	if res != nil {
		i.outgoing(s.req, res)
	}

	return proxy.Relay
}

// useProxy forwards an initial INVITE to the proxy named in the Contact of a
// 305 (Use Proxy) from the S-CSCF (TS 24.229 §5.3.2.1), and reports whether it
// did.
func (i *ICSCF) useProxy(s *session, res *sip.Response) bool {
	contacts, err := res.Header.Contacts()
	if err != nil || len(contacts) == 0 || contacts[0].Star {
		i.answer(s.tx, 480)
		return false
	}

	scscf := i.lookup(contacts[0].URI)
	if scscf == nil {
		i.log.Info("305 to an unknown proxy", slog.String("contact", contacts[0].URI.String()))
		i.answer(s.tx, 480)

		return false
	}

	out := s.req.Clone()

	route := contacts[0].URI.Clone()
	route.Params.Set("lr", "")

	if s.originating {
		route.Params.Set("orig", "")
	}

	out.Header.Prepend("Route", "<"+route.String()+">")

	to, ok := i.target(scscf, s.req.Flow, contacts[0].URI)
	if !ok {
		i.answer(s.tx, 480)
		return false
	}

	return i.forward(s.tx, out, to, proxy.Options{OnReply: func(r proxy.Reply) proxy.Verdict {
		if r.Response != nil {
			i.outgoing(s.req, r.Response)
		}

		return proxy.Relay
	}})
}

// subsequent handles a request inside a dialog (TS 24.229 §5.3.5). The
// I-CSCF does not Record-Route, so it only sees one that was routed to it.
func (i *ICSCF) subsequent(tx *transaction.ServerTransaction, req *sip.Request) {
	trusted := i.trusted(req)
	if !trusted && i.rejectOrig(tx, req) {
		return
	}

	out := req.Clone()
	out.Header.Del("P-Profile-Key")

	if !trusted {
		out.Header.Del("P-Charging-Vector")
		trust.StripRequest(out)
	}

	out, _, err := i.cfg.Proxy.Preprocess(out)
	if err != nil {
		i.respond(tx, sip.NewResponse(req, 400, "Bad Route"))
		return
	}

	i.forwardOnRoute(tx, out)
}

// forwardOnRoute forwards a request on its top Route, which must name a
// known S-CSCF: there is no other next hop.
func (i *ICSCF) forwardOnRoute(tx *transaction.ServerTransaction, out *sip.Request) {
	route, err := out.Header.TopRoute()
	if err != nil {
		i.respond(tx, sip.NewResponse(tx.Request(), 480, ""))
		return
	}

	scscf := i.lookup(route.URI)
	if scscf == nil {
		i.respond(tx, sip.NewResponse(tx.Request(), 480, ""))
		return
	}

	to, ok := i.target(scscf, out.Flow, route.URI)
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
