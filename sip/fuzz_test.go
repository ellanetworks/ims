package sip_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/internal/corpus"
)

func addCorpus(f *testing.F) {
	for _, fx := range corpus.Corpus() {
		f.Add(fx.Raw)
	}

	for _, tc := range corpus.TortureCases() {
		raw, err := corpus.TortureMessage(tc.Name)
		if err != nil {
			f.Fatal(err)
		}

		f.Add(raw)
	}
}

func checkStable(t *testing.T, m sip.Message) {
	t.Helper()

	out := m.Bytes()

	again, err := sip.Parse(out)
	if err != nil {
		t.Fatalf("serialization %q does not parse: %v", out, err)
	}

	if got := again.Bytes(); !bytes.Equal(got, out) {
		t.Fatalf("unstable serialization:\n%q\n%q", out, got)
	}

	h := again.Env().Header
	_, _ = h.Vias()
	_, _ = h.CSeq()
	_, _ = h.From()
	_, _ = h.To()
	_, _ = h.Contacts()
	_ = again.Validate()
}

func FuzzParse(f *testing.F) {
	addCorpus(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := sip.Parse(data)
		if err != nil {
			var perr *sip.ParseError
			if errors.As(err, &perr) && perr.Request != nil {
				_ = sip.NewResponse(perr.Request, 400, "").Bytes()
			}

			return
		}

		for bytes.HasPrefix(data, []byte("\r\n")) {
			data = data[2:]
		}

		end := bytes.Index(data, []byte("\r\n\r\n")) + 4 + len(m.Env().Body)
		if got := m.Bytes(); !bytes.Equal(got, data[:end]) {
			t.Fatalf("not lossless:\n%q\n%q", data[:end], got)
		}

		checkStable(t, m)
	})
}

func FuzzStream(f *testing.F) {
	addCorpus(f)
	f.Add([]byte("\r\n\r\nOPTIONS sip:a@b SIP/2.0\r\nl: 1\r\n\r\nx\r\nSIP/2.0 200 OK\r\nl: 0\r\n\r\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		r := sip.NewStreamReader(bytes.NewReader(data), 4096)

		for range 64 {
			m, _, err := r.Next()

			var perr *sip.ParseError

			switch {
			case errors.As(err, &perr):
				continue
			case errors.Is(err, io.EOF), err != nil:
				return
			case m == nil:
				continue
			}

			checkStable(t, m)

			m2, _, err := sip.NewStreamReader(bytes.NewReader(m.Bytes()), 0).Next()
			if err != nil || !bytes.Equal(m2.Bytes(), m.Bytes()) {
				t.Fatalf("stream message %q does not read back: %v", m.Bytes(), err)
			}
		}
	})
}

func FuzzURI(f *testing.F) {
	for _, s := range []string{
		"sip:alice@example.com", "sips:[2001:db8::1]:5061;transport=tcp;lr",
		"sip:0198765432100;phone-context=ims.example.org@ims.example.org;user=phone",
		"tel:+1-555-123-0001;phone-context=x", "urn:service:sos", "sip:a:b@h:1?x=y",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		u, err := sip.ParseURI(s)
		if err != nil {
			return
		}

		out := u.String()

		again, err := sip.ParseURI(out)
		if err != nil {
			t.Fatalf("%q serializes to %q, which does not parse: %v", s, out, err)
		}

		if again.String() != out {
			t.Fatalf("unstable URI: %q, %q", out, again.String())
		}
	})
}

func FuzzAddress(f *testing.F) {
	for _, s := range []string{
		`"Doe, J" <sip:j@x;lr>;expires=60`, "sip:a@b;tag=1", "*",
		`<sip:ue@[2001:db8::1]>;+sip.instance="<urn:gsma:imei:1>";reg-id=1`,
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		a, err := sip.ParseAddress(s)
		if err != nil {
			return
		}

		out := a.String()

		again, err := sip.ParseAddress(out)
		if err != nil {
			t.Fatalf("%q serializes to %q, which does not parse: %v", s, out, err)
		}

		if again.String() != out {
			t.Fatalf("unstable address: %q, %q", out, again.String())
		}
	})
}

func FuzzVia(f *testing.F) {
	for _, s := range []string{
		"SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK1;rport",
		"SIP / 2.0 / TCP [2001:db8::1] ; received=2001:db8::9",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		v, err := sip.ParseVia(s)
		if err != nil {
			return
		}

		out := v.String()

		again, err := sip.ParseVia(out)
		if err != nil || again.String() != out {
			t.Fatalf("%q serializes to %q, which does not round-trip: %v", s, out, err)
		}
	})
}

func FuzzAuth(f *testing.F) {
	for _, s := range []string{
		`Digest realm="ims.example.org", nonce="bm9uY2U=", algorithm=AKAv1-MD5, qop="auth", ck="00ff", ik="ff00"`,
		`Digest username="a@b", uri="sip:b", response="", qop=auth, nc=00000001, integrity-protected="yes"`,
		"Digest\trealm = \"a, b\" ,nonce=x==",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		a, err := sip.ParseAuth(s)
		if err != nil {
			return
		}

		out := a.String()

		again, err := sip.ParseAuth(out)
		if err != nil || again.String() != out {
			t.Fatalf("%q serializes to %q, which does not round-trip: %v", s, out, err)
		}
	})
}
