package as

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

func TestPrepare(t *testing.T) {
	v4, v6 := netip.MustParseAddrPort("192.0.2.1:5060"), netip.MustParseAddrPort("[2001:db8::1]:5060")
	p := Prepare(context.Background(), []netip.AddrPort{v4, v6}, &sip.Resolver{DNS: dns{}})

	for _, tc := range []struct {
		uri  string
		want netip.AddrPort
	}{
		{"sip:a@198.51.100.1:5070", v4},
		{"sip:a@[2001:db8::2];transport=tcp", v6},
		{"sip:scscf.ims.example.com", v4},
	} {
		req := &sip.Request{Method: "OPTIONS", URI: uri(t, tc.uri)}
		if err := p(t.Context(), req); err != nil {
			t.Fatal(err)
		}

		if req.Flow.Local != tc.want {
			t.Errorf("%s: from %s", tc.uri, req.Flow.Local)
		}

		if via, err := req.Header.TopVia(); err != nil || via.Transport != req.Flow.Transport {
			t.Errorf("%s: Via %+v, %v", tc.uri, via, err)
		}
	}

	if err := Prepare(context.Background(), []netip.AddrPort{v4}, &sip.Resolver{DNS: dns{}})(t.Context(), &sip.Request{Method: "OPTIONS", URI: uri(t, "sip:[2001:db8::2]")}); err == nil {
		t.Error("IPv6 next hop from IPv4 only")
	}

	req := &sip.Request{Method: "INVITE", URI: uri(t, "sip:bob@ims.example.com")}
	req.Header.Set("Route", "<sip:scscf.ims.example.com;lr>")

	if err := p(t.Context(), req); err != nil || req.Flow.Remote != netip.MustParseAddrPort("192.0.2.9:6060") || req.Flow.Transport != sip.UDP {
		t.Errorf("to the Route: %+v, %v", req.Flow, err)
	}

	if err := p(t.Context(), &sip.Request{Method: "OPTIONS", URI: uri(t, "sip:unknown.example.com")}); err == nil {
		t.Error("to a domain without records")
	}
}

func TestPrepareLifetime(t *testing.T) {
	life, kill := context.WithCancel(context.Background())
	p := Prepare(life, []netip.AddrPort{netip.MustParseAddrPort("192.0.2.1:5060")}, &sip.Resolver{DNS: unanswered{}})

	kill()

	if err := p(t.Context(), &sip.Request{Method: "BYE", URI: uri(t, "sip:scscf.ims.example.com")}); !errors.Is(err, context.Canceled) {
		t.Errorf("after the lifetime: %v", err)
	}
}

type unanswered struct{}

func (unanswered) LookupNAPTR(ctx context.Context, _ string) ([]sip.NAPTR, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (unanswered) LookupSRV(ctx context.Context, _, _, _ string) (string, []*net.SRV, error) {
	<-ctx.Done()
	return "", nil, ctx.Err()
}

func (unanswered) LookupNetIP(ctx context.Context, _, _ string) ([]netip.Addr, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type dns struct{}

func (dns) LookupNAPTR(_ context.Context, name string) ([]sip.NAPTR, error) {
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (dns) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	if service == "sip" && proto == "udp" && name == "scscf.ims.example.com" {
		return "", []*net.SRV{{Target: "scscf1.ims.example.com.", Port: 6060}}, nil
	}

	return "", nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (dns) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if host == "scscf1.ims.example.com" {
		return []netip.Addr{netip.MustParseAddr("192.0.2.9")}, nil
	}

	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func uri(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}
