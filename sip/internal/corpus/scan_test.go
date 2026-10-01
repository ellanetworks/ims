package corpus

import (
	"slices"
	"testing"
)

func TestScan(t *testing.T) {
	raw := []byte("SIP/2.0 401 Unauthorized\r\n" +
		"v: SIP/2.0/UDP a;branch=z9hG4bK1, SIP/2.0/UDP b;branch=z9hG4bK2\r\n" +
		"WWW-Authenticate: Digest realm=\"x\",\r\n   nonce=\"n\"\r\n" +
		"P-Asserted-Identity: \"Doe, J\" <sip:j@x>, <tel:+1555>\r\n" +
		"l: 4\r\n\r\nbodyEXTRA")

	s, ok := scan(raw)
	if !ok {
		t.Fatal("Scan failed")
	}

	if got := s.values("Via"); len(got) != 2 {
		t.Errorf("Via = %q", got)
	}

	if got := s.values("WWW-Authenticate"); !slices.Equal(got, []string{`Digest realm="x", nonce="n"`}) {
		t.Errorf("WWW-Authenticate = %q", got)
	}

	if got := s.values("P-Asserted-Identity"); !slices.Equal(got, []string{`"Doe, J" <sip:j@x>`, "<tel:+1555>"}) {
		t.Errorf("P-Asserted-Identity = %q", got)
	}

	if string(s.Body) != "body" {
		t.Errorf("body = %q", s.Body)
	}
}

func TestReferenceURI(t *testing.T) {
	tests := []struct {
		in   string
		want refURI
	}{
		{"tel:+15551230001", refURI{Scheme: "tel", User: "+15551230001"}},
		{"tel:0398765432100;phone-context=ims.example.org", refURI{Scheme: "tel", User: "0398765432100"}},
		{"sip:0398765432100;phone-context=x@ims.example.org;user=phone", refURI{Scheme: "sip", User: "0398765432100;phone-context=x", Host: "ims.example.org"}},
		{"sip:ue@[2001:db8::5]:5060;lr", refURI{Scheme: "sip", User: "ue", Host: "[2001:db8::5]", Port: 5060}},
		{"sip:10.0.0.1", refURI{Scheme: "sip", Host: "10.0.0.1"}},
	}

	for _, tt := range tests {
		tt.want.Text = tt.in
		if got := referenceURI(tt.in); got != tt.want {
			t.Errorf("ReferenceURI(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
