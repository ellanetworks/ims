package sip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// RFC 3263 section 4
type Server struct {
	Transport Transport
	Addr      netip.AddrPort
}

type DNS interface {
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// RFC 3263 section 4
// RFC 3263 section 4.1
type Resolver struct {
	DNS DNS
}

// RFC 3263 section 4.2
// RFC 2782
func (r *Resolver) Resolve(ctx context.Context, u URI) ([]Server, error) {
	if _, ok := u.Addr(); ok {
		tr, dest, err := Destination(u)
		if err != nil {
			return nil, err
		}

		return []Server{{Transport: tr, Addr: dest}}, nil
	}

	if !u.IsSIP() || u.IsSIPS() || u.Host == "" {
		return nil, fmt.Errorf("sip: no destination in %s", u)
	}

	transports := []Transport{UDP, TCP}

	if t, ok := u.Params.Get("transport"); ok {
		tr := Transport(strings.ToUpper(t))
		if tr != UDP && tr != TCP {
			return nil, fmt.Errorf("sip: destination transport %s", t)
		}

		transports = []Transport{tr}
	}

	if u.Port != 0 {
		return r.addresses(ctx, u.Host, transports[0], u.Port)
	}

	for _, tr := range transports {
		servers, err := r.srv(ctx, u.Host, tr)
		if err != nil || len(servers) > 0 {
			return servers, err
		}
	}

	return r.addresses(ctx, u.Host, transports[0], DefaultPort)
}

// RFC 2782
func (r *Resolver) srv(ctx context.Context, domain string, tr Transport) ([]Server, error) {
	_, records, err := r.dns().LookupSRV(ctx, "sip", strings.ToLower(string(tr)), domain)

	var dnsErr *net.DNSError

	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("sip: SRV of %s: %w", domain, err)
	}

	var out []Server

	for _, rec := range records {
		if rec.Target == "." {
			continue
		}

		servers, err := r.addresses(ctx, strings.TrimSuffix(rec.Target, "."), tr, rec.Port)
		if err != nil {
			continue
		}

		out = append(out, servers...)
	}

	if len(out) == 0 && len(records) > 0 {
		return nil, fmt.Errorf("sip: no address for the SRV targets of %s", domain)
	}

	return out, nil
}

func (r *Resolver) addresses(ctx context.Context, host string, tr Transport, port uint16) ([]Server, error) {
	addrs, err := r.dns().LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("sip: addresses of %s: %w", host, err)
	}

	out := make([]Server, 0, len(addrs))

	for _, a := range addrs {
		out = append(out, Server{Transport: tr, Addr: netip.AddrPortFrom(a.Unmap(), port)})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("sip: no address for %s", host)
	}

	return out, nil
}

func (r *Resolver) dns() DNS {
	if r == nil || r.DNS == nil {
		return net.DefaultResolver
	}

	return r.DNS
}
