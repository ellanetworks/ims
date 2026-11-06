package sip

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"strings"
)

// RFC 3263 section 4
type Server struct {
	Transport Transport
	Addr      netip.AddrPort
	Domain    string
}

type NAPTR struct {
	Order       uint16
	Preference  uint16
	Flags       string
	Service     string
	Regexp      string
	Replacement string
}

type DNS interface {
	LookupNAPTR(ctx context.Context, name string) ([]NAPTR, error)
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// RFC 3263 section 4
// RFC 3263 section 4.1
type Resolver struct {
	DNS        DNS
	Transports []Transport
	Rand       func(n uint64) uint64
}

// RFC 3263 section 4
func (r *Resolver) Resolve(ctx context.Context, u URI) ([]Server, error) {
	servers, err := r.resolve(ctx, u)
	if err != nil {
		return nil, err
	}

	if _, ok := HostAddr(target(u)); !ok {
		for i := range servers {
			servers[i].Domain = strings.TrimSuffix(target(u), ".")
		}
	}

	return servers, nil
}

// RFC 3263 section 4.1
// RFC 3263 section 4.2
// RFC 3263 section 4.3
func (r *Resolver) resolve(ctx context.Context, u URI) ([]Server, error) {
	if !u.IsSIP() || target(u) == "" {
		return nil, fmt.Errorf("sip: no destination in %s", u)
	}

	secure := u.IsSIPS()

	tr, explicit, err := uriTransport(u, secure)
	if err != nil {
		return nil, err
	}

	if !explicit && !r.supports(tr, secure) {
		tr = r.preferred(tr)
	}

	if a, ok := HostAddr(target(u)); ok {
		port := u.Port
		if port == 0 {
			port = DefaultPortFor(tr)
		}

		return []Server{{Transport: tr, Addr: netip.AddrPortFrom(a.Unmap(), port)}}, nil
	}

	host := target(u)

	if u.Port != 0 {
		return r.addresses(ctx, host, tr, u.Port)
	}

	transports := []Transport{tr}

	if !explicit {
		servers, err := r.naptr(ctx, host, secure)
		if err != nil || len(servers) > 0 {
			return servers, err
		}

		transports = r.fallback(secure)
	}

	var (
		servers []Server
		found   bool
	)

	for _, t := range transports {
		service, proto := srvService(t)

		s, ok, err := r.srv(ctx, service, proto, host, t)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if err != nil {
			continue
		}

		servers = append(servers, s...)
		found = found || ok
	}

	switch {
	case len(servers) > 0:
		return servers, nil
	case found:
		return nil, fmt.Errorf("sip: no address for the SRV targets of %s", strings.TrimSuffix(host, "."))
	}

	return r.addresses(ctx, host, tr, DefaultPortFor(tr))
}

// RFC 3263 section 4.1
// SIPconnect 2.0 section 8.1
func uriTransport(u URI, secure bool) (Transport, bool, error) {
	t, ok := u.Params.Get("transport")
	if !ok || strings.EqualFold(t, "tls") {
		if secure {
			return TLS, false, nil
		}

		return UDP, false, nil
	}

	tr := Transport(strings.ToUpper(t))

	switch {
	case secure && tr == TCP:
		return TLS, true, nil
	case !secure && (tr == UDP || tr == TCP):
		return tr, true, nil
	}

	return "", false, fmt.Errorf("sip: destination transport %s", t)
}

func (r *Resolver) supports(tr Transport, secure bool) bool {
	if secure {
		return tr == TLS
	}

	if r.Transports == nil {
		return tr == UDP || tr == TCP
	}

	return slices.Contains(r.Transports, tr)
}

// RFC 3263 section 4.1
func (r *Resolver) preferred(tr Transport) Transport {
	if i := slices.IndexFunc(r.Transports, func(t Transport) bool { return t == TCP || t == TLS }); i >= 0 {
		return r.Transports[i]
	}

	return tr
}

// RFC 3263 section 4.1
// RFC 3263 section 4.2
func (r *Resolver) fallback(secure bool) []Transport {
	if secure {
		return []Transport{TLS}
	}

	var out []Transport

	for _, tr := range []Transport{TLS, UDP, TCP} {
		if r.supports(tr, false) {
			out = append(out, tr)
		}
	}

	return out
}

// RFC 3263 section 4.1
func naptrTransport(service string) (Transport, bool) {
	switch strings.ToUpper(service) {
	case "SIP+D2U":
		return UDP, true
	case "SIP+D2T":
		return TCP, true
	case "SIPS+D2T":
		return TLS, true
	}

	return "", false
}

// RFC 3263 section 4.1
// RFC 2915 section 2
// RFC 2915 section 4
func (r *Resolver) naptr(ctx context.Context, host string, secure bool) ([]Server, error) {
	records, err := r.dns().LookupNAPTR(ctx, host)
	if err != nil {
		return nil, ctx.Err()
	}

	type usable struct {
		NAPTR
		tr Transport
	}

	var candidates []usable

	for _, rec := range records {
		tr, ok := naptrTransport(rec.Service)
		if !ok || !r.supports(tr, secure) || !strings.EqualFold(rec.Flags, "s") || rec.Regexp != "" {
			continue
		}

		if strings.TrimSuffix(rec.Replacement, ".") != "" {
			candidates = append(candidates, usable{rec, tr})
		}
	}

	slices.SortStableFunc(candidates, func(a, b usable) int {
		return cmp.Or(cmp.Compare(a.Order, b.Order), cmp.Compare(a.Preference, b.Preference))
	})

	for _, c := range candidates {
		if c.Order != candidates[0].Order {
			break
		}

		servers, _, err := r.srv(ctx, "", "", absolute(c.Replacement), c.tr)
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if len(servers) > 0 {
			return servers, nil
		}
	}

	return nil, nil
}

func absolute(name string) string {
	if strings.HasSuffix(name, ".") {
		return name
	}

	return name + "."
}

// RFC 3263 section 4.2
func srvService(tr Transport) (string, string) {
	if tr == TLS {
		return "sips", "tcp"
	}

	return "sip", strings.ToLower(string(tr))
}

// RFC 2782
// RFC 3263 section 4.2
func (r *Resolver) srv(ctx context.Context, service, proto, name string, tr Transport) ([]Server, bool, error) {
	_, records, err := r.dns().LookupSRV(ctx, service, proto, name)

	var dnsErr *net.DNSError

	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return nil, false, nil
	case err != nil && len(records) == 0:
		return nil, false, fmt.Errorf("sip: SRV of %s: %w", name, err)
	case len(records) == 0:
		return nil, false, nil
	}

	var out []Server

	for _, rec := range r.order(records) {
		if strings.TrimSuffix(rec.Target, ".") == "" {
			continue
		}

		servers, err := r.addresses(ctx, absolute(rec.Target), tr, rec.Port)
		if err != nil {
			continue
		}

		out = append(out, servers...)
	}

	return out, true, nil
}

// RFC 2782
func (r *Resolver) order(records []*net.SRV) []*net.SRV {
	sorted := slices.Clone(records)
	slices.SortStableFunc(sorted, func(a, b *net.SRV) int { return cmp.Compare(a.Priority, b.Priority) })

	out := make([]*net.SRV, 0, len(sorted))

	for start := 0; start < len(sorted); {
		end := start
		for end < len(sorted) && sorted[end].Priority == sorted[start].Priority {
			end++
		}

		group := slices.Clone(sorted[start:end])
		slices.SortStableFunc(group, func(a, b *net.SRV) int { return cmp.Compare(min(a.Weight, 1), min(b.Weight, 1)) })

		for len(group) > 0 {
			var sum uint64
			for _, rec := range group {
				sum += uint64(rec.Weight)
			}

			pick, running := r.random(sum+1), uint64(0)

			i := slices.IndexFunc(group, func(rec *net.SRV) bool {
				running += uint64(rec.Weight)
				return running >= pick
			})

			out = append(out, group[i])
			group = slices.Delete(group, i, i+1)
		}

		start = end
	}

	return out
}

func (r *Resolver) random(n uint64) uint64 {
	if r.Rand != nil {
		return r.Rand(n)
	}

	return rand.Uint64N(n)
}

func (r *Resolver) addresses(ctx context.Context, host string, tr Transport, port uint16) ([]Server, error) {
	addrs, err := r.dns().LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("sip: addresses of %s: %w", strings.TrimSuffix(host, "."), err)
	}

	out := make([]Server, 0, len(addrs))

	for _, a := range addrs {
		out = append(out, Server{Transport: tr, Addr: netip.AddrPortFrom(a.Unmap(), port)})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("sip: no address for %s", strings.TrimSuffix(host, "."))
	}

	return out, nil
}

func (r *Resolver) dns() DNS {
	if r == nil || r.DNS == nil {
		return system
	}

	return r.DNS
}
