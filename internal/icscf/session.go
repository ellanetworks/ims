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

	deadline time.Time
}

func (i *ICSCF) initial(tx *transaction.ServerTransaction, req *sip.Request) {
	out := req.Clone()
	out.Header.Del("P-Profile-Key")

	if !i.trusted(req) {
		out.Header.Del("P-Charging-Vector")
		out.Header.Del("P-Charging-Function-Addresses")
		trust.StripRequest(out)
	}

	out, removed, err := i.cfg.Proxy.Preprocess(out)
	if err != nil {
		i.respond(tx, sip.NewResponse(req, 400, "Bad Route"))
		return
	}

	switch {
	case out.Header.Has("Route") || slices.ContainsFunc(removed, func(u sip.URI) bool { return u.Params.Has("orig") }):
		i.log.Info("request routed beyond the I-CSCF", slog.String("request", req.StartLine()),
			slog.String("source", req.Flow.Remote.String()))
		i.respond(tx, sip.NewResponse(req, 403, ""))
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

func (i *ICSCF) queryLocation(ctx context.Context, s *session) {
	lia, err := i.locationInfo(ctx, s.identity)
	if err != nil {
		i.log.Info("user location query failed", slog.String("impu", s.identity), slog.Any("error", err))
		i.answer(s.tx, locationFailure(err))

		return
	}

	name := i.cfg.SCSCF.Name

	switch {
	case lia.ServerName != "":
		name = i.assigned(lia.ServerName, s.identity)
	case !i.capable(lia.Capabilities):
		i.answer(s.tx, 480)
		return
	}

	out := s.req.Clone()

	route := name.Clone()
	route.Params.Set("lr", "")
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
