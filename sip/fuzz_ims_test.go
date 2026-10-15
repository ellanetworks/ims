package sip_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
)

func FuzzReason(f *testing.F) {
	for _, s := range []string{
		`SIP;cause=200;text="Call completed elsewhere"`,
		`Q.850 ; cause=16 ; text="a \"b\""`,
		"RELEASE_CAUSE;cause=3;x=y",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		r, err := sip.ParseReason(s)
		if err != nil {
			return
		}

		out := r.String()
		checkText(t, out)

		again, err := sip.ParseReason(out)
		if err != nil || again.String() != out || again.Text() != r.Text() {
			t.Fatalf("%q serializes to %q, which does not round-trip: %v", s, out, err)
		}

		c1, ok1 := r.Cause()
		if c2, ok2 := again.Cause(); c1 != c2 || ok1 != ok2 {
			t.Fatalf("%q: cause %d/%v became %d/%v", s, c1, ok1, c2, ok2)
		}

		n, err := sip.NewReason(r.Protocol, c1, r.Text())
		if err != nil {
			t.Fatalf("NewReason(%q, %d, %q): %v", r.Protocol, c1, r.Text(), err)
		}

		if back, err := sip.ParseReason(n.String()); err != nil || back.Text() != r.Text() {
			t.Fatalf("NewReason(%q, %d, %q) = %q does not round-trip: %v", r.Protocol, c1, r.Text(), n, err)
		}
	})
}

func FuzzWarning(f *testing.F) {
	for _, s := range []string{
		`304 pcscf.ims.example.org "Media type not available"`,
		`301 [2001:db8::1]:5060 "x\"y"`,
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		w, err := sip.ParseWarning(s)
		if err != nil {
			return
		}

		out := w.String()
		checkText(t, out)

		again, err := sip.ParseWarning(out)
		if err != nil || again != w {
			t.Fatalf("%q serializes to %q, which does not round-trip: %v", s, out, err)
		}

		if _, err := sip.NewWarning(w.Code, w.Agent); err != nil {
			t.Fatalf("parsed warn-agent %q rejected by NewWarning: %v", w.Agent, err)
		}
	})
}

func FuzzPrivacy(f *testing.F) {
	for _, s := range []string{"id", "header;id;critical", "user, id", ""} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		p, err := sip.ParsePrivacy(s)
		if err != nil {
			return
		}

		again, err := sip.ParsePrivacy(p.String())
		if err != nil || !slices.Equal(again, p) {
			t.Fatalf("%q serializes to %q, which does not round-trip: %v", s, p, err)
		}
	})
}

func FuzzEarlyMedia(f *testing.F) {
	for _, s := range []string{"supported", "inactive, gated", "sendrecv,x"} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		em, err := sip.ParseEarlyMedia(s)
		if err != nil {
			return
		}

		again, err := sip.ParseEarlyMedia(em.String())
		if err != nil || !slices.Equal(again, em) {
			t.Fatalf("%q serializes to %q, which does not round-trip: %v", s, em, err)
		}
	})
}

func checkText(t *testing.T, s string) {
	t.Helper()

	if strings.ContainsFunc(s, func(r rune) bool { return r < ' ' && r != '\t' || r == 0x7f }) {
		t.Fatalf("%q contains a control character", s)
	}
}

func FuzzMultipart(f *testing.F) {
	for _, s := range []struct{ contentType, body string }{
		{`multipart/mixed;boundary="boundary1"`, "--boundary1\r\nContent-Type: application/sdp\r\n\r\nv=0\r\n\r\n" +
			"--boundary1\r\nContent-Type: application/vnd.3gpp.mcptt-info+xml\r\nContent-ID: <a@b>\r\n\r\n<mcpttinfo/>\r\n" +
			"--boundary1--\r\n"},
		{"multipart/mixed;boundary=b", "preamble\r\n--b \r\n\r\nx\r\n--b\r\nContent-Type: multipart/mixed;boundary=c\r\n\r\n" +
			"--c\r\nContent-Type: text/plain\r\n\r\ny\r\n--c--\r\n--b--\r\nepilogue"},
	} {
		f.Add(s.contentType, []byte(s.body))
	}

	f.Fuzz(func(t *testing.T, contentType string, body []byte) {
		m, err := sip.ParseMultipart(contentType, body)
		if err != nil {
			return
		}

		for _, p := range m.Parts {
			_, _, _ = p.Disposition()
			_ = p.Optional()
		}

		_, _ = m.Find("application/sdp")
		_, _ = m.ByContentID("cid:a@b")

		out := m.Bytes()

		again, err := sip.ParseMultipart(m.ContentType(), out)
		if err != nil {
			t.Fatalf("serialization %q does not parse: %v", out, err)
		}

		if !slices.EqualFunc(again.Parts, m.Parts, func(a, b sip.BodyPart) bool {
			return string(a.Body) == string(b.Body) && a.MediaType() == b.MediaType() && a.ContentID() == b.ContentID()
		}) {
			t.Fatalf("parts changed in a round trip through %q", out)
		}

		if got := again.Bytes(); string(got) != string(out) {
			t.Fatalf("unstable serialization:\n%q\n%q", out, got)
		}
	})
}
