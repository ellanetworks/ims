package ibcf

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
)

type ResolveFunc func(ctx context.Context, u sip.URI, transports []sip.Transport) ([]sip.Server, error)

func SystemResolve(ctx context.Context, u sip.URI, transports []sip.Transport) ([]sip.Server, error) {
	r := &sip.Resolver{Transports: transports}

	return r.Resolve(ctx, u)
}

type hop struct {
	trunk settings.Trunk
	addr  settings.TrunkAddress
}

type target struct {
	hop
	server sip.Server
	local  netip.AddrPort
}

// TS 23.228 clause 4.14
// RFC 3263 section 4.3
type route struct {
	b *IBCF

	mu      sync.Mutex
	hops    []hop
	at      int
	servers []target
	server  int
	known   map[string][]netip.Addr
}

func (b *IBCF) newRoute(trunks []settings.Trunk) *route {
	rt := &route{b: b, at: -1, known: map[string][]netip.Addr{}}

	for _, t := range trunks {
		for _, a := range t.Addresses {
			rt.hops = append(rt.hops, hop{trunk: t, addr: a})
		}
	}

	return rt
}

func (r *route) next(ctx context.Context) (target, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.server++

	for r.server >= len(r.servers) {
		r.at++
		if r.at >= len(r.hops) {
			return target{}, false
		}

		r.servers, r.server = r.resolve(ctx, r.hops[r.at]), 0
	}

	return r.servers[r.server], true
}

func (r *route) resolve(ctx context.Context, h hop) []target {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	servers, err := r.b.resolveTrunk(ctx, h.trunk, addressURI(h.addr))
	if err != nil {
		r.b.log.WarnContext(ctx, "SIP trunk address not resolved", slog.String("trunk", h.trunk.Name), slog.String("address", h.addr.String()),
			slog.Any("error", err))

		return nil
	}

	out := make([]target, 0, len(servers))

	for _, srv := range servers {
		local, ok := r.b.local(srv.Transport, srv.Addr.Addr())
		if !ok {
			continue
		}

		r.b.peers.note(h.trunk, h.addr, srv)
		out = append(out, target{hop: h, server: srv, local: local})
		r.known[h.trunk.ID] = append(r.known[h.trunk.ID], srv.Addr.Addr())
	}

	return out
}

func (r *route) addresses(trunk string) []netip.Addr {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.known[trunk])
}

func (b *IBCF) resolveTrunk(ctx context.Context, t settings.Trunk, u sip.URI) ([]sip.Server, error) {
	allowed := b.transports(t)

	servers, err := b.resolve(ctx, u, allowed)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(servers, func(s sip.Server) bool {
		_, ok := b.local(s.Transport, s.Addr.Addr())
		return !ok || !slices.Contains(allowed, s.Transport)
	}), nil
}

// RFC 3263 section 4.1
// RFC 3263 section 4.2
func addressURI(a settings.TrunkAddress) sip.URI {
	u := sip.URI{Scheme: "sip", Host: host(a.Host), Port: uint16(a.Port)}
	if a.Transport != "" {
		u.Params.Set("transport", strings.ToLower(string(a.Transport)))
	}

	return u
}

func host(h string) string {
	if a, err := netip.ParseAddr(h); err == nil {
		return sip.FormatHost(a)
	}

	return h
}
