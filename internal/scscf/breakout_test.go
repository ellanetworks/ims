package scscf

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/bgcf"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
)

const (
	quiet     = 50 * time.Millisecond
	offNetTel = "tel:+16617629239"
)

func routedBGCF(prefix string) *bgcf.BGCF {
	return bgcfWith(prefix, true)
}

func bgcfWith(prefix string, enabled bool) *bgcf.BGCF {
	s := settings.Settings{
		Trunks:    []settings.Trunk{{ID: "carrier", Name: "Carrier", Enabled: enabled}},
		SIPRoutes: []settings.SIPRoute{{ID: "r", Prefix: prefix, Trunks: []string{"carrier"}}},
	}

	return bgcf.New(func() settings.Settings { return s }, nil)
}

func TestBreakoutToTheIBCF(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{"tel URI", offNetTel + ";cpc=ordinary"},
		{"SIP URI with user=phone", "sip:+16617629239@" + homeDomain + ";user=phone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := newSessionHarness(t)
			sh.bgcf = routedBGCF("+1661")
			sh.restart()

			req := sh.originating("INVITE", tt.target, "<"+testMSISDN+">")
			req.Header.Add("P-Charging-Vector", `icid-value=1234;access-network-charging-info="x";orig-ioi=home`)
			sh.orig.Send(sip.UDP, sh.scscf, req)

			got, f := sh.ibcf.RecvRequest()

			routes, _ := got.Header.Routes()

			switch rr, _ := got.Header.RecordRoutes(); {
			case got.URI.String() != tt.target:
				t.Fatalf("Request-URI = %s, want %s unmodified", got.URI, tt.target)
			case len(routes) != 1 || routes[0].URI.HostPort() != sh.ibcf.Addr().String() || !routes[0].URI.IsLooseRouter():
				t.Fatalf("Route = %v, want the IBCF alone", got.Header.Values("Route"))
			case len(rr) != 1 || rr[0].URI.User != rrOriginating:
				t.Fatalf("Record-Route = %v, want the S-CSCF's", got.Header.Values("Record-Route"))
			case got.Header.Get("P-Charging-Vector") != "icid-value=1234;orig-ioi=home":
				t.Fatalf("P-Charging-Vector = %q, want it without access-network-charging-info", got.Header.Get("P-Charging-Vector"))
			case got.Header.Has("P-Served-User"):
				t.Fatal("P-Served-User forwarded to the IBCF")
			}

			if q := sh.directory.queries(); !slices.Equal(q, []string{tt.target}) {
				t.Fatalf("translations %v, want %s", q, tt.target)
			}

			reply(t, sh.ibcf, got, f, 486, "carrier")

			if res := final(t, sh.orig); res.StatusCode != 486 {
				t.Fatalf("caller got %q, want 486", res.StartLine())
			}

			sh.icscf.RecvNone(quiet)
		})
	}
}

func TestNoBreakout(t *testing.T) {
	tests := []struct {
		name      string
		bgcf      *bgcf.BGCF
		method    string
		target    string
		known     bool
		err       error
		translate bool
	}{
		{name: "number in the HSS", bgcf: routedBGCF("+1"), target: offNetTel, known: true, translate: true},
		{name: "registered number", bgcf: routedBGCF("+1"), target: testTel},
		{name: "translation failed", bgcf: routedBGCF("+1"), target: offNetTel, err: errors.New("HSS down"), translate: true},
		{name: "no SIP route", bgcf: routedBGCF("+44"), target: offNetTel},
		{name: "no BGCF", target: offNetTel},
		{name: "short number under a country code", bgcf: routedBGCF("+1"), target: "tel:+1911"},
		{name: "SIP URI", bgcf: routedBGCF("+1"), target: "sip:+16617629239@" + homeDomain},
		{name: "MESSAGE", bgcf: routedBGCF("+1"), method: "MESSAGE", target: offNetTel},
		{name: "SUBSCRIBE", bgcf: routedBGCF("+1"), method: "SUBSCRIBE", target: offNetTel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := newSessionHarness(t)
			sh.bgcf = tt.bgcf
			sh.directory.known = map[string]bool{tt.target: tt.known}
			sh.directory.err = tt.err
			sh.restart()

			method := tt.method
			if method == "" {
				method = "INVITE"
			}

			sh.orig.Send(sip.UDP, sh.scscf, sh.originating(method, tt.target, "<"+testMSISDN+">"))

			if got, _ := sh.icscf.RecvRequest(); got.Method != method {
				t.Fatalf("I-CSCF got %s, want %s", got.Method, method)
			}

			sh.ibcf.RecvNone(quiet)

			if q := sh.directory.queries(); tt.translate != (len(q) > 0) {
				t.Fatalf("translations %v, want any %v", q, tt.translate)
			}
		})
	}
}

func TestBreakoutWithoutUsableTrunk(t *testing.T) {
	sh := newSessionHarness(t)
	sh.bgcf = bgcfWith("+1", false)
	sh.restart()

	sh.orig.Send(sip.UDP, sh.scscf, sh.originating("INVITE", offNetTel, "<"+testMSISDN+">"))

	if res := final(t, sh.orig); res.StatusCode != 503 {
		t.Fatalf("got %q, want 503", res.StartLine())
	}

	sh.icscf.RecvNone(quiet)
	sh.ibcf.RecvNone(quiet)
}

func TestBreakoutOfUnusableTrunkForAKnownNumber(t *testing.T) {
	sh := newSessionHarness(t)
	sh.bgcf = bgcfWith("+1", false)
	sh.directory.known = map[string]bool{offNetTel: true}
	sh.restart()

	sh.orig.Send(sip.UDP, sh.scscf, sh.originating("INVITE", offNetTel, "<"+testMSISDN+">"))

	if got, _ := sh.icscf.RecvRequest(); got.URI.String() != offNetTel {
		t.Fatalf("I-CSCF got %s, want %s", got.URI, offNetTel)
	}
}

func TestWithoutAccessNetworkChargingInfo(t *testing.T) {
	for in, want := range map[string]string{
		"icid-value=1;access-network-charging-info=x;orig-ioi=a": "icid-value=1;orig-ioi=a",
		"icid-value=1": "icid-value=1",
		"icid-value=1; Access-Network-Charging-Info=\"a;b\"": "icid-value=1",
	} {
		var h sip.Header
		h.Set("P-Charging-Vector", in)
		withoutAccessNetworkChargingInfo(&h)

		if got := h.Get("P-Charging-Vector"); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
