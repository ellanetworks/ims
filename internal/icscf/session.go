package icscf

import (
	"context"
	"errors"
	"log/slog"

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
		originating = route.URI.Params.Has("orig")
	}

	if originating && !trusted {
		i.log.Info("originating request from outside the trust domain", slog.String("source", req.Flow.Remote.String()))
		i.respond(tx, sip.NewResponse(req, 403, ""))

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

	identity, err := publicIdentity(out.URI)
	if err != nil {
		i.respond(tx, sip.NewResponse(tx.Request(), 400, "Bad Request-URI"))
		return
	}

	s := &session{tx: tx, req: out, identity: identity}
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

	s := &session{tx: tx, req: out, identity: identity, originating: true}
	i.spawn(tx, func(ctx context.Context) { i.queryLocation(ctx, s) })
}

func servedUser(h sip.Header) (string, error) {
	for _, name := range []string{"P-Served-User", "P-Asserted-Identity"} {
		users, err := h.Addresses(name)
		if err != nil || len(users) == 0 {
			continue
		}

		return publicIdentity(users[0].URI)
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

func (i *ICSCF) forwardSession(s *session, scscf *SCSCF, reselected bool) {
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

		return
	}

	opts := proxy.Options{OnReply: func(r proxy.Reply) proxy.Verdict { return i.sessionReply(s, r) }}

	// A non-INVITE transaction times out upstream when it does here: the
	// S-CSCF gets less time so that another can be selected.
	if out.Method != "INVITE" && !s.assigned && i.untried(s.tried) {
		opts.Timeout = i.cfg.SCSCFTimeout
	}

	i.forward(s.tx, out, to, opts)
}

func (i *ICSCF) sessionReply(s *session, r proxy.Reply) proxy.Verdict {
	res := r.Response

	switch {
	case res != nil && res.StatusCode == 305 && s.req.Method == "INVITE" && !s.redirect:
		s.redirect = true
		i.useProxy(s, res)

		return proxy.Hold
	case errors.Is(r.Err, transaction.ErrTimeout) && !s.assigned:
		if scscf := i.choose(s.caps, s.tried); scscf != nil {
			i.log.Info("reselecting the S-CSCF", slog.String("impu", s.identity), slog.String("scscf", scscf.Name.String()))
			i.forwardSession(s, scscf, true)

			return proxy.Hold
		}
	}

	if res != nil {
		i.outgoing(s.req, res)
	}

	return proxy.Relay
}

// useProxy forwards an initial INVITE to the proxy named in the Contact of a
// 305 (Use Proxy) from the S-CSCF (TS 24.229 §5.3.2.1).
func (i *ICSCF) useProxy(s *session, res *sip.Response) {
	contacts, err := res.Header.Contacts()
	if err != nil || len(contacts) == 0 || contacts[0].Star {
		i.answer(s.tx, 480)
		return
	}

	scscf := i.lookup(contacts[0].URI)
	if scscf == nil {
		i.log.Info("305 to an unknown proxy", slog.String("contact", contacts[0].URI.String()))
		i.answer(s.tx, 480)

		return
	}

	out := s.req.Clone()

	route := contacts[0].URI.Clone()
	route.Params.Set("lr", "")
	out.Header.Prepend("Route", "<"+route.String()+">")

	to, ok := i.target(scscf, s.req.Flow, contacts[0].URI)
	if !ok {
		i.answer(s.tx, 480)
		return
	}

	i.forward(s.tx, out, to, proxy.Options{OnReply: func(r proxy.Reply) proxy.Verdict {
		if r.Response != nil {
			i.outgoing(s.req, r.Response)
		}

		return proxy.Relay
	}})
}

// subsequent handles a request inside a dialog (TS 24.229 §5.3.5). The
// I-CSCF does not Record-Route, so it only sees one that was routed to it.
func (i *ICSCF) subsequent(tx *transaction.ServerTransaction, req *sip.Request) {
	out := req.Clone()
	out.Header.Del("P-Profile-Key")

	if !i.trusted(req) {
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
