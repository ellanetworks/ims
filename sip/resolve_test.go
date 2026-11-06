package sip

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type fakeDNS struct {
	naptr    map[string][]NAPTR
	naptrErr error
	srv      map[string][]*net.SRV
	hosts    map[string][]netip.Addr

	queries []string
}

func (d *fakeDNS) LookupNAPTR(_ context.Context, name string) ([]NAPTR, error) {
	d.queries = append(d.queries, "NAPTR "+name)

	if d.naptrErr != nil {
		return nil, d.naptrErr
	}

	recs, ok := d.naptr[name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}

	return recs, nil
}

func (d *fakeDNS) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	q := name
	if service != "" || proto != "" {
		q = "_" + service + "._" + proto + "." + name
	}

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
				"_sips._tcp.tls.example.com": {{Target: "as1.example.com.", Port: 5091}},
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
		{uri: "sip:10.0.0.7:5090;transport=tcp", want: []string{"TCP 10.0.0.7:5090"}},
		{uri: "sip:tcp.example.com:5099", want: []string{"UDP 10.0.0.9:5099"}, queries: []string{"A tcp.example.com"}},
		{
			uri: "sip:tcp.example.com", want: []string{"TCP 10.0.0.2:5070", "TCP 10.0.0.1:5071", "TCP [2001:db8::1]:5071"},
			queries: []string{"NAPTR tcp.example.com", "SRV _sip._udp.tcp.example.com", "SRV _sip._tcp.tcp.example.com", "A as2.example.com", "A as1.example.com"},
		},
		{uri: "sip:as@udp.example.com", want: []string{"UDP 10.0.0.1:5080", "UDP [2001:db8::1]:5080"}},
		{uri: "sip:udp.example.com;transport=tcp", bad: true},
		{
			uri: "sip:plain.example.com", want: []string{"UDP 10.0.0.3:5060"},
			queries: []string{"NAPTR plain.example.com", "SRV _sip._udp.plain.example.com", "SRV _sip._tcp.plain.example.com", "A plain.example.com"},
		},
		{uri: "sip:down.example.com", bad: true},
		{
			uri: "sips:as1.example.com", want: []string{"TLS 10.0.0.1:5061", "TLS [2001:db8::1]:5061"},
			queries: []string{"NAPTR as1.example.com", "SRV _sips._tcp.as1.example.com", "A as1.example.com"},
		},
		{uri: "sip:as1.example.com;transport=sctp", bad: true},
		{uri: "sip:10.0.0.7;transport=tls", want: []string{"TLS 10.0.0.7:5061"}},
		{uri: "sip:tls.example.com;transport=TLS", want: []string{"TLS 10.0.0.1:5091", "TLS [2001:db8::1]:5091"}, queries: []string{"SRV _sips._tcp.tls.example.com", "A as1.example.com"}},
		{uri: "sip:plain.example.com;transport=tls", want: []string{"TLS 10.0.0.3:5061"}, queries: []string{"SRV _sips._tcp.plain.example.com", "A plain.example.com"}},
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

func naptrZone() *fakeDNS {
	return &fakeDNS{
		naptr: map[string][]NAPTR{
			"carrier.example.com": {
				{Order: 90, Preference: 60, Flags: "s", Service: "SIP+D2U", Replacement: "_sip._udp.carrier.example.com."},
				{Order: 50, Preference: 50, Flags: "S", Service: "SIPS+D2T", Replacement: "_sips._tcp.carrier.example.com."},
				{Order: 90, Preference: 50, Flags: "s", Service: "sip+d2t", Replacement: "_sip._tcp.sbc.example.net."},
				{Order: 10, Preference: 10, Flags: "s", Service: "SIP+D2S", Replacement: "_sip._sctp.carrier.example.com."},
				{Order: 10, Preference: 10, Flags: "u", Service: "SIP+D2U", Replacement: "_sip._udp.bad.example.com."},
				{Order: 10, Preference: 10, Flags: "", Service: "SIP+D2U", Replacement: "_sip._udp.bad.example.com."},
				{Order: 10, Preference: 10, Flags: "s", Service: "SIP+D2U", Regexp: "!^.*$!sip:bad@example.com!", Replacement: "."},
				{Order: 10, Preference: 10, Flags: "s", Service: "SIP+D2U", Replacement: "."},
				{Order: 10, Preference: 10, Flags: "s", Service: "SIPS+D2U", Replacement: "_sips._udp.carrier.example.com."},
				{Order: 10, Preference: 10, Flags: "s", Service: "E2U+sip", Replacement: "_sip._udp.bad.example.com."},
			},
			"sctp.example.com": {
				{Order: 10, Preference: 10, Flags: "s", Service: "SIP+D2S", Replacement: "_sip._sctp.sctp.example.com."},
			},
			"failover.example.com": {
				{Order: 30, Preference: 10, Flags: "s", Service: "SIP+D2U", Replacement: "_sip._udp.carrier.example.com."},
				{Order: 10, Preference: 10, Flags: "s", Service: "SIP+D2T", Replacement: "_sip._tcp.gone.example.com."},
				{Order: 10, Preference: 20, Flags: "s", Service: "SIP+D2T", Replacement: "_sip._tcp.root.example.com."},
				{Order: 20, Preference: 10, Flags: "s", Service: "SIP+D2U", Replacement: "_sip._udp.missing.example.com."},
			},
			"dead.example.com": {
				{Order: 10, Preference: 10, Flags: "s", Service: "SIP+D2U", Replacement: "_sip._udp.dead.example.com."},
			},
		},
		srv: map[string][]*net.SRV{
			"_sip._udp.carrier.example.com":  {{Target: "sbc1.example.com.", Port: 5060}},
			"_sips._tcp.carrier.example.com": {{Target: "sbc1.example.com.", Port: 5061}},
			"_sip._tcp.sbc.example.net":      {{Target: "sbc2.example.net.", Port: 5070}, {Target: ".", Port: 0}, {Target: "nowhere.example.net.", Port: 5070}},
			"_sip._sctp.sctp.example.com":    {{Target: "sbc1.example.com.", Port: 5060}},
			"_sip._udp.sctp.example.com":     {{Target: "sbc1.example.com.", Port: 5080}},
			"_sip._udp.dead.example.com":     {{Target: ".", Port: 0}},
			"_sips._tcp.tls.example.com":     {{Target: "sbc1.example.com.", Port: 5061}},
			"_sip._udp.tls.example.com":      {{Target: "sbc1.example.com.", Port: 5062}},
			"_sip._tcp.tls.example.com":      {{Target: "sbc1.example.com.", Port: 5063}},
			"_sip._tcp.gone.example.com":     {{Target: "gone.example.com.", Port: 5060}},
			"_sip._tcp.root.example.com":     {{Target: ".", Port: 0}},
			"_sip._tcp.dead.example.com":     {{Target: "sbc2.example.net.", Port: 5090}},
		},
		hosts: map[string][]netip.Addr{
			"sbc1.example.com":    {netip.MustParseAddr("192.0.2.1")},
			"sbc2.example.net":    {netip.MustParseAddr("192.0.2.2"), netip.MustParseAddr("2001:db8::2")},
			"carrier.example.com": {netip.MustParseAddr("192.0.2.9")},
			"sctp.example.com":    {netip.MustParseAddr("192.0.2.8")},
		},
	}
}

func TestResolveNAPTR(t *testing.T) {
	all := []Transport{UDP, TCP, TLS}

	for _, tt := range []struct {
		name       string
		uri        string
		transports []Transport
		naptrErr   error
		want       []string
		queries    []string
		bad        bool
	}{
		{
			name: "order and preference without TLS", uri: "sip:+15551230001@carrier.example.com;user=phone",
			want: []string{"TCP 192.0.2.2:5070", "TCP [2001:db8::2]:5070"},
			queries: []string{
				"NAPTR carrier.example.com",
				"SRV _sip._tcp.sbc.example.net", "A sbc2.example.net", "A nowhere.example.net",
			},
		},
		{
			name: "order and preference with TLS", uri: "sip:carrier.example.com", transports: all,
			want: []string{"TLS 192.0.2.1:5061"},
		},
		{
			name: "next NAPTR when the preferred one has no target", uri: "sip:failover.example.com",
			want: []string{"UDP 192.0.2.1:5060"},
			queries: []string{
				"NAPTR failover.example.com",
				"SRV _sip._tcp.gone.example.com", "A gone.example.com",
				"SRV _sip._tcp.root.example.com",
				"SRV _sip._udp.missing.example.com",
				"SRV _sip._udp.carrier.example.com", "A sbc1.example.com",
			},
		},
		{
			name: "maddr is the target", uri: "sip:+15551230001@ignored.example.org;maddr=carrier.example.com;transport=udp",
			want:    []string{"UDP 192.0.2.1:5060"},
			queries: []string{"SRV _sip._udp.carrier.example.com", "A sbc1.example.com"},
		},
		{name: "maddr IP", uri: "sip:ignored.example.org:5070;maddr=192.0.2.44", want: []string{"UDP 192.0.2.44:5070"}, queries: []string{}},
		{
			name: "UDP only", uri: "sip:carrier.example.com", transports: []Transport{UDP},
			want:    []string{"UDP 192.0.2.1:5060"},
			queries: []string{"NAPTR carrier.example.com", "SRV _sip._udp.carrier.example.com", "A sbc1.example.com"},
		},
		{
			name: "sips keeps SIPS services only", uri: "sips:carrier.example.com",
			want:    []string{"TLS 192.0.2.1:5061"},
			queries: []string{"NAPTR carrier.example.com", "SRV _sips._tcp.carrier.example.com", "A sbc1.example.com"},
		},
		{
			name: "transport given skips NAPTR", uri: "sip:carrier.example.com;transport=udp",
			want:    []string{"UDP 192.0.2.1:5060"},
			queries: []string{"SRV _sip._udp.carrier.example.com", "A sbc1.example.com"},
		},
		{
			name: "port given skips NAPTR and SRV", uri: "sip:carrier.example.com:5099",
			want:    []string{"UDP 192.0.2.9:5099"},
			queries: []string{"A carrier.example.com"},
		},
		{
			name: "sips port given", uri: "sips:carrier.example.com:5099;transport=tcp",
			want: []string{"TLS 192.0.2.9:5099"},
		},
		{name: "sips over UDP", uri: "sips:carrier.example.com;transport=udp", bad: true},
		{name: "sips IP", uri: "sips:[2001:db8::7]", want: []string{"TLS [2001:db8::7]:5061"}, queries: []string{}},
		{name: "IP", uri: "sip:192.0.2.7", want: []string{"UDP 192.0.2.7:5060"}, queries: []string{}},
		{
			name: "no usable NAPTR falls back to SRV", uri: "sip:sctp.example.com",
			want: []string{"UDP 192.0.2.1:5080"},
			queries: []string{
				"NAPTR sctp.example.com", "SRV _sip._udp.sctp.example.com", "A sbc1.example.com", "SRV _sip._tcp.sctp.example.com",
			},
		},
		{
			name: "no NAPTR with TLS queries _sips first", uri: "sip:tls.example.com", transports: all,
			want: []string{"TLS 192.0.2.1:5061", "UDP 192.0.2.1:5062", "TCP 192.0.2.1:5063"},
			queries: []string{
				"NAPTR tls.example.com",
				"SRV _sips._tcp.tls.example.com", "A sbc1.example.com",
				"SRV _sip._udp.tls.example.com", "A sbc1.example.com",
				"SRV _sip._tcp.tls.example.com", "A sbc1.example.com",
			},
		},
		{
			name: "no NAPTR without TLS skips _sips", uri: "sip:tls.example.com",
			want: []string{"UDP 192.0.2.1:5062", "TCP 192.0.2.1:5063"},
			queries: []string{
				"NAPTR tls.example.com",
				"SRV _sip._udp.tls.example.com", "A sbc1.example.com",
				"SRV _sip._tcp.tls.example.com", "A sbc1.example.com",
			},
		},
		{
			name: "NAPTR failure falls back to SRV", uri: "sip:sctp.example.com", naptrErr: errors.New("SERVFAIL"),
			want: []string{"UDP 192.0.2.1:5080"},
		},
		{
			name: "NAPTR without service falls back to SRV then A", uri: "sip:dead.example.com",
			want: []string{"TCP 192.0.2.2:5090", "TCP [2001:db8::2]:5090"},
			queries: []string{
				"NAPTR dead.example.com", "SRV _sip._udp.dead.example.com",
				"SRV _sip._udp.dead.example.com", "SRV _sip._tcp.dead.example.com", "A sbc2.example.net",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, err := ParseURI(tt.uri)
			if err != nil {
				t.Fatal(err)
			}

			d := naptrZone()
			d.naptrErr = tt.naptrErr
			r := &Resolver{DNS: d, Transports: tt.transports}

			servers, err := r.Resolve(t.Context(), u)
			if tt.bad {
				if err == nil {
					t.Fatalf("Resolve = %v, want an error", servers)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			got := []string{}
			for _, s := range servers {
				got = append(got, string(s.Transport)+" "+s.Addr.String())
			}

			if !slices.Equal(got, tt.want) {
				t.Errorf("Resolve = %v, want %v", got, tt.want)
			}

			wantDomain := strings.TrimSuffix(u.Host, ".")
			if maddr, ok := u.Params.Get("maddr"); ok {
				wantDomain = maddr
			}

			if _, ok := HostAddr(wantDomain); ok {
				wantDomain = ""
			}

			for _, s := range servers {
				if s.Domain != wantDomain {
					t.Errorf("Domain of %v = %q, want %q", s.Addr, s.Domain, wantDomain)
				}
			}

			if tt.queries != nil && !slices.Equal(append([]string{}, d.queries...), tt.queries) {
				t.Errorf("queries %v, want %v", d.queries, tt.queries)
			}
		})
	}
}

func TestResolveCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	d := naptrZone()
	d.naptrErr = context.Canceled

	if _, err := (&Resolver{DNS: d}).Resolve(ctx, URI{Scheme: "sip", Host: "carrier.example.com"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve = %v, want context.Canceled", err)
	}

	if len(d.queries) != 1 {
		t.Errorf("queries %v after cancel", d.queries)
	}
}

func TestOrderSRV(t *testing.T) {
	records := []*net.SRV{
		{Target: "backup.", Priority: 20, Weight: 0},
		{Target: "light.", Priority: 10, Weight: 1},
		{Target: "heavy.", Priority: 10, Weight: 3},
		{Target: "idle.", Priority: 10, Weight: 0},
	}

	seeded := func(seed uint64) *Resolver {
		return &Resolver{Rand: rand.New(rand.NewPCG(seed, seed)).Uint64N}
	}

	targets := func(recs []*net.SRV) []string {
		var out []string
		for _, rec := range recs {
			out = append(out, rec.Target)
		}

		return out
	}

	a, b := targets(seeded(7).order(records)), targets(seeded(7).order(records))
	if !slices.Equal(a, b) {
		t.Errorf("same seed: %v and %v", a, b)
	}

	first := map[string]int{}

	r := seeded(42)
	for range 4000 {
		got := targets(r.order(records))
		if len(got) != 4 || got[3] != "backup." {
			t.Fatalf("order = %v, want priority 20 last", got)
		}

		first[got[0]]++
	}

	if first["heavy."] < 2200 || first["heavy."] > 2600 || first["light."] < 650 || first["light."] > 950 || first["idle."] < 650 || first["idle."] > 950 {
		t.Errorf("first picks %v, want about 2400 heavy, 800 light and 800 idle", first)
	}

	zero := []*net.SRV{{Target: "a.", Weight: 0}, {Target: "b.", Weight: 0}}
	if got := targets(seeded(1).order(zero)); !slices.Equal(got, []string{"a.", "b."}) {
		t.Errorf("zero weights: %v", got)
	}
}

func encodeNAPTR(rec NAPTR) []byte {
	b := binary.BigEndian.AppendUint16(nil, rec.Order)
	b = binary.BigEndian.AppendUint16(b, rec.Preference)

	for _, s := range []string{rec.Flags, rec.Service, rec.Regexp} {
		b = append(append(b, byte(len(s))), s...)
	}

	for label := range strings.SplitSeq(strings.TrimSuffix(rec.Replacement, "."), ".") {
		if label != "" {
			b = append(append(b, byte(len(label))), label...)
		}
	}

	return append(b, 0)
}

func TestDecodeNAPTR(t *testing.T) {
	rec := NAPTR{Order: 100, Preference: 10, Flags: "s", Service: "SIP+D2U", Replacement: "_sip._udp.example.com."}

	got, ok := decodeNAPTR(encodeNAPTR(rec))
	if !ok || got != rec {
		t.Fatalf("decodeNAPTR = %+v %v, want %+v", got, ok, rec)
	}

	root := NAPTR{Flags: "s", Service: "SIP+D2T", Replacement: "."}
	if got, ok := decodeNAPTR(encodeNAPTR(root)); !ok || got != root {
		t.Errorf("decodeNAPTR = %+v %v, want %+v", got, ok, root)
	}

	full := encodeNAPTR(rec)

	for _, bad := range [][]byte{
		nil,
		full[:3],
		full[:6],
		full[:len(full)-1],
		append(slices.Clone(full), 0),
		append(slices.Clone(full[:len(full)-1-len("com")-1]), 0xc0, 0x0c),
	} {
		if got, ok := decodeNAPTR(bad); ok {
			t.Errorf("decodeNAPTR(%x) = %+v, want malformed", bad, got)
		}
	}
}

type dnsServer struct {
	addr      netip.AddrPort
	records   []NAPTR
	malformed bool
	truncate  bool
	rcode     dnsmessage.RCode
	tcp       atomic.Int32
}

func newDNSServer(t *testing.T, s *dnsServer) *dnsServer {
	t.Helper()

	var (
		pc net.PacketConn
		ln net.Listener
	)

	var lc net.ListenConfig

	for range 20 {
		var err error

		pc, err = lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}

		ln, err = lc.Listen(t.Context(), "tcp", pc.LocalAddr().String())
		if err == nil {
			break
		}

		_ = pc.Close()
		pc = nil
	}

	if pc == nil {
		t.Fatal("no port for UDP and TCP")
	}

	s.addr = netip.MustParseAddrPort(pc.LocalAddr().String())

	t.Cleanup(func() {
		_ = pc.Close()
		_ = ln.Close()
	})

	go func() {
		buf := make([]byte, 4096)

		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}

			_, _ = pc.WriteTo(s.answer(buf[:n], s.truncate), from)
		}
	}()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}

			s.tcp.Add(1)

			var size [2]byte
			if _, err := io.ReadFull(c, size[:]); err == nil {
				q := make([]byte, binary.BigEndian.Uint16(size[:]))
				if _, err := io.ReadFull(c, q); err == nil {
					a := s.answer(q, false)
					_, _ = c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(a))), a...))
				}
			}

			_ = c.Close()
		}
	}()

	return s
}

func (s *dnsServer) answer(query []byte, truncate bool) []byte {
	var p dnsmessage.Parser

	h, err := p.Start(query)
	if err != nil {
		return nil
	}

	q, err := p.Question()
	if err != nil {
		return nil
	}

	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Truncated: truncate, RCode: s.rcode})
	_ = b.StartQuestions()
	_ = b.Question(q)
	_ = b.StartAnswers()

	if !truncate {
		_ = b.CNAMEResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.CNAMEResource{CNAME: q.Name})

		for _, rec := range s.records {
			data := encodeNAPTR(rec)
			if s.malformed {
				data = data[:5]
			}

			_ = b.UnknownResource(dnsmessage.ResourceHeader{Name: q.Name, Type: typeNAPTR, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.UnknownResource{Type: typeNAPTR, Data: data})
		}
	}

	out, _ := b.Finish()

	return out
}

func TestSystemNAPTR(t *testing.T) {
	recs := []NAPTR{
		{Order: 10, Preference: 10, Flags: "s", Service: "SIP+D2T", Replacement: "_sip._tcp.example.com."},
		{Order: 20, Preference: 10, Flags: "s", Service: "SIP+D2U", Replacement: "_sip._udp.example.com."},
	}

	for _, tt := range []struct {
		name      string
		truncate  bool
		malformed bool
		rcode     dnsmessage.RCode
		want      []NAPTR
		notFound  bool
		tcp       int32
	}{
		{name: "UDP", want: recs},
		{name: "truncated", truncate: true, want: recs, tcp: 1},
		{name: "NXDOMAIN", rcode: dnsmessage.RCodeNameError, notFound: true},
		{name: "malformed", malformed: true, notFound: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newDNSServer(t, &dnsServer{records: recs, truncate: tt.truncate, malformed: tt.malformed, rcode: tt.rcode})

			d := systemDNS{conf: "/nonexistent", servers: []netip.AddrPort{s.addr}}

			got, err := d.LookupNAPTR(t.Context(), "example.com")

			var dnsErr *net.DNSError
			if tt.notFound {
				if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
					t.Fatalf("LookupNAPTR = %v, %v, want not found", got, err)
				}

				return
			}

			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("LookupNAPTR = %+v, %v, want %+v", got, err, tt.want)
			}

			if n := s.tcp.Load(); n != tt.tcp {
				t.Errorf("%d TCP queries, want %d", n, tt.tcp)
			}
		})
	}
}

func TestSystemNAPTRServerFailure(t *testing.T) {
	failing := newDNSServer(t, &dnsServer{rcode: dnsmessage.RCodeServerFailure})
	working := newDNSServer(t, &dnsServer{records: []NAPTR{{Order: 1, Flags: "s", Service: "SIP+D2U", Replacement: "_sip._udp.example.com."}}})

	d := systemDNS{conf: "/nonexistent", servers: []netip.AddrPort{failing.addr, working.addr}}

	got, err := d.LookupNAPTR(t.Context(), "example.com.")
	if err != nil || !slices.Equal(got, working.records) {
		t.Fatalf("LookupNAPTR = %+v, %v", got, err)
	}
}

func TestReadResolvConf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")

	conf := "# comment\nsearch example.com\nnameserver 192.0.2.53\nnameserver bogus\nnameserver 2001:db8::53\noptions ndots:2 timeout:3 attempts:9 rotate\n"
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}

	c := readResolvConf(path)

	want := []netip.AddrPort{netip.MustParseAddrPort("192.0.2.53:53"), netip.MustParseAddrPort("[2001:db8::53]:53")}
	if !slices.Equal(c.servers, want) || c.timeout != 3*time.Second || c.attempts != 5 {
		t.Errorf("readResolvConf = %+v", c)
	}

	if c := readResolvConf(filepath.Join(t.TempDir(), "missing")); len(c.servers) != 2 || c.timeout != dnsTimeout || c.attempts != dnsAttempts {
		t.Errorf("readResolvConf without a file = %+v", c)
	}
}
