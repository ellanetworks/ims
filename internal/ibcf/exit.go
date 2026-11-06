package ibcf

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/internal/bgcf"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/trace"
)

const anonymous = `"Anonymous" <sip:anonymous@anonymous.invalid>`

// TS 23.228 clause 4.14
// TS 24.229 clause 5.10.2.2
// SIPconnect 2.0 section 10.2
// RFC 3261 section 21.5.4
func (b *IBCF) exit(ctx context.Context, r *ua.Request) {
	in := r.Message()

	if !slices.Contains(b.cfg.ICSCF, unmap(in.Flow.Remote)) {
		b.log.WarnContext(ctx, "call to a SIP trunk from another node than the I-CSCF", slog.String("source", in.Flow.Remote.String()))

		_ = r.Reject(403)

		return
	}

	if _, err := remaining(in); err != nil {
		_ = r.Reject(hopsStatus(err))
		return
	}

	number, ok := globalNumber(in.URI)
	if !ok {
		_ = r.Reject(404)
		return
	}

	trunks, err := b.cfg.BGCF.Trunks(number)

	switch {
	case errors.Is(err, bgcf.ErrNoRoute):
		b.log.DebugContext(ctx, "no SIP route for the number", slog.String("number", number))

		_ = r.Reject(404)

		return
	case errors.Is(err, bgcf.ErrNoUsableTrunk):
		b.log.InfoContext(ctx, "no usable SIP trunk for the number", slog.String("number", number))

		_ = r.Reject(503)

		return
	case err != nil:
		b.log.WarnContext(ctx, "SIP route selection failed", slog.String("number", number), slog.Any("error", err))

		_ = r.Reject(500)

		return
	}

	privacy, err := in.Header.Privacy()
	if err != nil {
		_ = r.Reject(400)
		return
	}

	if unsupported := unsupportedPrivacy(privacy, b.cfg.Media != nil); unsupported != "" {
		_ = r.Respond(ua.Reply{Code: 500, Reason: "Privacy Failure: " + unsupported})
		return
	}

	x, ok := b.exitOf(ctx, in, privacy)
	if !ok {
		_ = r.Reject(403)
		return
	}

	c := &call{
		b: b, outbound: true, initial: in, in: r, icid: icidOf(in.Header), cause: trace.SpanContextFromContext(ctx),
		route: b.newRoute(trunks), exit: x,
	}

	if c.icid != "" {
		trace.SpanFromContext(ctx).SetAttributes(tracing.ICIDKey.String(c.icid))
	}

	if !b.legs.add(key{in.Header.CallID(), tagOf(in), false}, &leg{call: c}) {
		_ = r.Reject(482)
		return
	}

	out := b.attempt(ctx, c)
	if out == nil {
		_ = r.Reject(503)

		b.release(c)

		return
	}

	b.bridge(ctx, r, c, out)
}

func tagOf(req *sip.Request) string {
	from, _ := req.Header.From()
	return from.Tag()
}

// RFC 3263 section 4.3
func (b *IBCF) failover(ctx context.Context, c *call, f b2bua.Failure) *sip.Request {
	if !f.ServerFailed() {
		return nil
	}

	t, _ := c.owner()

	next := b.attempt(ctx, c)
	if _, ok := b.cfg.Settings().Trunk(t.ID); ok && next != nil {
		b.cfg.Metrics.failover(t.ID)
	}

	return next
}

// RFC 3263 section 4.3
// SIPconnect 2.0 section 17.1.1.2
func (b *IBCF) attempt(ctx context.Context, c *call) *sip.Request {
	for {
		next, ok := c.route.next(ctx)
		if !ok {
			return nil
		}

		c.mu.Lock()

		if c.callID == "" || c.trunk.ID != next.trunk.ID {
			c.callID, c.fromTag = sip.NewTag(), sip.NewTag()
		}

		c.trunk, c.signal = next.trunk, next.local.Addr()
		callID, fromTag := c.callID, c.fromTag

		c.mu.Unlock()

		c.own(next.trunk)

		out, err := b.toTrunk(c, next, callID, fromTag)
		if err != nil {
			b.log.DebugContext(ctx, "INVITE to the SIP trunk not built", slog.String("trunk", next.trunk.Name), slog.Any("error", err))
			return nil
		}

		k := key{callID, fromTag, true}
		l := b.legs.get(k)

		if l == nil {
			l = &leg{call: c, trunk: true}
			if !b.legs.add(k, l) {
				continue
			}
		}

		l.setFlow(sip.Flow{Transport: next.server.Transport, Local: next.local, Remote: next.server.Addr}, next.addr.Host)

		return out
	}
}

func (b *IBCF) bridge(ctx context.Context, r *ua.Request, c *call, out *sip.Request) {
	bc := b.begin(r, c)

	if b.cfg.Media != nil {
		c.media = newMedia(c, *b.cfg.Media, bc)
	}

	if _, err := b2bua.Bridge(ctx, bc, r, out, b.relay(c)); err != nil {
		b.log.DebugContext(ctx, "call not bridged", slog.String("call_id", r.Message().Header.CallID()), slog.Any("error", err))
	}
}

// RFC 3323 section 5
// RFC 3323 section 5.2
func unsupportedPrivacy(p sip.Privacy, anchored bool) string {
	if !p.Has(sip.PrivacyCritical) {
		return ""
	}

	for _, v := range p {
		switch strings.ToLower(v) {
		case sip.PrivacyNone, sip.PrivacyCritical, sip.PrivacyID, sip.PrivacyUser, sip.PrivacyHeader, sip.PrivacyHistory:
		case sip.PrivacySession:
			if !anchored {
				return v
			}
		default:
			return v
		}
	}

	return ""
}

// SIPconnect 2.0 section 9
// SIPconnect 2.0 section 11.1
func (b *IBCF) exitOf(ctx context.Context, in *sip.Request, privacy sip.Privacy) (exit, bool) {
	x := exit{number: in.URI, privacy: privacy}

	fwd, forwarded := forwarderOf(in.Header)
	if !forwarded {
		caller, ok := callerOf(in.Header)
		if !ok {
			b.log.InfoContext(ctx, "off-net call of a caller without an E.164 number",
				slog.Any("p-asserted-identity", in.Header.Values("P-Asserted-Identity")))

			return exit{}, false
		}

		x.asserted = sip.Address{Display: caller.Display, URI: phoneURI(caller.URI.User, b.cfg.HomeDomain)}
		x.caller = x.asserted

		return x, true
	}

	if fwd.number == "" {
		b.log.InfoContext(ctx, "off-net call forwarded by a user without an E.164 number",
			slog.Any("history-info", in.Header.Values("History-Info")))

		return exit{}, false
	}

	x.asserted = sip.Address{URI: phoneURI(fwd.number, b.cfg.HomeDomain)}
	x.forwarder = &fwd
	x.caller = b.originalCaller(in.Header)

	return x, true
}

func (b *IBCF) originalCaller(h sip.Header) sip.Address {
	if caller, ok := callerOf(h); ok {
		return sip.Address{Display: caller.Display, URI: phoneURI(caller.URI.User, b.cfg.HomeDomain)}
	}

	from, err := h.From()
	if err != nil {
		a, _ := sip.ParseAddress(anonymous)
		return a
	}

	if number, ok := globalNumber(from.URI); ok {
		return sip.Address{Display: from.Display, URI: phoneURI(number, b.cfg.HomeDomain)}
	}

	return sip.Address{Display: from.Display, URI: from.URI}
}

type forwarder struct {
	number  string
	private bool
}

// RFC 7044 section 5
// RFC 7044 section 10.4
// TS 24.604 clause 4.5.2.6.2.2
// TS 24.604 clause 4.5.2.6.2.3
func forwarderOf(h sip.Header) (forwarder, bool) {
	entries, err := h.Addresses("History-Info")
	if err != nil {
		return forwarder{}, false
	}

	for i := len(entries) - 1; i >= 0; i-- {
		mp, ok := entries[i].Params.Get("mp")
		if !ok {
			continue
		}

		j := slices.IndexFunc(entries[:i], func(e sip.Address) bool {
			index, _ := e.Params.Get("index")
			return index == mp
		})
		if j < 0 {
			return forwarder{}, true
		}

		served := entries[j].URI
		number, _ := globalNumber(served)

		embedded, err := served.Embedded()
		if err != nil {
			return forwarder{}, true
		}

		privacy, err := embedded.Header.Privacy()

		return forwarder{number: number, private: err != nil || privacy.Has(sip.PrivacyHistory)}, true
	}

	return forwarder{}, false
}

// TS 24.229 clause 5.10.8
// SIPconnect 2.0 section 9
// SIPconnect 2.0 section 10.2.1
// SIPconnect 2.0 section 10.2.2
// SIPconnect 2.0 section 10.2.3
// SIPconnect 2.0 section 10.2.4
// SIPconnect 2.0 section 10.2.5
// SIPconnect 2.0 section 11.1
// RFC 3325 section 7
func (b *IBCF) toTrunk(c *call, next target, callID, fromTag string) (*sip.Request, error) {
	in, x, t := c.initial, c.exit, next.trunk

	uri := sip.URI{Scheme: "sip", User: subscriber(x.number, t.Trusted), Host: host(t.Domain)}
	uri.Params.Set("user", "phone")

	out := sip.NewRequest("INVITE", uri)

	hidden := !x.privacy.Has(sip.PrivacyNone) && (x.privacy.Has(sip.PrivacyID) || x.privacy.Has(sip.PrivacyUser))

	from := x.caller
	if hidden {
		from, _ = sip.ParseAddress(anonymous)
	}

	from = from.Clone()
	from.Params.Set("tag", fromTag)

	out.Header.Add("From", from.String())
	out.Header.Add("To", "<"+uri.String()+">")
	out.Header.Add("Call-ID", callID)

	if t.Trusted || !withholds(x.privacy) {
		out.Header.Add("P-Asserted-Identity", x.asserted.String())
	}

	privacy := x.privacy

	if x.forwarder != nil {
		var served sip.URI

		served, privacy = b.served(*x.forwarder, x.privacy, t.Trusted)
		out.Header.Add("History-Info", "<"+served.String()+">;index=1")
		out.Header.Add("History-Info", "<"+uri.String()+">;index=1.1;mp=1")
	}

	if len(privacy) > 0 {
		out.Header.Add("Privacy", privacy.String())
	}

	out.Header.Add("Contact", b.contact(next.local, next.server.Transport).String())

	if err := forwards(in, out); err != nil {
		return nil, err
	}

	return out, nil
}

// RFC 7044 section 10.1.2
// TS 24.229 clause 4.4.4
// TS 24.229 clause 5.10.8
func (b *IBCF) served(f forwarder, privacy sip.Privacy, trusted bool) (sip.URI, sip.Privacy) {
	u := phoneURI(f.number, b.cfg.HomeDomain)
	asked := !privacy.Has(sip.PrivacyNone) && (privacy.Has(sip.PrivacyHistory) || privacy.Has(sip.PrivacyHeader))

	switch {
	case trusted && f.private:
		u.SetEmbedded(sip.Envelope{Header: sip.Header{{Name: "Privacy", Value: sip.PrivacyHistory}}})
	case trusted:
	case f.private || asked:
		u = sip.URI{Scheme: "sip", User: "anonymous", Host: "anonymous.invalid"}
		privacy = withoutHistory(privacy)
	}

	return u, privacy
}

// RFC 3323 section 5
// RFC 7044 section 10.1.2
func withoutHistory(p sip.Privacy) sip.Privacy {
	out := slices.DeleteFunc(slices.Clone(p), func(v string) bool { return strings.EqualFold(v, sip.PrivacyHistory) })

	if !slices.ContainsFunc(out, func(v string) bool { return !strings.EqualFold(v, sip.PrivacyCritical) }) {
		return nil
	}

	return out
}

// RFC 3261 section 19.1.6
// TS 24.229 clause 4.4.12
// SIPconnect 2.0 section 9
func subscriber(u sip.URI, trusted bool) string {
	number, _ := globalNumber(u)

	var first, rest []sip.Param

	if !u.IsTel() {
		return number
	}

	for _, p := range u.Params {
		name := strings.ToLower(p.Name)

		switch {
		case name == "phone-context":
		case !trusted && (name == "cpc" || name == "oli"):
		case name == "isub" || name == "postd":
			first = append(first, sip.Param{Name: name, Value: strings.ToLower(p.Value)})
		default:
			rest = append(rest, sip.Param{Name: name, Value: strings.ToLower(p.Value)})
		}
	}

	slices.SortStableFunc(first, func(a, b sip.Param) int { return cmp.Compare(a.Name, b.Name) })
	slices.SortStableFunc(rest, func(a, b sip.Param) int { return cmp.Compare(a.Name, b.Name) })

	var sb strings.Builder

	sb.WriteString(number)

	for _, p := range append(first, rest...) {
		sb.WriteString(";" + p.Name)

		if p.Value != "" {
			sb.WriteString("=" + p.Value)
		}
	}

	return sb.String()
}

var (
	errLoop        = errors.New("ibcf: Max-Forwards exhausted")
	errMaxForwards = errors.New("ibcf: Max-Forwards not a number")
)

// RFC 3261 section 16.3
func remaining(in *sip.Request) (int, error) {
	v := in.Header.Get("Max-Forwards")
	if v == "" {
		return -1, nil
	}

	mf, err := strconv.Atoi(strings.TrimSpace(v))

	switch {
	case err != nil || mf < 0:
		return 0, errMaxForwards
	case mf == 0:
		return 0, errLoop
	}

	return mf - 1, nil
}

func hopsStatus(err error) int {
	if errors.Is(err, errLoop) {
		return 483
	}

	return 400
}

func forwards(in, out *sip.Request) error {
	mf, err := remaining(in)
	if err != nil || mf < 0 {
		return err
	}

	out.Header.Set("Max-Forwards", strconv.Itoa(mf))

	return nil
}

func phoneURI(number, home string) sip.URI {
	u := sip.URI{Scheme: "sip", User: number, Host: home}
	u.Params.Set("user", "phone")

	return u
}

// SIPconnect 2.0 section 9
func callerOf(h sip.Header) (sip.Address, bool) {
	asserted, err := h.Addresses("P-Asserted-Identity")
	if err != nil {
		return sip.Address{}, false
	}

	var found sip.Address

	ok := false

	for _, a := range asserted {
		number, global := globalNumber(a.URI)
		if !global {
			continue
		}

		if !ok || a.URI.IsTel() {
			found = sip.Address{Display: a.Display, URI: sip.URI{Scheme: "tel", User: number}}
			ok = true
		}
	}

	return found, ok
}

// SIPconnect 2.0 section 9
func globalNumber(u sip.URI) (string, bool) {
	switch {
	case u.IsTel():
		return bgcf.Number(u)
	case !u.IsSIP():
		return "", false
	}

	user, _, _ := strings.Cut(u.User, ";")

	number := strings.Map(func(r rune) rune {
		if strings.ContainsRune("-.()", r) {
			return -1
		}

		return r
	}, user)

	digits, ok := strings.CutPrefix(number, "+")
	if !ok || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return "", false
	}

	return number, true
}
