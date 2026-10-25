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
)

// DefaultASTimeout is how long the S-CSCF waits for an AS to respond before it applies the default handling of the
// iFC (TS 24.229 §5.4.1.7, §5.4.3.2, §5.4.3.3).
const DefaultASTimeout = 4 * time.Second

// odiPrefix starts the user part of the S-CSCF URI that carries an original dialog identifier (TS 24.229 §5.4.3.4).
const odiPrefix = "odi-"

const registrationToken = "+g.3gpp.registration-token"

// tokenParam is the parameter of the S-CSCF's Record-Route entry towards an AS that holds the registration token of an
// originating dialog, for the target refresh requests and responses of the dialog (TS 24.229 §5.4.3.2).
const tokenParam = "rtoken"

var errNoUserData = errors.New("SAA without User-Data")

// chain is the evaluation of the iFCs of the served user for one request, the iFCs checked in priority order as the
// request comes back from each AS invoked (TS 23.218 §5.2.3).
type chain struct {
	served  sip.Address
	sescase cx.SessionCase
	ifcs    []cx.InitialFilterCriteria

	// sub is the subscription of the served user, for the aliases of its identities.
	sub cx.IMSSubscription

	// binding is the binding the UE sent the request with, originating; token identifies it to the ASs
	// (TS 24.229 §5.4.3.2 step 5h).
	binding int64
	token   string

	// ruri is the Request-URI the terminating chain began with (TS 24.229 §5.4.3.3 step 1), regs the registrations of
	// the served user.
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

// servedKeys are the identity keys of the served user, in each of its forms.
func (c *chain) servedKeys(homeDomain string) []string {
	return identityKeys(c.served.URI, homeDomain)
}

const (
	hopPending int32 = iota
	hopBack
	hopFailed
)

// hop is an AS a chain sent the request to, which the original dialog identifier the request comes back with names
// (TS 24.229 §5.4.3.4).
type hop struct {
	c *chain

	// next is the index of the iFC after the AS's, tx the transaction whose request went to the AS.
	next int
	tx   *transaction.ServerTransaction

	// state is hopBack once a request came back from the AS, and hopFailed once the default handling of its iFC
	// applied: the chain went on without the AS, or ended.
	state atomic.Int32
}

// addHop keeps the hop while its transaction lasts, and 64*T1 after: an AS may send more requests with the same
// original dialog identifier than the one it got, which go on with the iFCs (TS 24.229 §5.4.3.2 NOTE 8, §5.4.3.4
// NOTE), such as a B2BUA that answers the request and sends it on after.
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

// hopOf is the hop the original dialog identifier of a request names, if the request has one (TS 24.229 §5.4.3.2
// step 3, §5.4.3.3 step 1).
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

// resume goes on with the iFCs of a request an AS sent back (TS 24.229 §5.4.3.2 steps 3 to 5, §5.4.3.3 steps 1 to 4).
// A terminating request the AS retargeted leaves the chain for its new target (§5.4.3.3 step 3 a). The chain of an AS
// whose default handling applied went on without it: what the AS sends back late is refused, rather than run the rest
// of the chain twice.
func (s *Sessions) resume(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, h *hop) {
	if !h.state.CompareAndSwap(hopPending, hopBack) && h.state.Load() != hopBack {
		s.log.Warn("request back from an AS whose default handling applied", slog.String("request", out.StartLine()))
		s.answer(tx, sip.NewResponse(tx.Request(), 403, "Unknown Original Dialog Identifier"))

		return
	}

	s.proxy.Reached(h.tx, h)

	c := h.c

	if !c.originating() && !sameTarget(out.URI, c.ruri, s.r.cfg.HomeDomain) {
		s.log.Debug("request retargeted by an AS", slog.String("from", c.ruri.String()), slog.String("to", out.URI.String()))
		s.toICSCF(tx, out, c, false)

		return
	}

	s.next(ctx, tx, out, c, h.next, false)
}

// sameTarget reports whether a Request-URI matches the saved one: equal in canonical form, without parameters, or an
// alias of it (TS 24.229 §5.4.3.3 step 3).
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

// next sends the request to the AS of the first iFC from i on that it matches, or on to its destination when none
// does (TS 24.229 §5.4.3.2 step 5, §5.4.3.3 step 4). first marks the request of the served UE.
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
		s.toICSCF(tx, out, c, first)
	case c.sescase == cx.SessionCaseTerminatingUnregistered:
		// TS 24.229 §5.4.3.3: no more AS to contact.
		s.log.Debug("request to an unregistered user", slog.String("impu", c.served.URI.String()))
		s.answer(tx, sip.NewResponse(tx.Request(), 480, ""))
	default:
		s.deliver(ctx, tx, out, c, !first)
	}
}

// toAS sends the request to the AS of iFC i, with the S-CSCF's own URI and an original dialog identifier in the
// Route after it (TS 24.229 §5.4.3.2 step 5, §5.4.3.3 step 4).
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
		s.log.Warn("unusable AS", slog.String("as", as.ServerName), slog.Any("error", err))
		s.asFailed(ctx, tx, out, c, i, first, nil)

		return
	}

	h := &hop{c: c, next: i + 1, tx: tx}

	odi := sip.URI{Scheme: "sip", User: odiPrefix + s.addHop(h), Host: sip.FormatHost(to.Flow.Local.Addr()), Port: to.Flow.Local.Port()}
	if to.Flow.Transport != sip.UDP {
		odi.Params.Set("transport", strings.ToLower(string(to.Flow.Transport)))
	}

	odi.Params.Set("lr", "")

	// The AS routes loosely (TS 24.229 §5.7.4).
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

	// The Record-Route entry that faces the AS holds the token, for the target refresh requests of the dialog
	// (TS 24.229 §5.4.3.2).
	if opts.RecordRoute != nil && c.originating() && c.token != "" {
		opts.RecordRoute.Double = true
		opts.RecordRoute.DownstreamParams = sip.Params{{Name: tokenParam, Value: c.token}}
	}

	if opts.Dialog != nil && c.binding != 0 {
		s.r.calls.add(opts.Dialog, bindingKey(c.binding), proxy.Caller, c.servedKeys(s.r.cfg.HomeDomain))
	}

	s.log.Debug("request sent to an AS", slog.String("request", out.StartLine()), slog.String("as", as.ServerName),
		slog.String("served", c.served.URI.String()), slog.String("case", c.sescase.String()))

	err = s.proxy.Fork(tx, [][]proxy.Branch{{{Request: r, Target: to, Options: opts, Key: h}}})

	if err != nil && opts.Dialog != nil {
		s.r.calls.forget(opts.Dialog)
	}

	if err == nil || errors.Is(err, proxy.ErrAnswered) {
		return
	}

	s.log.Debug("forwarding to an AS failed", slog.String("as", as.ServerName), slog.Any("error", err))

	if h.state.CompareAndSwap(hopPending, hopFailed) {
		s.asFailed(ctx, tx, out, c, i, first, nil)
	}
}

// servedUser is the P-Served-User of a request to an AS: the served user, with the session case and the registration
// state (TS 24.229 §5.4.3.2 step 5b, §5.4.3.3 step 4c; RFC 5502 §6, RFC 8498).
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

// asReply relays the responses of an AS, but for those that show it failed: none in time, or a 408 or a 5xx before
// any provisional response or the request coming back (TS 24.229 §5.4.3.2, §5.4.3.3; TS 23.218 §6.4.1). A response
// that goes back to the served UE or the I-CSCF rather than to an AS loses the registration token (TS 24.229
// §5.4.3.3).
func (s *Sessions) asReply(tx *transaction.ServerTransaction, out *sip.Request, c *chain, i int, first bool, h *hop,
) func(proxy.Reply) proxy.Verdict {
	return func(r proxy.Reply) proxy.Verdict {
		res := r.Response

		if res != nil && first {
			withoutRegistrationToken(&res.Header)
		}

		switch {
		case res != nil && res.IsProvisional(), r.Provisional, res != nil && res.StatusCode != 408 && res.StatusCode < 500:
			return proxy.Relay
		case !h.state.CompareAndSwap(hopPending, hopFailed):
			// The request came back from the AS: it failed later on.
			return proxy.Relay
		}

		if !terminates(c.ifcs[i].ApplicationServer) {
			// Going on may resolve the next AS: not on the goroutine of the response.
			err := s.r.cfg.Layer.Go(func(ctx context.Context) {
				ctx, done, ok := s.r.begin(ctx)
				if !ok {
					s.answer(tx, retryLater(tx.Request()))
					return
				}

				defer done()

				s.asFailed(ctx, tx, out, c, i, first, &r)
			})
			if err != nil {
				s.answer(tx, retryLater(tx.Request()))
			}

			return proxy.Hold
		}

		s.asFailed(context.Background(), tx, out, c, i, first, &r)

		if res != nil {
			return proxy.Relay
		}

		return proxy.Hold
	}
}

// asFailed applies the default handling of iFC i, its AS unusable or failed with r: SESSION_CONTINUED goes on with the
// next iFC; SESSION_TERMINATED ends the request, with the AS's response if it sent one (TS 24.229 §5.4.3.2,
// §5.4.3.3).
func (s *Sessions) asFailed(ctx context.Context, tx *transaction.ServerTransaction, out *sip.Request, c *chain, i int, first bool,
	r *proxy.Reply,
) {
	as := c.ifcs[i].ApplicationServer

	var cause any
	if r != nil {
		cause = r.Err
	}

	if !terminates(as) {
		s.log.Info("AS failed: session continued", slog.String("as", as.ServerName), slog.String("request", out.StartLine()),
			slog.Any("error", cause))
		s.next(ctx, tx, out, c, i+1, first)

		return
	}

	s.log.Info("AS failed: request ended", slog.String("as", as.ServerName), slog.String("request", out.StartLine()),
		slog.Any("error", cause))

	switch {
	case r != nil && r.Response != nil:
		// The response goes up as the proxy relays it.
	case r != nil && out.Method != "INVITE":
		// No 408 to a non-INVITE request (RFC 4320 §4.2).
		s.answer(tx, sip.NewResponse(tx.Request(), 504, ""))
	case r != nil:
		s.answer(tx, sip.NewResponse(tx.Request(), 408, ""))
	default:
		s.answer(tx, sip.NewResponse(tx.Request(), 500, ""))
	}
}

func terminates(as cx.ApplicationServer) bool {
	return as.DefaultHandling != nil && *as.DefaultHandling == cx.DefaultHandlingSessionTerminated
}

// withoutRegistrationToken removes the "+g.3gpp.registration-token" of the Feature-Caps values (TS 24.229 §5.4.3.2
// step 18). A value it cannot parse stays as it is.
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

// withRegistrationToken puts the token in the Feature-Caps of a message, in place of any other.
func withRegistrationToken(h *sip.Header, token string) {
	withoutRegistrationToken(h)

	if v, err := sip.FeatureCapsValue(sip.FeatureSet{sip.NewStringFeature(registrationToken, token)}); err == nil {
		h.Add("Feature-Caps", v)
	}
}

// profileOf is the service profile of a public identity, and the subscription it is in: the registered one of its
// registration set, or else the one the HSS gives for it unregistered, for a user or a PSI (TS 24.229 §5.4.3.2
// NOTE 24, §5.4.3.3 NOTE 27).
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
			r.log.Warn("no service profile holds the identity: no service", slog.String("impu", u.String()))
		}

		return p, sub, true, nil
	}

	set, sub, err := r.unregisteredProfile(ctx, u)
	if err != nil {
		return cx.ServiceProfile{}, cx.IMSSubscription{}, false, err
	}

	p, ok := serviceProfileOf(sub, u, set.Unregistered.Wildcard, r.cfg.HomeDomain)
	if !ok {
		r.log.Warn("no service profile of the HSS holds the identity: no service", slog.String("impu", u.String()))
	}

	return p, sub, false, nil
}

// criteria are the iFCs of a service profile that can be evaluated: one with a regular expression that does not
// compile is left out (TS 29.228 Annex F).
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

// barredIn reports whether the profile bars the identity u (TS 29.228 Annex B.2.1).
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
