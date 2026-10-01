package transport

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

func viaRequest(t *testing.T, via string, src string) *sip.Request {
	t.Helper()

	raw := "OPTIONS sip:a@b SIP/2.0\r\n" + via + "\r\nVia: SIP/2.0/UDP 10.0.0.9;branch=z9hG4bK2\r\n" +
		"From: <sip:a@b>;tag=1\r\nTo: <sip:a@b>\r\nCall-ID: c\r\nCSeq: 1 OPTIONS\r\nContent-Length: 0\r\n\r\n"

	m, err := sip.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}

	r := m.(*sip.Request)
	r.Flow.Remote = netip.MustParseAddrPort(src)

	return r
}

func TestStampVia(t *testing.T) {
	for _, tc := range []struct {
		name, via, src, want string
	}{
		{
			"same address, no rport: unchanged",
			"Via:  SIP/2.0/UDP 10.0.0.1:5060 ;branch=z9hG4bK1",
			"10.0.0.1:5060",
			"Via:  SIP/2.0/UDP 10.0.0.1:5060 ;branch=z9hG4bK1",
		},
		{
			"different address",
			"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK1",
			"10.0.0.2:5060",
			"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK1;received=10.0.0.2",
		},
		{
			"host name",
			"Via: SIP/2.0/UDP pcscf.example.com;branch=z9hG4bK1",
			"10.0.0.2:5060",
			"Via: SIP/2.0/UDP pcscf.example.com;branch=z9hG4bK1;received=10.0.0.2",
		},
		{
			"rport filled, received added even for the same address",
			"Via: SIP/2.0/UDP 192.168.101.3:6400;branch=z9hG4bK1;rport",
			"192.168.101.3:6401",
			"Via: SIP/2.0/UDP 192.168.101.3:6400;branch=z9hG4bK1;rport=6401;received=192.168.101.3",
		},
		{
			"compact name and list kept",
			"v: SIP/2.0/TCP 10.0.0.1:5060;rport;branch=z9hG4bK1, SIP/2.0/UDP 10.0.0.8;branch=z9hG4bK0",
			"10.0.0.1:40000",
			"v: SIP/2.0/TCP 10.0.0.1:5060;rport=40000;branch=z9hG4bK1;received=10.0.0.1, SIP/2.0/UDP 10.0.0.8;branch=z9hG4bK0",
		},
		{
			"stale received replaced",
			"Via: SIP/2.0/UDP 10.0.0.1:5060;received=192.0.2.66;branch=z9hG4bK1",
			"10.0.0.1:5060",
			"Via: SIP/2.0/UDP 10.0.0.1:5060;received=10.0.0.1;branch=z9hG4bK1",
		},
		{
			"IPv6",
			"Via: SIP/2.0/UDP [2001:db8::1]:5060;branch=z9hG4bK1;rport",
			"[2001:db8::2]:5070",
			"Via: SIP/2.0/UDP [2001:db8::1]:5060;branch=z9hG4bK1;rport=5070;received=2001:db8::2",
		},
	} {
		r := viaRequest(t, tc.via, tc.src)
		if err := stampVia(r); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if got := strings.Split(r.String(), "\r\n")[1]; got != tc.want {
			t.Errorf("%s:\ngot  %s\nwant %s", tc.name, got, tc.want)
		}

		if err := r.Validate(); err != nil {
			t.Errorf("%s: stamped request invalid: %v", tc.name, err)
		}

		if got := r.Header.Count("Via"); got != 2 {
			t.Errorf("%s: %d Via fields", tc.name, got)
		}
	}
}

func TestStampViaUnusable(t *testing.T) {
	r := viaRequest(t, "Via: garbage", "10.0.0.1:5060")
	if err := stampVia(r); err == nil {
		t.Error("no error for an unparsable top Via")
	}
}

func TestDestinations(t *testing.T) {
	for _, tc := range []struct {
		via, udp, tcp string
	}{
		{"SIP/2.0/UDP 10.0.0.1:6400;received=10.0.0.2;rport=6401", "10.0.0.2:6401", "10.0.0.2:6400"},
		{"SIP/2.0/UDP 10.0.0.1:6400;received=10.0.0.2", "10.0.0.2:6400", "10.0.0.2:6400"},
		{"SIP/2.0/UDP 10.0.0.1", "10.0.0.1:5060", "10.0.0.1:5060"},
		{"SIP/2.0/UDP 10.0.0.1;rport", "10.0.0.1:5060", "10.0.0.1:5060"},
		{"SIP/2.0/UDP [2001:db8::1]:5062;received=[2001:db8::2];rport=7000", "[2001:db8::2]:7000", "[2001:db8::2]:5062"},
		{"SIP/2.0/UDP host.example.com;received=10.0.0.3", "10.0.0.3:5060", "10.0.0.3:5060"},
		{"SIP/2.0/UDP host.example.com", "", ""},
		{"SIP/2.0/UDP 10.0.0.1;received=junk", "", ""},
	} {
		via, err := sip.ParseVia(tc.via)
		if err != nil {
			t.Fatal(err)
		}

		check := func(kind string, got netip.AddrPort, err error, want string) {
			switch {
			case want == "" && err == nil:
				t.Errorf("%s %s: got %s, want an error", kind, tc.via, got)
			case want != "" && (err != nil || got.String() != want):
				t.Errorf("%s %s: got %s, %v, want %s", kind, tc.via, got, err, want)
			}
		}

		got, err := udpDestination(via)
		check("UDP", got, err, tc.udp)

		got, err = tcpDestination(via)
		check("TCP", got, err, tc.tcp)
	}
}
