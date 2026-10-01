package sip

import (
	"net/netip"
	"slices"
	"testing"
)

func TestParseURI(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want URI
	}{
		{"sip:alice@example.com", URI{Scheme: "sip", User: "alice", Host: "example.com"}},
		{"SIP:alice:secret@10.0.0.1:5070", URI{Scheme: "SIP", User: "alice", Password: "secret", Host: "10.0.0.1", Port: 5070}},
		{"sips:[2001:db8::1]:5061;transport=tcp;lr", URI{Scheme: "sips", Host: "[2001:db8::1]", Port: 5061, Params: Params{{"transport", "tcp"}, {"lr", ""}}}},
		{
			"sip:0198765432100;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone",
			URI{Scheme: "sip", User: "0198765432100;phone-context=ims.mnc001.mcc001.3gppnetwork.org", Host: "ims.mnc001.mcc001.3gppnetwork.org", Params: Params{{"user", "phone"}}},
		},
		{"sip:192.168.101.3:5060;alias=192.168.101.3~40762~2;alias=x", URI{Scheme: "sip", Host: "192.168.101.3", Port: 5060, Params: Params{{"alias", "192.168.101.3~40762~2"}, {"alias", "x"}}}},
		{"sip:user@example.com?Route=%3Csip:example.com%3E", URI{Scheme: "sip", User: "user", Host: "example.com", Headers: "Route=%3Csip:example.com%3E"}},
		{"tel:+1-555-123-0001", URI{Scheme: "tel", User: "+1-555-123-0001"}},
		{"tel:0398765432100;phone-context=ims.example.org", URI{Scheme: "tel", User: "0398765432100", Params: Params{{"phone-context", "ims.example.org"}}}},
		{"urn:service:sos", URI{Scheme: "urn", Opaque: "service:sos"}},
		{"sip:pcscf.ims.example.org.;lr", URI{Scheme: "sip", Host: "pcscf.ims.example.org.", Params: Params{{"lr", ""}}}},
		{"tel:*31#", URI{Scheme: "tel", User: "*31#"}},
		{"soap.beep://192.0.2.103:3002", URI{Scheme: "soap.beep", Opaque: "//192.0.2.103:3002"}},
	} {
		got, err := ParseURI(tc.in)
		if err != nil {
			t.Errorf("ParseURI(%q): %v", tc.in, err)
			continue
		}

		if !equalURI(got, tc.want) {
			t.Errorf("ParseURI(%q) = %+v, want %+v", tc.in, got, tc.want)
		}

		if s := got.String(); s != tc.in {
			t.Errorf("String() = %q, want %q", s, tc.in)
		}
	}
}

func equalURI(a, b URI) bool {
	return slices.Equal(a.Params, b.Params) &&
		a.Scheme == b.Scheme && a.User == b.User && a.Password == b.Password && a.Host == b.Host &&
		a.Port == b.Port && a.Headers == b.Headers && a.Opaque == b.Opaque
}

func TestParseURIErrors(t *testing.T) {
	for _, in := range []string{
		"", "sip:", "alice@example.com", "<sip:a@b>", "sip:a@b c", "1sip:a@b",
		"sip:@example.com", "sip:[2001:db8::1", "sip:[10.0.0.1]", "sip:2001:db8::1",
		"sip:a@b:99999", "sip:a@b:x", "sip:a@b;;lr", "sip:a@b?", "tel:", "tel:;x=y",
		"sip:a@<null>", "sip:[fe80::1%eth0]",
		"sip:a[b@host", "sip:a%2@host", "sip:a:p{w@host", "sip:a@-x.example", "sip:a@b..c", "sip:a@.b",
		"tel:+", "tel:12x3", "tel:+1 555",
	} {
		if u, err := ParseURI(in); err == nil {
			t.Errorf("ParseURI(%q) = %+v, want an error", in, u)
		}
	}
}

func TestURIHelpers(t *testing.T) {
	u, err := ParseURI("sip:p@[2001:db8::5]:5060")
	if err != nil {
		t.Fatal(err)
	}

	if a, ok := u.Addr(); !ok || a != netip.MustParseAddr("2001:db8::5") {
		t.Errorf("Addr() = %v, %v", a, ok)
	}

	if got := u.HostPort(); got != "[2001:db8::5]:5060" {
		t.Errorf("HostPort() = %q", got)
	}

	if !u.IsSIP() || u.IsSIPS() || u.IsTel() {
		t.Error("scheme predicates")
	}

	if got := FormatHost(netip.MustParseAddr("2001:db8::5")); got != "[2001:db8::5]" {
		t.Errorf("HostFor = %q", got)
	}

	if got := FormatHost(netip.MustParseAddr("::ffff:10.0.0.1")); got != "10.0.0.1" {
		t.Errorf("HostFor(mapped) = %q", got)
	}

	c := u.Clone()
	c.Params.Set("lr", "")

	if len(u.Params) != 0 {
		t.Error("Clone shares params")
	}
}

func TestParams(t *testing.T) {
	ps := Params{{"Tag", "1"}, {"lr", ""}, {"tag", "2"}}

	if v, ok := ps.Get("tag"); !ok || v != "1" {
		t.Errorf("Get(tag) = %q, %v", v, ok)
	}

	ps.Set("TAG", "3")
	ps.Set("expires", "60")
	ps.Del("lr")

	if got := ps.String(); got != ";Tag=3;tag=2;expires=60" {
		t.Errorf("String() = %q", got)
	}
}

func TestParamsCopyOnWrite(t *testing.T) {
	u, err := ParseURI("sip:b@x;transport=tcp;lr;x=1")
	if err != nil {
		t.Fatal(err)
	}

	const orig = "sip:b@x;transport=tcp;lr;x=1"

	c := u
	c.Params.Del("transport")
	c.Params.Set("lr", "on")
	c.Params.Set("y", "2")

	if u.String() != orig {
		t.Errorf("original changed to %s", u)
	}

	if c.String() != "sip:b@x;lr=on;x=1;y=2" {
		t.Errorf("copy = %s", c)
	}
}
