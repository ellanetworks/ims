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
		whole := sip.NewBodyPart(contentType, body)

		_, _, _ = whole.Find("application/sdp")
		_, _, _ = whole.ByContentID("cid:a@b")
		_, _ = whole.Parts()

		m, err := whole.Multipart()
		if err != nil {
			return
		}

		for _, p := range m.Parts {
			_, _, _ = p.Disposition()
			_ = p.Optional()
		}

		ct, out := m.Encode()

		again, err := sip.NewBodyPart(ct, out).Multipart()
		if err != nil {
			t.Fatalf("serialization %q does not parse: %v", out, err)
		}

		if !slices.EqualFunc(again.Parts, m.Parts, func(a, b sip.BodyPart) bool {
			da, pa, ea := a.Disposition()
			db, pb, eb := b.Disposition()

			return string(a.Body) == string(b.Body) && sameFields(a.Header, b.Header) &&
				da == db && slices.Equal(pa, pb) && (ea == nil) == (eb == nil)
		}) {
			t.Fatalf("parts changed in a round trip through %q", out)
		}

		if ct2, got := again.Encode(); ct2 != ct || string(got) != string(out) {
			t.Fatalf("unstable serialization:\n%q\n%q", out, got)
		}
	})
}

func sameFields(a, b sip.Header) bool {
	return slices.EqualFunc(a, b, func(f, g sip.Field) bool { return f.Name == g.Name && f.Value == g.Value })
}

func FuzzFeatureSet(f *testing.F) {
	for _, s := range []string{
		`;audio;video;methods="INVITE,BYE";q=0.2`,
		`;mobility="fixed";events="!presence,message-summary";description="<PC>";+rangeparam="#-4:+5.125"`,
		`;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mcptt";+g.3gpp.mcptt;require;explicit`,
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		ps, err := sip.ParseParams(s)
		if err != nil {
			return
		}

		fs, err := sip.ParseFeatureSet(ps)
		if err != nil {
			return
		}

		_, _ = fs.Preference([]sip.CallerPreference{{Features: fs, Require: true, Explicit: true}})
		_ = fs.RejectedBy([]sip.CallerPreference{{Features: fs}})

		out := fs.Params()

		again, err := sip.ParseFeatureSet(out)
		if err != nil {
			t.Fatalf("%q encodes to %q, which does not parse: %v", s, out, err)
		}

		if again.Params().String() != out.String() {
			t.Fatalf("unstable encoding: %q, %q", out, again.Params())
		}

		if qa, ok := again.Preference([]sip.CallerPreference{{Features: fs, Require: true, Explicit: true}}); len(fs) > 0 && (!ok || qa != 1) {
			t.Fatalf("%q does not match itself after a round trip: %v, %v", s, qa, ok)
		}
	})
}

func FuzzTypedHeaders(f *testing.F) {
	for _, s := range []string{
		"auto;require", "mcpttp.13, dsn.flash", "a@b;local-tag=1;remote-tag=2", "a@b;to-tag=1;from-tag=2;early-only",
		"3600;refresher=uac", "presence.winfo;id=7", "terminated;reason=timeout;retry-after=30", "g.3gpp.mcptt-info;x",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		stable := func(name, out string, again func(string) (string, error)) {
			back, err := again(out)
			if err != nil || back != out {
				t.Fatalf("%s %q serializes to %q, which does not round-trip: %q, %v", name, s, out, back, err)
			}
		}

		if v, err := sip.ParseAnswerMode(s); err == nil {
			stable("Answer-Mode", v.String(), func(x string) (string, error) { v, err := sip.ParseAnswerMode(x); return v.String(), err })
		}

		if v, err := sip.ParseResourcePriority(s); err == nil {
			stable("r-value", v.String(), func(x string) (string, error) { v, err := sip.ParseResourcePriority(x); return v.String(), err })
		}

		if v, err := sip.ParseTargetDialog(s); err == nil {
			stable("Target-Dialog", v.String(), func(x string) (string, error) { v, err := sip.ParseTargetDialog(x); return v.String(), err })
		}

		if v, err := sip.ParseReplaces(s); err == nil {
			stable("Replaces", v.String(), func(x string) (string, error) { v, err := sip.ParseReplaces(x); return v.String(), err })
		}

		if v, err := sip.ParseSessionExpires(s); err == nil {
			stable("Session-Expires", v.String(), func(x string) (string, error) { v, err := sip.ParseSessionExpires(x); return v.String(), err })
		}

		if v, err := sip.ParseEvent(s); err == nil {
			stable("Event", v.String(), func(x string) (string, error) { v, err := sip.ParseEvent(x); return v.String(), err })
		}

		if v, err := sip.ParseSubscriptionState(s); err == nil {
			stable("Subscription-State", v.String(), func(x string) (string, error) { v, err := sip.ParseSubscriptionState(x); return v.String(), err })
		}

		if v, err := sip.ParseInfoPackage(s); err == nil {
			stable("Info-Package", v.String(), func(x string) (string, error) { v, err := sip.ParseInfoPackage(x); return v.String(), err })
		}

		var h sip.Header

		for _, name := range []string{"Accept-Contact", "Feature-Caps", "Recv-Info", "Allow-Events", "P-Asserted-Service", "Supported"} {
			h.Add(name, s)
		}

		_, _ = h.AcceptContact()
		_, _ = h.FeatureCaps()
		_, _, _ = h.RecvInfo()
		_, _ = h.AllowEvents()
		_, _ = h.AssertedServices()
		_, _ = h.OptionTags("Supported")
	})
}
