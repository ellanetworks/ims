package as

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

// A request goes from the address of its next hop's family.
func TestPrepare(t *testing.T) {
	v4, v6 := netip.MustParseAddrPort("192.0.2.1:5060"), netip.MustParseAddrPort("[2001:db8::1]:5060")
	p := Prepare(context.Background(), []netip.AddrPort{v4, v6}, &sip.Resolver{DNS: dns{}})

	for _, tc := range []struct {
		uri  string
		want netip.AddrPort
	}{
		{"sip:a@198.51.100.1:5070", v4},
		{"sip:a@[2001:db8::2];transport=tcp", v6},
		// RFC 3263 §4.2: by the SRV records of a domain name, an S-CSCF's of the third-party REGISTERs.
		{"sip:scscf.ims.example.com", v4},
	} {
		req := &sip.Request{Method: "OPTIONS", URI: uri(t, tc.uri)}
		if err := p(req); err != nil {
			t.Fatal(err)
		}

		if req.Flow.Local != tc.want {
			t.Errorf("%s: from %s", tc.uri, req.Flow.Local)
		}

		if via, err := req.Header.TopVia(); err != nil || via.Transport != req.Flow.Transport {
			t.Errorf("%s: Via %+v, %v", tc.uri, via, err)
		}
	}

	if err := Prepare(context.Background(), []netip.AddrPort{v4}, &sip.Resolver{DNS: dns{}})(&sip.Request{Method: "OPTIONS", URI: uri(t, "sip:[2001:db8::2]")}); err == nil {
		t.Error("IPv6 next hop from IPv4 only")
	}

	// The top Route, a domain name, before the Request-URI.
	req := &sip.Request{Method: "INVITE", URI: uri(t, "sip:bob@ims.example.com")}
	req.Header.Set("Route", "<sip:scscf.ims.example.com;lr>")

	if err := p(req); err != nil || req.Flow.Remote != netip.MustParseAddrPort("192.0.2.9:6060") || req.Flow.Transport != sip.UDP {
		t.Errorf("to the Route: %+v, %v", req.Flow, err)
	}

	if err := p(&sip.Request{Method: "OPTIONS", URI: uri(t, "sip:unknown.example.com")}); err == nil {
		t.Error("to a domain without records")
	}
}

// The DNS queries of a next hop end with the lifetime of the server: after the deadline of its shutdown, a BYE to a
// next hop named by a domain name fails at once, whatever the DNS.
func TestPrepareLifetime(t *testing.T) {
	life, kill := context.WithCancel(context.Background())
	p := Prepare(life, []netip.AddrPort{netip.MustParseAddrPort("192.0.2.1:5060")}, &sip.Resolver{DNS: unanswered{}})

	kill()

	if err := p(&sip.Request{Method: "BYE", URI: uri(t, "sip:scscf.ims.example.com")}); !errors.Is(err, context.Canceled) {
		t.Errorf("after the lifetime: %v", err)
	}
}

// unanswered is a DNS that answers no query: each fails once its ctx is done, as one unreachable does at its
// timeout.
type unanswered struct{}

func (unanswered) LookupSRV(ctx context.Context, _, _, _ string) (string, []*net.SRV, error) {
	<-ctx.Done()
	return "", nil, ctx.Err()
}

func (unanswered) LookupNetIP(ctx context.Context, _, _ string) ([]netip.Addr, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// dns has scscf.ims.example.com at 192.0.2.9, port 6060 over UDP by its SRV record.
type dns struct{}

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
