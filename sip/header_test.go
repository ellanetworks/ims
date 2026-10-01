package sip

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseAddress(t *testing.T) {
	for _, tc := range []struct {
		in      string
		display string
		uri     string
		params  string
	}{
		{"<sip:a@b>", "", "sip:a@b", ""},
		{"sip:a@b;tag=1", "", "sip:a@b", ";tag=1"},
		{"sip:a@b ;  tag = 1", "", "sip:a@b", ";tag=1"},
		{`"Doe, J" <sip:j@x;lr>;expires=60`, `"Doe, J"`, "sip:j@x;lr", ";expires=60"},
		{`"J Rosenberg \\\""       <sip:jdrosen@example.com> ; tag = 98asjd8`, `"J Rosenberg \\\""`, "sip:jdrosen@example.com", ";tag=98asjd8"},
		{"caller<sip:caller@example.com>;tag=323", "caller", "sip:caller@example.com", ";tag=323"},
		{"Bob Smith <tel:+15551230001>", "Bob Smith", "tel:+15551230001", ""},
		{`<sip:192.168.101.3:5060;alias=192.168.101.3~40762~2>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call`, "", "sip:192.168.101.3:5060;alias=192.168.101.3~40762~2", `;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call`},
		{"isbn:2983792873", "", "isbn:2983792873", ""},
		{`sip:ue@1.2.3.4;+sip.instance="<urn:gsma:imei:1>";expires=`, "", "sip:ue@1.2.3.4", `;+sip.instance="<urn:gsma:imei:1>";expires`},
		{"<sip:a@b;>;tag=1;", "", "sip:a@b", ";tag=1"},
	} {
		a, err := ParseAddress(tc.in)
		if err != nil {
			t.Errorf("ParseAddress(%q): %v", tc.in, err)
			continue
		}

		if a.Display != tc.display || a.URI.String() != tc.uri || a.Params.String() != tc.params {
			t.Errorf("ParseAddress(%q) = %q %q %q", tc.in, a.Display, a.URI, a.Params)
		}

		again, err := ParseAddress(a.String())
		if err != nil || again.String() != a.String() {
			t.Errorf("String() %q does not round-trip: %v", a.String(), err)
		}
	}
}

func TestParseAddressErrors(t *testing.T) {
	for _, in := range []string{
		`"Mr. J. User <sip:j.user@example.com>`,
		"Bell, Alexander <sip:a.g.bell@example.com>;tag=43",
		"<sip:a@b",
		`"x" sip:a@b`,
		"<sip:a@b>;;;",
		"sip:user;x=y@host",
		"<sip:a@b> junk",
		"",
	} {
		if a, err := ParseAddress(in); err == nil {
			t.Errorf("ParseAddress(%q) = %+v, want an error", in, a)
		}
	}
}

func TestParseAddressList(t *testing.T) {
	as, err := ParseAddressList(" * ")
	if err != nil || len(as) != 1 || !as[0].Star || as[0].String() != "*" {
		t.Errorf("ParseAddressList(*) = %+v, %v", as, err)
	}

	if as, err := ParseAddressList("*, <sip:a@b>"); err == nil {
		t.Errorf("ParseAddressList(*, <sip:a@b>) = %+v", as)
	}

	if as, err := ParseAddressList(", <sip:p1;lr>,"); err != nil || len(as) != 1 {
		t.Errorf("empty elements: %+v, %v", as, err)
	}
}

func TestParseVia(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		host     string
		port     uint16
	}{
		{"SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK1;rport", "SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK1;rport", "10.0.0.1", 5060},
		{"SIP  /   2.0 /UDP 192.0.2.2;branch=390skdjuw", "SIP/2.0/UDP 192.0.2.2;branch=390skdjuw", "192.0.2.2", 0},
		{"SIP  / 2.0  / TCP     spindle.example.com   ; branch  =   z9hG4bK9ikj8", "SIP/2.0/TCP spindle.example.com;branch=z9hG4bK9ikj8", "spindle.example.com", 0},
		{"SIP/2.0/TCP [2001:db8::1] : 5060 ;received=2001:db8::9", "SIP/2.0/TCP [2001:db8::1]:5060;received=2001:db8::9", "[2001:db8::1]", 5060},
	} {
		v, err := ParseVia(tc.in)
		if err != nil {
			t.Errorf("ParseVia(%q): %v", tc.in, err)
			continue
		}

		if v.String() != tc.want || v.Host != tc.host || v.Port != tc.port {
			t.Errorf("ParseVia(%q) = %q (%q, %d)", tc.in, v, v.Host, v.Port)
		}
	}

	for _, in := range []string{
		"SIP/2.0/UDP", "SIP/2.0 10.0.0.1", "SIP/2.0/UDP 10.0.0.1;;", "SIP/2.0/UDP 192.0.2.15;;,;,,",
		"SIP/2.0/UDP 10.0.0.1:99999", "SIP/2.0/UDP10.0.0.1", "SIP/2.0/UDP 10.0.0.1 junk",
	} {
		if v, err := ParseVia(in); err == nil {
			t.Errorf("ParseVia(%q) = %q, want an error", in, v)
		}
	}
}

func TestViaParams(t *testing.T) {
	v, err := ParseVia("SIP/2.0/UDP h:5060;rport=40762;received=10.0.0.9;branch=z9hG4bKx")
	if err != nil {
		t.Fatal(err)
	}

	if p, ok := v.RPort(); !ok || p != 40762 {
		t.Errorf("RPort() = %d, %v", p, ok)
	}

	if v.Received() != "10.0.0.9" || v.Branch() != "z9hG4bKx" || v.SentBy() != "h:5060" {
		t.Errorf("params: %q %q %q", v.Received(), v.Branch(), v.SentBy())
	}

	v, _ = ParseVia("SIP/2.0/UDP h;rport")
	if p, ok := v.RPort(); !ok || p != 0 {
		t.Errorf("RPort() flag = %d, %v", p, ok)
	}
}

func TestParseCSeq(t *testing.T) {
	for in, want := range map[string]CSeq{
		"1 INVITE":         {1, "INVITE"},
		"0009 \t INVITE":   {9, "INVITE"},
		"2147483647 PRACK": {1<<31 - 1, "PRACK"},
		"3000000000 BYE":   {3000000000, "BYE"},
		"4294967295 BYE":   {1<<32 - 1, "BYE"},
	} {
		if got, err := ParseCSeq(in); err != nil || got != want {
			t.Errorf("ParseCSeq(%q) = %v, %v", in, got, err)
		}
	}

	for _, in := range []string{"INVITE", "1", "4294967296 INVITE", "-1 INVITE", "1 IN<VITE", "36893488147419103232 REGISTER"} {
		if got, err := ParseCSeq(in); err == nil {
			t.Errorf("ParseCSeq(%q) = %v, want an error", in, got)
		}
	}
}

func TestFields(t *testing.T) {
	fs := Header{
		{Name: "v", Value: "SIP/2.0/UDP a;branch=z9hG4bK1, SIP/2.0/UDP b;branch=z9hG4bK2"},
		{Name: "Via", Value: "SIP/2.0/UDP c;branch=z9hG4bK3"},
		{Name: "k", Value: "timer"},
		{Name: "Supported", Value: "100rel"},
		{Name: "Max-Forwards", Value: "70"},
		{Name: "m", Value: "<sip:a@b>, \"x, y\" <sip:c@d>"},
	}

	if got := fs.Values("SUPPORTED"); !slices.Equal(got, []string{"timer", "100rel"}) {
		t.Errorf("Values(Supported) = %q", got)
	}

	if got := fs.Values("k"); len(got) != 2 {
		t.Errorf("Values(k) = %q", got)
	}

	if cs, err := fs.Contacts(); err != nil || len(cs) != 2 || cs[1].DisplayName() != "x, y" {
		t.Errorf("Contacts() = %v, %v", cs, err)
	}

	if mf, err := fs.MaxForwards(); err != nil || mf != 70 {
		t.Errorf("MaxForwards() = %d, %v", mf, err)
	}

	if _, err := fs.Expires(); !errors.Is(err, ErrMissingHeader) {
		t.Errorf("Expires() error = %v", err)
	}

	top, ok := fs.PopFirst("Via")
	if !ok || top != "SIP/2.0/UDP a;branch=z9hG4bK1" || fs[0].Value != "SIP/2.0/UDP b;branch=z9hG4bK2" {
		t.Fatalf("RemoveFirstValue = %q, %v; first field %q", top, ok, fs[0].Value)
	}

	fs.PopFirst("Via")

	route := Header{{Name: "Route", Value: ", "}, {Name: "Route", Value: " , <sip:p1;lr>, <sip:p2;lr>"}}
	if top, ok := route.PopFirst("Route"); !ok || top != "<sip:p1;lr>" || route.Get("Route") != "<sip:p2;lr>" {
		t.Errorf("RemoveFirstValue over empty elements = %q, %v; left %v", top, ok, route)
	}

	if v, err := fs.TopVia(); err != nil || v.Host != "c" {
		t.Errorf("TopVia() = %v, %v", v, err)
	}

	fs.Prepend("Record-Route", "<sip:p;lr>")
	fs.Set("supported", "path")

	if fs[0].Name != "Record-Route" || !slices.Equal(fs.Values("k"), []string{"path"}) || fs[2].Name != "k" {
		t.Errorf("after Prepend and Set: %v", fs)
	}

	if n := fs.Del("Via"); n != 1 || fs.Has("v") {
		t.Errorf("Remove(Via) = %d", n)
	}

	if LongName("m") != "Contact" || LongName("Q") != "Q" || LongName("x") != "Session-Expires" {
		t.Error("LongName")
	}
}

func TestSplitList(t *testing.T) {
	got := SplitList(`"a, b" <sip:x>;p="1,2", <sip:y,z>,,tel:+1`)
	want := []string{`"a, b" <sip:x>;p="1,2"`, "<sip:y,z>", "", "tel:+1"}

	if !slices.Equal(got, want) {
		t.Errorf("SplitList = %q", got)
	}
}

func TestQuote(t *testing.T) {
	if q := Quote(`a "b" \c`); q != `"a \"b\" \\c"` || Unquote(q) != `a "b" \c` {
		t.Errorf("Quote = %s, Unquote = %s", q, Unquote(q))
	}
}

func TestSetTopVia(t *testing.T) {
	m, err := Parse([]byte("OPTIONS sip:a@b SIP/2.0\r\nX: 1\r\nv: SIP/2.0/UDP a:1;branch=z9hG4bK1 , SIP/2.0/UDP b;branch=z9hG4bK0\r\n" +
		"Via:  SIP/2.0/UDP c;branch=z9hG4bK9\r\nl: 0\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	h := &m.Env().Header

	top, err := h.TopVia()
	if err != nil {
		t.Fatal(err)
	}

	top.Transport = TCP
	top.Params.Set("received", "10.0.0.1")

	if err := h.SetTopVia(top); err != nil {
		t.Fatal(err)
	}

	want := "OPTIONS sip:a@b SIP/2.0\r\nX: 1\r\nv: SIP/2.0/TCP a:1;branch=z9hG4bK1;received=10.0.0.1, SIP/2.0/UDP b;branch=z9hG4bK0\r\n" +
		"Via:  SIP/2.0/UDP c;branch=z9hG4bK9\r\nl: 0\r\n\r\n"
	if got := m.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}

	var empty Header
	if err := empty.SetTopVia(top); !errors.Is(err, ErrMissingHeader) {
		t.Errorf("no Via: err = %v", err)
	}
}

func TestViaAddr(t *testing.T) {
	for in, want := range map[string]string{
		"SIP/2.0/UDP 10.0.0.1:5060": "10.0.0.1",
		"SIP/2.0/UDP [2001:db8::1]": "2001:db8::1",
		"SIP/2.0/UDP host.example":  "",
	} {
		v, err := ParseVia(in)
		if err != nil {
			t.Fatal(err)
		}

		a, ok := v.Addr()
		if got := map[bool]string{true: a.String(), false: ""}[ok]; got != want {
			t.Errorf("%s: Addr() = %q, want %q", in, got, want)
		}
	}
}

func TestHeaderInsert(t *testing.T) {
	var h Header

	h.Insert("A", "1")
	h.Add("Content-Length", "0")
	h.Insert("B", "2")

	var names []string
	for _, f := range h {
		names = append(names, f.Name)
	}

	if got := strings.Join(names, ","); got != "A,B,Content-Length" {
		t.Errorf("fields %s, want A,B,Content-Length", got)
	}
}
