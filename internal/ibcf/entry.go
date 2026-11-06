package ibcf

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/trace"
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

	switch {
	case !ok:
		_ = r.Reject(403)
		return
	case !t.Enabled:
		_ = r.Reject(503)
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

	out, err := b.toIMS(in, t, number, icscf.Local, fromTag)

	switch {
	case errors.Is(err, errLoop):
		_ = r.Reject(483)
		return
	case err != nil:
		_ = r.Reject(400)
		return
	}

	c := &call{b: b, trunk: t, signal: in.Flow.Local.Addr(), initial: in, in: r, cause: trace.SpanContextFromContext(ctx)}

	if t.Trusted {
		c.icid = icidOf(in.Header)
	}

	if c.icid == "" {
		c.icid = sip.NewTag()
		out.Header.Set("P-Charging-Vector", "icid-value="+c.icid)
	}

	trace.SpanFromContext(ctx).SetAttributes(tracing.ICIDKey.String(c.icid))

	if !b.legs.add(key{in.Header.CallID(), tagOf(in), false}, &leg{call: c, trunk: true, flow: b.trunkDialogFlow(ctx, t, in)}) {
		_ = r.Reject(482)
		return
	}

	if !b.legs.add(key{out.Header.CallID(), fromTag, true}, &leg{call: c, flow: icscf}) {
		b.release(c)

		_ = r.Reject(500)

		return
	}

	c.own(t)

	identities := identitiesOf(out)
	b.cfg.Records.Attempt(callrecords.Attempt{
		ICID: c.icid, CallID: in.Header.CallID(), RequestURI: in.URI.String(), Asserted: identities,
		InTrunk: t.ID, InName: t.Name,
	})
	b.cfg.Records.Routed(c.icid, callrecords.Routing{Asserted: identities, RequestURI: out.URI.String()})

	b.bridge(ctx, r, c, out)
}

func identitiesOf(out *sip.Request) []string {
	asserted, _ := out.Header.Addresses("P-Asserted-Identity")
	if len(asserted) == 0 {
		if from, err := out.Header.From(); err == nil {
			asserted = []sip.Address{from}
		}
	}

	ids := make([]string, 0, len(asserted))
	for _, a := range asserted {
		ids = append(ids, a.URI.String())
	}

	return ids
}

// TS 24.229 clause 5.10.3.2
func (b *IBCF) icscf() (sip.Flow, bool) {
	for _, l := range b.cfg.Internal {
		for _, i := range b.cfg.ICSCF {
			if l.Addr().Is4() == i.Addr().Unmap().Is4() {
				return sip.Flow{Transport: sip.UDP, Local: l, Remote: i}, true
			}
		}
	}

	return sip.Flow{}, false
}

// TS 24.173 clause 5.2
const acceptMMTel = `*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`

// TS 24.229 clause 4.4
// TS 24.173 clause 5.2
// SIPconnect 2.0 section 9
// SIPconnect 2.0 section 10.1.3
// SIPconnect 2.0 section 10.1.4
func (b *IBCF) toIMS(in *sip.Request, t settings.Trunk, number string, local netip.AddrPort, fromTag string) (*sip.Request, error) {
	trusted := t.Trusted

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

		if from.URI.IsSIP() && b.underHome(from.URI.Host) {
			from.URI.Host = host(t.Domain)
		}
	}

	from.Params.Set("tag", fromTag)

	out.Header.Add("From", from.String())
	out.Header.Add("To", to.String())
	out.Header.Add("Call-ID", sip.NewTag())
	out.Header.Add("Contact", b.contact(local, sip.UDP).String())
	out.Header.Add("Accept-Contact", acceptMMTel)

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

func (b *IBCF) underHome(h string) bool {
	h, home := strings.ToLower(strings.TrimSuffix(h, ".")), strings.ToLower(b.cfg.HomeDomain)

	return h == home || strings.HasSuffix(h, "."+home)
}

// RFC 3263 section 4.1
// SIPconnect 2.0 section 17.1.1.2
// SIPconnect 2.0 section 17.2
func (b *IBCF) trunkDialogFlow(ctx context.Context, t settings.Trunk, in *sip.Request) sip.Flow {
	f := in.Flow

	if f.Transport != sip.TLS || t.TLS.Required {
		return f
	}

	if _, ours := b.peers.get(f.Remote); ours {
		return f
	}

	unset := sip.Flow{Transport: sip.TLS, Local: f.Local}

	contacts, err := in.Header.Contacts()
	if err != nil || len(contacts) != 1 {
		return unset
	}

	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	servers, err := b.resolve(ctx, contacts[0].URI, []sip.Transport{sip.TLS})
	if err != nil {
		return unset
	}

	for _, srv := range servers {
		if srv.Transport == sip.TLS && inTrunk(t, nil, srv.Addr.Addr()) && srv.Addr.Addr().Unmap().Is4() == f.Local.Addr().Unmap().Is4() {
			return sip.Flow{Transport: sip.TLS, Local: f.Local, Remote: unmap(srv.Addr)}
		}
	}

	return unset
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
