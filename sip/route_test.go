package sip

import (
	"net/netip"
	"testing"
)

func TestNextHop(t *testing.T) {
	cases := []struct {
		uri, route string
		tr         Transport
		addr       string
		bad        bool
	}{
		{uri: "sip:b@10.0.0.2", tr: UDP, addr: "10.0.0.2:5060"},
		{uri: "sip:b@10.0.0.2:5070;transport=TCP", tr: TCP, addr: "10.0.0.2:5070"},
		{uri: "sip:b@10.0.0.2", route: "<sip:[2001:db8::1]:5080;lr;transport=tcp>, <sip:x;lr>", tr: TCP, addr: "[2001:db8::1]:5080"},
		{uri: "sip:b@example.com", bad: true},
		{uri: "tel:+15551234", bad: true},
		{uri: "sips:b@10.0.0.2", bad: true},
		{uri: "sip:b@10.0.0.2;transport=sctp", bad: true},
		{uri: "sip:b@10.0.0.2", route: "<sip:p.example;lr>", bad: true},
	}

	for _, c := range cases {
		u, err := ParseURI(c.uri)
		if err != nil {
			t.Fatal(err)
		}

		r := NewRequest("OPTIONS", u)
		if c.route != "" {
			r.Header.Add("Route", c.route)
		}

		tr, addr, err := NextHop(r)

		switch {
		case c.bad && err == nil:
			t.Errorf("%s %s: next hop %s %s, want an error", c.uri, c.route, tr, addr)
		case !c.bad && err != nil:
			t.Errorf("%s %s: %v", c.uri, c.route, err)
		case !c.bad && (tr != c.tr || addr != netip.MustParseAddrPort(c.addr)):
			t.Errorf("%s %s: next hop %s %s, want %s %s", c.uri, c.route, tr, addr, c.tr, c.addr)
		}
	}
}

func TestNewVia(t *testing.T) {
	v := NewVia(TCP, netip.MustParseAddrPort("[2001:db8::1]:5060"))
	if v.SentBy() != "[2001:db8::1]:5060" || v.Transport != TCP || len(v.Branch()) <= len(MagicCookie) {
		t.Errorf("NewVia = %s", v)
	}
}

func TestRAckAndRSeq(t *testing.T) {
	var h Header

	h.Add("RSeq", " 988789 ")
	h.Add("RAck", "776656 1 INVITE")

	if n, err := h.RSeq(); err != nil || n != 988789 {
		t.Errorf("RSeq = %d, %v", n, err)
	}

	r, err := h.RAck()
	if err != nil || r != (RAck{RSeq: 776656, CSeq: 1, Method: "INVITE"}) || r.String() != "776656 1 INVITE" {
		t.Errorf("RAck = %+v, %v", r, err)
	}

	for _, bad := range []string{"", "1 INVITE", "0 1 INVITE", "1 x INVITE", "1 2 INV@TE", "4294967296 1 INVITE"} {
		if _, err := ParseRAck(bad); err == nil {
			t.Errorf("ParseRAck(%q) succeeded", bad)
		}
	}

	h.Set("RSeq", "0")

	if _, err := h.RSeq(); err == nil {
		t.Error("RSeq 0 accepted")
	}
}
