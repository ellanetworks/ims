package trust

import (
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

func TestTrusted(t *testing.T) {
	d := New(
		[]netip.Addr{netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("2001:db8::5")},
		[]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
	)

	tests := []struct {
		addr string
		want bool
	}{
		{"10.0.0.5", true},
		{"::ffff:10.0.0.5", true},
		{"2001:db8::5", true},
		{"192.0.2.77", true},
		{"10.0.0.6", false},
		{"2001:db8::6", false},
		{"198.51.100.1", false},
	}

	for _, tt := range tests {
		if got := d.Trusted(netip.MustParseAddr(tt.addr)); got != tt.want {
			t.Errorf("Trusted(%s) = %t, want %t", tt.addr, got, tt.want)
		}
	}
}

func TestStripRequest(t *testing.T) {
	uri, err := sip.ParseURI("sip:+15551230002;cpc=ordinary;phone-context=x;oli=1@ims.example.org;user=phone;iotl=homea-homeb")
	if err != nil {
		t.Fatal(err)
	}

	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("P-Asserted-Identity", "<sip:alice@ims.example.org>")
	req.Header.Add("P-Served-User", "<sip:alice@ims.example.org>")
	req.Header.Add("Feature-Caps", "*;+g.3gpp.icsi-ref")
	req.Header.Add("P-Charging-Vector", "icid-value=1")

	StripRequest(req)

	for _, name := range []string{"P-Asserted-Identity", "P-Served-User", "Feature-Caps"} {
		if req.Header.Has(name) {
			t.Errorf("%s kept", name)
		}
	}

	if !req.Header.Has("P-Charging-Vector") {
		t.Error("P-Charging-Vector removed")
	}

	if want := "sip:+15551230002;phone-context=x@ims.example.org;user=phone"; req.URI.String() != want {
		t.Errorf("Request-URI = %s, want %s", req.URI, want)
	}

	tel, _ := sip.ParseURI("tel:+15551230002;cpc=test;oli=0;phone-context=x")
	req.URI = tel

	StripRequest(req)

	if want := "tel:+15551230002;phone-context=x"; req.URI.String() != want {
		t.Errorf("Request-URI = %s, want %s", req.URI, want)
	}
}

func TestStripResponse(t *testing.T) {
	req := sip.NewRequest("INVITE", sip.URI{Scheme: "sip", Host: "ims.example.org"})
	res := sip.NewResponse(req, 486, "")
	res.Header.Add("Reason", "Q.850;cause=17")
	res.Header.Add("P-Charging-Function-Addresses", "ccf=192.0.2.10")
	res.Header.Add("P-Asserted-Identity", "<sip:bob@ims.example.org>")
	res.Header.Add("Response-Source", "<urn:3gpp:fe:s-cscf>")

	StripResponse(res)

	for _, name := range []string{"Reason", "P-Charging-Function-Addresses", "P-Asserted-Identity", "Response-Source"} {
		if res.Header.Has(name) {
			t.Errorf("%s kept", name)
		}
	}
}
