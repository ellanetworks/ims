package icscf

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
)

// TS 24.229 clause 5.3.2.1
// TS 24.229 clause 5.6.2
// RFC 3261 section 16.6
func (i *ICSCF) breakout(ctx context.Context, s *session) bool {
	switch {
	case i.cfg.BGCF == nil || len(i.cfg.IBCF) == 0 || s.req.Method != "INVITE":
		return false
	case s.onNet:
		i.log.DebugContext(ctx, "unknown on-net number not routed to the IBCF", slog.String("request-uri", s.req.URI.String()))
		return false
	case !i.fromSCSCF(s.req.Flow):
		i.log.DebugContext(ctx, "unknown number from another node than the S-CSCF not routed to the IBCF",
			slog.String("request-uri", s.req.URI.String()), slog.String("source", s.req.Flow.Remote.String()))

		return false
	case i.fromIBCF(s.req.Flow):
		i.log.DebugContext(ctx, "unknown number from the IBCF not routed back to it", slog.String("request-uri", s.req.URI.String()))
		return false
	case !i.cfg.BGCF.Routable(s.req.URI):
		i.log.DebugContext(ctx, "no SIP route for the unknown number", slog.String("request-uri", s.req.URI.String()))
		return false
	}

	to, ok := i.ibcf(s.req.Flow)
	if !ok {
		i.log.WarnContext(ctx, "no IBCF listener for an off-net request", slog.String("local", s.req.Flow.Local.String()))
		return false
	}

	route := sip.URI{Scheme: "sip", Host: sip.FormatHost(to.Flow.Remote.Addr()), Port: to.Flow.Remote.Port()}
	if to.Flow.Transport != sip.UDP {
		route.Params.Set("transport", strings.ToLower(string(to.Flow.Transport)))
	}

	route.Params.Set("lr", "")

	out := s.req.Clone()
	out.Header.Del("P-Served-User")
	out.Header.Prepend("Route", "<"+route.String()+">")

	opts := proxy.Options{OnReply: func(ctx context.Context, r proxy.Reply) proxy.Verdict { return i.sessionReply(ctx, s, r) }}

	i.log.DebugContext(ctx, "unknown number routed to the IBCF", slog.String("request-uri", out.URI.String()),
		slog.String("ibcf", to.Flow.Remote.String()))
	i.forward(ctx, s.tx, out, to, opts)

	return true
}

func (i *ICSCF) fromSCSCF(in sip.Flow) bool {
	return slices.ContainsFunc(i.cfg.SCSCF.Listeners, func(l netip.AddrPort) bool { return unmapped(l) == unmapped(in.Remote) })
}

func unmapped(a netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
}

func (i *ICSCF) fromIBCF(in sip.Flow) bool {
	remote := netip.AddrPortFrom(in.Remote.Addr().Unmap(), in.Remote.Port())

	return slices.ContainsFunc(i.cfg.IBCF, func(l netip.AddrPort) bool {
		return netip.AddrPortFrom(l.Addr().Unmap(), l.Port()) == remote
	})
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
