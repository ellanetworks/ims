package sip

import (
	"slices"
	"testing"
)

func TestParseSecurityMechanism(t *testing.T) {
	for _, tc := range []struct {
		in, out string
		want    SecurityMechanism
	}{
		{
			"ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=6301;port-s=6300;alg=hmac-md5-96;ealg=null", "",
			SecurityMechanism{Name: "ipsec-3gpp", Params: Params{
				{"prot", "esp"},
				{"mod", "trans"},
				{"spi-c", "25656"},
				{"spi-s", "25657"},
				{"port-c", "6301"},
				{"port-s", "6300"},
				{"alg", "hmac-md5-96"},
				{"ealg", "null"},
			}},
		},
		{"ipsec-3gpp ; q=0.1 ;alg=hmac-sha-1-96", "ipsec-3gpp;q=0.1;alg=hmac-sha-1-96", SecurityMechanism{Name: "ipsec-3gpp", Params: Params{{"q", "0.1"}, {"alg", "hmac-sha-1-96"}}}},
		{"sdes-srtp;mediasec", "", SecurityMechanism{Name: "sdes-srtp", Params: Params{{"mediasec", ""}}}},
		{"tls", "", SecurityMechanism{Name: "tls"}},
	} {
		got, err := ParseSecurityMechanism(tc.in)
		if err != nil {
			t.Errorf("ParseSecurityMechanism(%q): %v", tc.in, err)
			continue
		}

		if got.Name != tc.want.Name || !slices.Equal(got.Params, tc.want.Params) {
			t.Errorf("ParseSecurityMechanism(%q) = %+v, want %+v", tc.in, got, tc.want)
		}

		out := tc.out
		if out == "" {
			out = tc.in
		}

		if s := got.String(); s != out {
			t.Errorf("String() = %q, want %q", s, out)
		}
	}
}

func TestParseSecurityMechanismErrors(t *testing.T) {
	for _, in := range []string{"", ";alg=x", "ipsec-3gpp alg=x", `ipsec-3gpp;alg="x`, "ipsec-3gpp;=1"} {
		if m, err := ParseSecurityMechanism(in); err == nil {
			t.Errorf("ParseSecurityMechanism(%q) = %+v, want error", in, m)
		}
	}
}

func TestHeaderSecurityMechanisms(t *testing.T) {
	var h Header

	h.Add("Security-Client", "ipsec-3gpp;alg=hmac-sha-1-96;ealg=aes-cbc, ipsec-3gpp;alg=hmac-sha-1-96;ealg=null")
	h.Add("Via", "SIP/2.0/UDP 10.0.0.1;branch=z9hG4bK1")
	h.Add("security-client", "ipsec-3gpp;alg=hmac-md5-96")

	got, err := h.SecurityMechanisms("Security-Client")
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, m := range got {
		names = append(names, m.String())
	}

	want := []string{
		"ipsec-3gpp;alg=hmac-sha-1-96;ealg=aes-cbc",
		"ipsec-3gpp;alg=hmac-sha-1-96;ealg=null",
		"ipsec-3gpp;alg=hmac-md5-96",
	}
	if !slices.Equal(names, want) {
		t.Errorf("SecurityMechanisms = %q, want %q", names, want)
	}

	if got, err := h.SecurityMechanisms("Security-Verify"); err != nil || got != nil {
		t.Errorf("SecurityMechanisms(absent) = %v, %v", got, err)
	}

	h.Add("Security-Verify", "ipsec-3gpp;alg=a b")

	if _, err := h.SecurityMechanisms("Security-Verify"); err == nil {
		t.Error("SecurityMechanisms(malformed): no error")
	}
}

func TestSecurityMechanismEqual(t *testing.T) {
	m := func(s string) SecurityMechanism {
		t.Helper()

		v, err := ParseSecurityMechanism(s)
		if err != nil {
			t.Fatal(err)
		}

		return v
	}

	a := m("ipsec-3gpp;q=0.1;alg=hmac-sha-1-96;spi-c=100")

	for _, tc := range []struct {
		b    string
		want bool
	}{
		{"ipsec-3gpp;q=0.1;alg=hmac-sha-1-96;spi-c=100", true},
		{"IPSEC-3GPP;spi-c=100;ALG=HMAC-SHA-1-96;q=0.1", true},
		{"ipsec-3gpp;q=0.1;alg=hmac-sha-1-96;spi-c=101", false},
		{"ipsec-3gpp;q=0.1;alg=hmac-sha-1-96", false},
		{"ipsec-3gpp;q=0.1;alg=hmac-sha-1-96;spi-c=100;spi-s=101", false},
		{"tls;q=0.1;alg=hmac-sha-1-96;spi-c=100", false},
	} {
		if got := a.Equal(m(tc.b)); got != tc.want {
			t.Errorf("Equal(%q) = %v, want %v", tc.b, got, tc.want)
		}
	}
}

func TestSecurityMechanismEqualDuplicates(t *testing.T) {
	a := SecurityMechanism{Name: "x", Params: Params{{"p", "1"}, {"p", ""}}}
	b := SecurityMechanism{Name: "x", Params: Params{{"p", ""}, {"p", "1"}}}
	c := SecurityMechanism{Name: "x", Params: Params{{"p", "1"}, {"p", "1"}}}

	if !a.Equal(a) || !a.Equal(b) {
		t.Error("Equal: same parameters differ")
	}

	if a.Equal(c) || c.Equal(a) {
		t.Error("Equal: duplicate parameter matched twice")
	}
}
