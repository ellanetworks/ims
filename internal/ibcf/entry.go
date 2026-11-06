package ibcf

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"

	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// TS 24.229 clause 4.5.2
// TS 24.229 clause 5.10.1
// TS 24.229 clause 5.10.3.2
// SIPconnect 2.0 section 10.1.1
// SIPconnect 2.0 section 10.1.2
// SIPconnect 2.0 section 17.6
func (b *IBCF) entry(ctx context.Context, r *ua.Request) {
	in := r.Message()

	t, ok := b.trunkFor(in.Flow)
	if !ok || !t.Enabled {
		_ = r.Reject(403)
		return
	}

	if routes, _ := in.Header.Routes(); len(routes) > 0 && routes[0].URI.Params.Has("orig") {
		b.log.WarnContext(ctx, "originating request from a SIP trunk", slog.String("trunk", t.Name),
			slog.String("source", in.Flow.Remote.String()))

		_ = r.Reject(403)

		return
	}

	number, ok := globalNumber(in.URI)
	if !ok {
		b.log.DebugContext(ctx, "incoming call to no global number", slog.String("trunk", t.Name), slog.String("request_uri", in.URI.String()))

		_ = r.Reject(404)

		return
	}

	icscf, ok := b.icscf()
	if !ok {
		_ = r.Reject(500)
		return
	}

	fromTag := sip.NewTag()

	out, err := b.toIMS(in, t.Trusted, number, icscf.Local, fromTag)
	if err != nil {
		_ = r.Reject(483)
		return
	}

	c := &call{b: b, trunk: t, signal: in.Flow.Local.Addr(), initial: in}

	if t.Trusted {
		c.icid = icidOf(in.Header)
	}

	if c.icid == "" {
		c.icid = sip.NewTag()
		out.Header.Set("P-Charging-Vector", "icid-value="+c.icid)
	}

	if !b.legs.add(key{in.Header.CallID(), tagOf(in), false}, &leg{call: c, trunk: true, flow: in.Flow}) {
		_ = r.Reject(482)
		return
	}

	if !b.legs.add(key{out.Header.CallID(), fromTag, true}, &leg{call: c, flow: icscf}) {
		b.release(c)

		_ = r.Reject(500)

		return
	}

	b.bridge(ctx, r, c, out)
}

// TS 24.229 clause 5.10.3.2
func (b *IBCF) icscf() (sip.Flow, bool) {
	for _, l := range b.ls().internal {
		for _, i := range b.cfg.ICSCF {
			if l.Addr().Is4() == i.Addr().Unmap().Is4() {
				return sip.Flow{Transport: sip.UDP, Local: l, Remote: i}, true
			}
		}
	}

	return sip.Flow{}, false
}

// TS 24.229 clause 4.4
// SIPconnect 2.0 section 9
// SIPconnect 2.0 section 10.1.3
// SIPconnect 2.0 section 10.1.4
func (b *IBCF) toIMS(in *sip.Request, trusted bool, number string, local netip.AddrPort, fromTag string) (*sip.Request, error) {
	out := sip.NewRequest("INVITE", phoneURI(number, b.cfg.HomeDomain))

	from, err := in.Header.From()
	if err != nil {
		return nil, err
	}

	to, err := in.Header.To()
	if err != nil {
		return nil, err
	}

	from = sip.Address{Display: from.Display, URI: from.URI}
	to = sip.Address{Display: to.Display, URI: to.URI}

	if !trusted {
		from.URI, to.URI = trust.StripURI(from.URI), trust.StripURI(to.URI)
	}

	from.Params.Set("tag", fromTag)

	out.Header.Add("From", from.String())
	out.Header.Add("To", to.String())
	out.Header.Add("Call-ID", sip.NewTag())
	out.Header.Add("Contact", b.contact(local, sip.UDP).String())

	if trusted {
		for _, a := range assertedOf(in.Header) {
			out.Header.Add("P-Asserted-Identity", a.String())
		}
	}

	if err := forwards(in, out); err != nil {
		return nil, err
	}

	return out, nil
}

// RFC 3325 section 5
// SIPconnect 2.0 section 10.1.4
func assertedOf(h sip.Header) []sip.Address {
	asserted, err := h.Addresses("P-Asserted-Identity")
	if err != nil {
		return nil
	}

	var out []sip.Address

	if i := slices.IndexFunc(asserted, func(a sip.Address) bool { return a.URI.IsSIP() }); i >= 0 {
		out = append(out, asserted[i])
	}

	if i := slices.IndexFunc(asserted, func(a sip.Address) bool { return a.URI.IsTel() }); i >= 0 {
		out = append(out, asserted[i])
	}

	return out
}
