package sip

import (
	"slices"
	"testing"
)

func TestParseAuth(t *testing.T) {
	for _, tc := range []struct {
		in, out string
		want    Auth
	}{
		{
			`Digest realm="ims.example.org", nonce="bm9uY2U=", algorithm=AKAv1-MD5, qop="auth", ck="00ff", ik="ff00"`, "",
			Auth{Scheme: "Digest", Params: Params{
				{"realm", `"ims.example.org"`},
				{"nonce", `"bm9uY2U="`},
				{"algorithm", "AKAv1-MD5"},
				{"qop", `"auth"`},
				{"ck", `"00ff"`},
				{"ik", `"ff00"`},
			}},
		},
		{
			`Digest username="001010000000001@ims.example.org", uri="sip:ims.example.org", response="", qop=auth, nc=00000001`, "",
			Auth{Scheme: "Digest", Params: Params{
				{"username", `"001010000000001@ims.example.org"`},
				{"uri", `"sip:ims.example.org"`},
				{"response", `""`},
				{"qop", "auth"},
				{"nc", "00000001"},
			}},
		},
		{
			"Digest\trealm = \"a, b\" ,nonce=x==", `Digest realm="a, b", nonce=x==`,
			Auth{Scheme: "Digest", Params: Params{{"realm", `"a, b"`}, {"nonce", "x=="}}},
		},
		{`Digest a="\"x\""`, "", Auth{Scheme: "Digest", Params: Params{{"a", `"\"x\""`}}}},
		{"Digest", "", Auth{Scheme: "Digest"}},
		{"Digest a=1,, b=2,", "Digest a=1, b=2", Auth{Scheme: "Digest", Params: Params{{"a", "1"}, {"b", "2"}}}},
	} {
		got, err := ParseAuth(tc.in)
		if err != nil {
			t.Errorf("ParseAuth(%q): %v", tc.in, err)
			continue
		}

		if got.Scheme != tc.want.Scheme || !slices.Equal(got.Params, tc.want.Params) {
			t.Errorf("ParseAuth(%q) = %+v, want %+v", tc.in, got, tc.want)
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

func TestParseAuthErrors(t *testing.T) {
	for _, in := range []string{
		"", `"Digest"`, "Digest,a=1", "Digest a", "Digest a=", `Digest a="x`, `Digest a="x"y`, "Digest a=1 b=2", "Digest =1",
	} {
		if a, err := ParseAuth(in); err == nil {
			t.Errorf("ParseAuth(%q) = %+v, want error", in, a)
		}
	}
}

func TestAuthEdit(t *testing.T) {
	a, err := ParseAuth(`Digest realm="r", nonce="n", ck="c", ik="i"`)
	if err != nil {
		t.Fatal(err)
	}

	a.Params.Del("ck")
	a.Params.Del("IK")
	a.Params.Set("integrity-protected", Quote("yes"))
	a.Params.Set("opaque", "")

	if got, want := a.String(), `Digest realm="r", nonce="n", integrity-protected="yes", opaque=""`; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestParseParams(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Params
	}{
		{"icid-value=1234bc9876e;icid-generated-at=192.0.6.8;orig-ioi=home1.net", Params{
			{"icid-value", "1234bc9876e"}, {"icid-generated-at", "192.0.6.8"}, {"orig-ioi", "home1.net"},
		}},
		{` ; a = "x;y" ;b`, Params{{"a", `"x;y"`}, {"b", ""}}},
		{"", nil},
	} {
		got, err := ParseParams(tc.in)
		if err != nil {
			t.Errorf("ParseParams(%q): %v", tc.in, err)
			continue
		}

		if !slices.Equal(got, tc.want) {
			t.Errorf("ParseParams(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	if ps, err := ParseParams("a=1 b"); err == nil {
		t.Errorf("ParseParams(a=1 b) = %v, want error", ps)
	}
}

func TestParseTokenParams(t *testing.T) {
	tok, ps, err := ParseTokenParams(" ipsec-3gpp; alg=hmac-sha-1-96;spi-c=1111;spi-s=2222;port-c=5062;port-s=5064 ")
	if err != nil {
		t.Fatal(err)
	}

	want := Params{{"alg", "hmac-sha-1-96"}, {"spi-c", "1111"}, {"spi-s", "2222"}, {"port-c", "5062"}, {"port-s", "5064"}}
	if tok != "ipsec-3gpp" || !slices.Equal(ps, want) {
		t.Errorf("ParseTokenParams = %q, %v", tok, ps)
	}

	if tok, ps, err := ParseTokenParams("digest"); err != nil || tok != "digest" || ps != nil {
		t.Errorf("ParseTokenParams(digest) = %q, %v, %v", tok, ps, err)
	}

	for _, in := range []string{"", ";alg=x", "ipsec-3gpp alg=x"} {
		if _, _, err := ParseTokenParams(in); err == nil {
			t.Errorf("ParseTokenParams(%q): want error", in)
		}
	}
}
