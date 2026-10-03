package sip_test

import (
	"slices"
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

		again, err := sip.ParseReason(out)
		if err != nil || again.String() != out || again.Text() != r.Text() {
			t.Fatalf("%q serializes to %q, which does not round-trip: %v", s, out, err)
		}

		c1, ok1 := r.Cause()
		if c2, ok2 := again.Cause(); c1 != c2 || ok1 != ok2 {
			t.Fatalf("%q: cause %d/%v became %d/%v", s, c1, ok1, c2, ok2)
		}

		n := sip.NewReason(r.Protocol, c1, r.Text())
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

		again, err := sip.ParseWarning(out)
		if err != nil || again != w {
			t.Fatalf("%q serializes to %q, which does not round-trip: %v", s, out, err)
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
