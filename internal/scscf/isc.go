package scscf

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

// TS 24.229 clause 5.4.1.7
// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.3
const DefaultASTimeout = 4 * time.Second

// TS 24.229 clause 5.4.3.4
const odiPrefix = "odi-"

const registrationToken = "+g.3gpp.registration-token"

// TS 24.229 clause 5.4.3.2
const tokenParam = "rtoken"

var errNoUserData = errors.New("SAA without User-Data")

// TS 23.218 clause 5.2.3
type chain struct {
	served  sip.Address
	sescase cx.SessionCase
	ifcs    []cx.InitialFilterCriteria

	sub cx.IMSSubscription

	binding int64
	token   string

	ruri sip.URI
	regs []db.Registration
}

func (c *chain) originating() bool {
	switch c.sescase {
	case cx.SessionCaseOriginatingRegistered, cx.SessionCaseOriginatingUnregistered, cx.SessionCaseOriginatingCDIV:
		return true
	}

	return false
}

func (c *chain) servedKeys(homeDomain string) []string {
	return identityKeys(c.served.URI, homeDomain)
}

const (
	hopPending int32 = iota
	hopBack
	hopFailed
)

// TS 24.229 clause 5.4.3.4
type hop struct {
	c *chain

	next int
	tx   *transaction.ServerTransaction

	state atomic.Int32

	sent trace.SpanContext
}

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.4
func (s *Sessions) addHop(h *hop) string {
	token := strings.ToLower(rand.Text())

	s.mu.Lock()
	s.hops[token] = h
	s.mu.Unlock()

	h.tx.OnTerminated(func() {
		s.r.clock.AfterFunc(64*s.r.cfg.Layer.T1(), func() {
			s.mu.Lock()
			delete(s.hops, token)
			s.mu.Unlock()
		})
	})

	return token
}

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.3
func (s *Sessions) hopOf(removed []sip.URI) (*hop, bool) {
	for _, u := range removed {
		if token, ok := strings.CutPrefix(strings.ToLower(u.User), odiPrefix); ok {
			s.mu.Lock()
			h := s.hops[token]
			s.mu.Unlock()

			return h, true
		}
	}

	return nil, false
}

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.3
func (s *Sessions) resume(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, h *hop) {
	if !h.state.CompareAndSwap(hopPending, hopBack) && h.state.Load() != hopBack {
		s.log.WarnContext(ctx, "request back from an AS whose default handling applied", slog.String("request", out.StartLine()))
		s.answer(ctx, tx, sip.NewResponse(tx.Request(), 403, "Unknown Original Dialog Identifier"))

		return
	}

	s.proxy.Reached(h.tx, h)

	if trace.SpanContextFromContext(ctx).TraceID() != h.sent.TraceID() {
		ctx = transaction.WithLinks(ctx, h.sent)
	}

	c := h.c

	if !c.originating() && !sameTarget(out.URI, c.ruri, s.r.cfg.HomeDomain) {
		s.log.DebugContext(ctx, "request retargeted by an AS", slog.String("from", c.ruri.String()), slog.String("to", out.URI.String()))
		s.routeOnward(ctx, tx, out, c, false)

		return
	}

	s.next(ctx, tx, out, c, h.next, false)
}

// TS 24.229 clause 5.4.3.3
func sameTarget(u, saved sip.URI, homeDomain string) bool {
	canonical := func(u sip.URI) string {
		u = u.Clone()
		u.Params, u.Headers = nil, ""

		return identityKey(u)
	}

	if canonical(u) == canonical(saved) {
		return true
	}

	keys := identityKeys(saved, homeDomain)

	return slices.ContainsFunc(identityKeys(u, homeDomain), func(k string) bool { return slices.Contains(keys, k) })
}

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.3
func (s *Sessions) next(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, c *chain, i int, first bool) {
	t := trigger{req: out, sescase: c.sescase}

	for ; i < len(c.ifcs); i++ {
		if t.matches(c.ifcs[i].Trigger) {
			s.toAS(ctx, tx, out, c, i, first)
			return
		}
	}

	switch {
	case c.originating():
		s.routeOnward(ctx, tx, out, c, first)
	case c.sescase == cx.SessionCaseTerminatingUnregistered:
		s.log.DebugContext(ctx, "request to an unregistered user", slog.String("impu", c.served.URI.String()))
		s.answer(ctx, tx, sip.NewResponse(tx.Request(), 480, ""))
	default:
		s.deliver(ctx, tx, out, c, !first)
	}
}

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.3
func (s *Sessions) toAS(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, c *chain, i int, first bool) {
	as := c.ifcs[i].ApplicationServer

	var to proxy.Target

	asURI, err := sip.ParseURI(as.ServerName)
	if err == nil {
		var srv sip.Server

		if srv, err = s.r.asServer(ctx, c.served.URI, as.ServerName); err == nil {
			to, err = s.target(srv.Transport, srv.Addr, tx.Request().Flow)
		}
	}

	if err != nil {
		s.log.WarnContext(ctx, "unusable AS", slog.String("as", as.ServerName), slog.Any("error", err))
		s.asFailed(ctx, tx, out, c, i, first, nil)

		return
	}

	h := &hop{c: c, next: i + 1, tx: tx, sent: trace.SpanContextFromContext(ctx)}

	odi := sip.URI{Scheme: "sip", User: odiPrefix + s.addHop(h), Host: sip.FormatHost(to.Flow.Local.Addr()), Port: to.Flow.Local.Port()}
	if to.Flow.Transport != sip.UDP {
		odi.Params.Set("transport", strings.ToLower(string(to.Flow.Transport)))
	}

	odi.Params.Set("lr", "")

	asURI.Params.Set("lr", "")

	r := out.Clone()
	r.Header.Prepend("Route", "<"+odi.String()+">")
	r.Header.Prepend("Route", "<"+asURI.String()+">")

	r.Header.Del("P-Served-User")
	r.Header.Add("P-Served-User", servedUser(c))

	s.r.toASCharging(&r.Header)

	if c.token != "" {
		withRegistrationToken(&r.Header, c.token)
	}

	rr := rrTerminating
	if c.originating() {
		rr = rrOriginating
	}

	opts := s.initialOptions(r, rr, first && c.originating())
	opts.Timeout, opts.AnyResponse = s.r.cfg.ASTimeout, true
	opts.OnReply = s.asReply(tx, out, c, i, first, h)

	if opts.RecordRoute != nil && c.originating() && c.token != "" {
		opts.RecordRoute.Double = true
		opts.RecordRoute.DownstreamParams = sip.Params{{Name: tokenParam, Value: c.token}}
	}

	if opts.Dialog != nil && c.binding != 0 {
		s.r.calls.add(opts.Dialog, bindingKey(c.binding), proxy.Caller, c.servedKeys(s.r.cfg.HomeDomain))
	}

	s.log.DebugContext(ctx, "request sent to an AS", slog.String("request", out.StartLine()), slog.String("as", as.ServerName),
		slog.String("served", c.served.URI.String()), slog.String("case", c.sescase.String()))

	err = s.proxy.Fork(ctx, tx, [][]proxy.Branch{{{Request: r, Target: to, Options: opts, Key: h}}})

	if err != nil && opts.Dialog != nil {
		s.r.calls.forget(opts.Dialog)
	}

	if err == nil || errors.Is(err, proxy.ErrAnswered) {
		return
	}

	s.log.DebugContext(ctx, "forwarding to an AS failed", slog.String("as", as.ServerName), slog.Any("error", err))

	if h.state.CompareAndSwap(hopPending, hopFailed) {
		s.asFailed(ctx, tx, out, c, i, first, nil)
	}
}

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.3
// RFC 5502 section 6
// RFC 8498
func servedUser(c *chain) string {
	v := "<" + c.served.URI.String() + ">"

	switch c.sescase {
	case cx.SessionCaseOriginatingRegistered:
		v += ";sescase=orig;regstate=reg"
	case cx.SessionCaseOriginatingUnregistered:
		v += ";sescase=orig;regstate=unreg"
	case cx.SessionCaseOriginatingCDIV:
		v += ";orig-cdiv"
	case cx.SessionCaseTerminatingRegistered:
		v += ";sescase=term;regstate=reg"
	case cx.SessionCaseTerminatingUnregistered:
		v += ";sescase=term;regstate=unreg"
	}

	return v
}

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.3
// TS 23.218 clause 6.4.1
func (s *Sessions) asReply(tx *transaction.ServerTransaction, out *sip.Request, c *chain, i int, first bool, h *hop,
) func(context.Context, proxy.Reply) proxy.Verdict {
	return func(ctx context.Context, r proxy.Reply) proxy.Verdict {
		res := r.Response

		if res != nil && first {
			withoutRegistrationToken(&res.Header)
		}

		switch {
		case res != nil && res.IsProvisional(), r.Provisional, res != nil && res.StatusCode != 408 && res.StatusCode < 500:
			return proxy.Relay
		case !h.state.CompareAndSwap(hopPending, hopFailed):
			return proxy.Relay
		}

		if !terminates(c.ifcs[i].ApplicationServer) {
			err := s.r.cfg.Layer.Go(ctx, func(ctx context.Context) {
				ctx, done, ok := s.r.begin(ctx)
				if !ok {
					s.answer(ctx, tx, retryLater(tx.Request()))
					return
				}

				defer done()

				s.asFailed(ctx, tx, out, c, i, first, &r)
			})
			if err != nil {
				s.answer(ctx, tx, retryLater(tx.Request()))
			}

			return proxy.Hold
		}

		s.asFailed(ctx, tx, out, c, i, first, &r)

		if res != nil {
			return proxy.Relay
		}

		return proxy.Hold
	}
}

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.3
func (s *Sessions) asFailed(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, c *chain, i int, first bool,
	r *proxy.Reply,
) {
	as := c.ifcs[i].ApplicationServer

	var cause any
	if r != nil {
		cause = r.Err
	}

	if !terminates(as) {
		s.log.InfoContext(ctx, "AS failed: session continued", slog.String("as", as.ServerName), slog.String("request", out.StartLine()),
			slog.Any("error", cause))
		s.next(ctx, tx, out, c, i+1, first)

		return
	}

	s.log.InfoContext(ctx, "AS failed: request ended", slog.String("as", as.ServerName), slog.String("request", out.StartLine()),
		slog.Any("error", cause))

	switch {
	case r != nil && r.Response != nil:
	case r != nil && out.Method != "INVITE":
		s.answer(ctx, tx, sip.NewResponse(tx.Request(), 504, ""))
	case r != nil:
		s.answer(ctx, tx, sip.NewResponse(tx.Request(), 408, ""))
	default:
		s.answer(ctx, tx, sip.NewResponse(tx.Request(), 500, ""))
	}
}

func terminates(as cx.ApplicationServer) bool {
	return as.DefaultHandling != nil && *as.DefaultHandling == cx.DefaultHandlingSessionTerminated
}

// TS 24.229 clause 5.4.3.2
func withoutRegistrationToken(h *sip.Header) {
	values := h.Values("Feature-Caps")
	if !slices.ContainsFunc(values, func(v string) bool { return strings.Contains(strings.ToLower(v), registrationToken) }) {
		return
	}

	h.Del("Feature-Caps")

	for _, v := range values {
		for _, e := range sip.SplitList(v) {
			star, ps, err := sip.ParseTokenParams(e)
			if err != nil {
				h.Add("Feature-Caps", e)
				continue
			}

			ps.Del(registrationToken)

			if len(ps) > 0 {
				h.Add("Feature-Caps", star+ps.String())
			}
		}
	}
}

func withRegistrationToken(h *sip.Header, token string) {
	withoutRegistrationToken(h)

	if v, err := sip.FeatureCapsValue(sip.FeatureSet{sip.NewStringFeature(registrationToken, token)}); err == nil {
		h.Add("Feature-Caps", v)
	}
}

// TS 24.229 clause 5.4.3.2
// TS 24.229 clause 5.4.3.3
func (r *Registrar) profileOf(ctx context.Context, u sip.URI, regs []db.Registration,
) (cx.ServiceProfile, cx.IMSSubscription, bool, error) {
	now := r.clock.Now()

	for _, reg := range regs {
		if len(liveAt(reg.Bindings, now)) == 0 {
			continue
		}

		sub, err := cx.ParseUserData(reg.UserData)
		if err != nil {
			return cx.ServiceProfile{}, cx.IMSSubscription{}, false, err
		}

		p, ok := serviceProfile(sub, u, r.cfg.HomeDomain)
		if !ok {
			r.log.WarnContext(ctx, "no service profile holds the identity: no service", slog.String("impu", u.String()))
		}

		return p, sub, true, nil
	}

	set, sub, err := r.unregisteredProfile(ctx, u)
	if err != nil {
		return cx.ServiceProfile{}, cx.IMSSubscription{}, false, err
	}

	p, ok := serviceProfileOf(sub, u, set.Unregistered.Wildcard, r.cfg.HomeDomain)
	if !ok {
		r.log.WarnContext(ctx, "no service profile of the HSS holds the identity: no service", slog.String("impu", u.String()))
	}

	return p, sub, false, nil
}

// TS 29.228 annex F
func (r *Registrar) criteria(p cx.ServiceProfile, registered bool) []cx.InitialFilterCriteria {
	return slices.DeleteFunc(filterCriteria(p, registered), func(c cx.InitialFilterCriteria) bool {
		expr, bad := invalidExpression(c.Trigger)
		if bad {
			r.log.Warn("iFC left out: invalid regular expression", slog.String("as", c.ApplicationServer.ServerName),
				slog.Int("priority", int(c.Priority)), slog.String("expression", expr))
		}

		return bad
	})
}

// TS 29.228 clause B.2.1
func barredIn(p cx.ServiceProfile, u sip.URI, homeDomain string) bool {
	keys := identityKeys(u, homeDomain)

	for _, id := range p.PublicIdentities {
		if id.Barred && slices.Contains(keys, identityKeyOf(id.Identity)) {
			return true
		}
	}

	return false
}

func bindingToken(id int64) string {
	return strconv.FormatInt(id, 10)
}
