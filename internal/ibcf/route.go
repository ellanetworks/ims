package ibcf

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

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
	b        *IBCF
	deadline time.Time
	stop     <-chan struct{}
	resolved func([]netip.Addr)

	mu      sync.Mutex
	hops    []hop
	at      int
	servers []target
	server  int
	known   map[string][]netip.Addr
}

func (b *IBCF) newRoute(trunks []settings.Trunk, stop <-chan struct{}) *route {
	rt := &route{b: b, deadline: time.Now().Add(failoverBudget), stop: stop, at: -1, known: map[string][]netip.Addr{}}

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
		if r.at >= len(r.hops) || !time.Now().Before(r.deadline) {
			return target{}, false
		}

		h := r.hops[r.at]

		r.mu.Unlock()
		servers, ok := r.lookup(ctx, h)
		r.mu.Lock()

		if !ok {
			return target{}, false
		}

		addrs := make([]netip.Addr, 0, len(servers))

		for _, t := range servers {
			r.known[t.trunk.ID] = append(r.known[t.trunk.ID], t.server.Addr.Addr())
			addrs = append(addrs, t.server.Addr.Addr())
		}

		if r.resolved != nil {
			r.resolved(addrs)
		}

		r.servers, r.server = servers, 0
	}

	return r.servers[r.server], true
}

func (r *route) lookup(ctx context.Context, h hop) ([]target, bool) {
	deadline := time.Now().Add(resolveTimeout)
	if r.deadline.Before(deadline) {
		deadline = r.deadline
	}

	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	done := make(chan []target, 1)

	go func() { done <- r.resolve(ctx, h) }()

	select {
	case out := <-done:
		return out, true
	case <-r.stop:
		return nil, false
	}
}

func (r *route) resolve(ctx context.Context, h hop) []target {
	servers, err := r.b.resolveAddress(ctx, h.trunk, h.addr)
	if err != nil {
		r.b.log.WarnContext(ctx, "SIP trunk address not resolved", slog.String("trunk", h.trunk.Name), slog.String("address", h.addr.String()),
			slog.Any("error", err))

		return nil
	}

	out := make([]target, 0, len(servers))

	for _, srv := range servers {
		local, ok := r.b.local(srv.Transport, srv.Addr.Addr())
		if !ok || !r.b.noteServer(h.trunk, h.addr, srv) {
			continue
		}

		out = append(out, target{hop: h, server: srv, local: local})
	}

	return out
}

func (r *route) addresses(trunk string) []netip.Addr {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.known[trunk])
}

func (b *IBCF) resolveTrunk(ctx context.Context, t settings.Trunk, u sip.URI) ([]sip.Server, error) {
	return b.resolveVia(ctx, u, b.transports(t))
}

// SIPconnect 2.0 section 8.1
// SIPconnect 2.0 section 17.1.1.2
// RFC 3263 section 4.1
func (b *IBCF) resolveAddress(ctx context.Context, t settings.Trunk, a settings.TrunkAddress) ([]sip.Server, error) {
	return b.resolveVia(ctx, addressURI(a), addressTransports(b.transports(t), a))
}

func (b *IBCF) resolveVia(ctx context.Context, u sip.URI, allowed []sip.Transport) ([]sip.Server, error) {
	if len(allowed) == 0 {
		return nil, errNoTransport
	}

	servers, err := b.resolve(ctx, u, allowed)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(servers, func(s sip.Server) bool {
		_, ok := b.local(s.Transport, s.Addr.Addr())
		return !ok || !slices.Contains(allowed, s.Transport)
	}), nil
}

var errNoTransport = errors.New("ibcf: no transport the SIP trunk address allows")

func addressTransports(allowed []sip.Transport, a settings.TrunkAddress) []sip.Transport {
	if a.Transport == "" {
		return allowed
	}

	tr := sip.Transport(strings.ToUpper(string(a.Transport)))
	if !slices.Contains(allowed, tr) {
		return nil
	}

	return []sip.Transport{tr}
}

// RFC 3263 section 4.1
// RFC 3263 section 4.2
func addressURI(a settings.TrunkAddress) sip.URI {
	return sip.URI{Scheme: "sip", Host: host(a.Host), Port: uint16(a.Port)}
}

func host(h string) string {
	if a, err := netip.ParseAddr(h); err == nil {
		return sip.FormatHost(a)
	}

	return h
}
