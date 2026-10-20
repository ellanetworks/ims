package sip

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"testing"
)

type fakeDNS struct {
	srv   map[string][]*net.SRV
	hosts map[string][]netip.Addr

	queries []string
}

func (d *fakeDNS) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	q := "_" + service + "._" + proto + "." + name
	d.queries = append(d.queries, "SRV "+q)

	recs, ok := d.srv[q]
	if !ok {
		return "", nil, &net.DNSError{Err: "no such host", Name: q, IsNotFound: true}
	}

	return q, recs, nil
}

func (d *fakeDNS) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	d.queries = append(d.queries, "A "+host)

	addrs, ok := d.hosts[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}

	return addrs, nil
}

func TestResolve(t *testing.T) {
	dns := func() *fakeDNS {
		return &fakeDNS{
			srv: map[string][]*net.SRV{
				"_sip._tcp.tcp.example.com":  {{Target: "as2.example.com.", Port: 5070}, {Target: "as1.example.com.", Port: 5071}},
				"_sip._udp.udp.example.com":  {{Target: "as1.example.com.", Port: 5080}},
				"_sip._udp.down.example.com": {{Target: ".", Port: 0}},
			},
			hosts: map[string][]netip.Addr{
				"as1.example.com":   {netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("2001:db8::1")},
				"as2.example.com":   {netip.MustParseAddr("::ffff:10.0.0.2")},
				"tcp.example.com":   {netip.MustParseAddr("10.0.0.9")},
				"plain.example.com": {netip.MustParseAddr("10.0.0.3")},
			},
		}
	}

	for _, tt := range []struct {
		uri     string
		want    []string
		queries []string
		bad     bool
	}{
		// RFC 3263 §4.2: an IP address needs no DNS.
		{uri: "sip:10.0.0.7:5090;transport=tcp", want: []string{"TCP 10.0.0.7:5090"}},
		// RFC 3263 §4.2: a port skips SRV.
		{uri: "sip:tcp.example.com:5099", want: []string{"UDP 10.0.0.9:5099"}, queries: []string{"A tcp.example.com"}},
		// RFC 3263 §4.1: no NAPTR, the SRV records of UDP then TCP.
		{
			uri: "sip:tcp.example.com", want: []string{"TCP 10.0.0.2:5070", "TCP 10.0.0.1:5071", "TCP [2001:db8::1]:5071"},
			queries: []string{"SRV _sip._udp.tcp.example.com", "SRV _sip._tcp.tcp.example.com", "A as2.example.com", "A as1.example.com"},
		},
		{uri: "sip:as@udp.example.com", want: []string{"UDP 10.0.0.1:5080", "UDP [2001:db8::1]:5080"}},
		// RFC 3263 §4.2: the transport of the URI, its SRV records only.
		{uri: "sip:udp.example.com;transport=tcp", bad: true},
		// RFC 3263 §4.2: no SRV record, the address records at the default port.
		{
			uri: "sip:plain.example.com", want: []string{"UDP 10.0.0.3:5060"},
			queries: []string{"SRV _sip._udp.plain.example.com", "SRV _sip._tcp.plain.example.com", "A plain.example.com"},
		},
		// RFC 2782: a target of "." means no service.
		{uri: "sip:down.example.com", bad: true},
		{uri: "sips:as1.example.com", bad: true},
		{uri: "sip:as1.example.com;transport=sctp", bad: true},
		{uri: "tel:+15551230001", bad: true},
	} {
		t.Run(tt.uri, func(t *testing.T) {
			u, err := ParseURI(tt.uri)
			if err != nil {
				t.Fatal(err)
			}

			d := dns()
			r := &Resolver{DNS: d}

			servers, err := r.Resolve(context.Background(), u)
			if tt.bad {
				if err == nil {
					t.Fatalf("Resolve = %v, want an error", servers)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			var got []string
			for _, s := range servers {
				got = append(got, string(s.Transport)+" "+s.Addr.String())
			}

			if !slices.Equal(got, tt.want) {
				t.Errorf("Resolve = %v, want %v", got, tt.want)
			}

			if tt.queries != nil && !slices.Equal(d.queries, tt.queries) {
				t.Errorf("queries %v, want %v", d.queries, tt.queries)
			}
		})
	}
}
