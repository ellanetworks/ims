package sdp

import (
	"slices"
	"testing"
)

// RFC 4855 §3, the fmtp:MCPTT examples of TS 24.380 §4.3.3.1 and TS 24.379: parameters with and without values, a trailing
// separator, whitespace, names compared case-insensitively.
func TestFormatParams(t *testing.T) {
	for _, c := range []struct {
		in, out string
		want    FormatParams
	}{
		{"mode-change-capability=2;max-red=220", "mode-change-capability=2;max-red=220", FormatParams{
			{Name: "mode-change-capability", Value: "2", HasValue: true}, {Name: "max-red", Value: "220", HasValue: true},
		}},
		{"mc_queueing;mc_priority=5;mc_granted;mc_floor_ssrc=12345678", "mc_queueing;mc_priority=5;mc_granted;mc_floor_ssrc=12345678", FormatParams{
			{Name: "mc_queueing"},
			{Name: "mc_priority", Value: "5", HasValue: true},
			{Name: "mc_granted"},
			{Name: "mc_floor_ssrc", Value: "12345678", HasValue: true},
		}},
		{"mc_queueing;", "mc_queueing", FormatParams{{Name: "mc_queueing"}}},
		{" profile-level-id = 42e01f ; packetization-mode=1 ;;", "profile-level-id=42e01f;packetization-mode=1", FormatParams{
			{Name: "profile-level-id", Value: "42e01f", HasValue: true}, {Name: "packetization-mode", Value: "1", HasValue: true},
		}},
		{"0-15", "0-15", FormatParams{{Name: "0-15"}}},
		{"x=;=y", "x=", FormatParams{{Name: "x", HasValue: true}}},
		{"", "", nil},
	} {
		got := ParseFormatParams(c.in)
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: %+v, want %+v", c.in, got, c.want)
		}

		if s := got.String(); s != c.out {
			t.Errorf("%q: String %q, want %q", c.in, s, c.out)
		}
	}

	ps := ParseFormatParams("Octet-Align=1;mc_granted")
	if v, ok := ps.Get("octet-align"); !ok || v != "1" {
		t.Errorf("Get(octet-align) = %q, %v", v, ok)
	}

	if !ps.Has("MC_GRANTED") || ps.Has("mc_queueing") {
		t.Errorf("Has on %v", ps)
	}
}

func TestMediaFormatParams(t *testing.T) {
	s, err := Parse([]byte("v=0\r\nm=application 1234 udp MCPTT\r\na=fmtp:MCPTT mc_queueing;mc_priority=5\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	ps, ok := s.Media[0].FormatParams("MCPTT")
	if v, _ := ps.Get("mc_priority"); !ok || v != "5" || !ps.Has("mc_queueing") {
		t.Errorf("FormatParams(MCPTT) = %+v, %v", ps, ok)
	}

	if _, ok := s.Media[0].FormatParams("96"); ok {
		t.Error("FormatParams of a format without fmtp")
	}
}

// RFC 5576 §4.1, TS 24.379 §6.2.1 step 2 i): the SSRC an MCPTT client would like for its RTP, and the attributes of
// other sources.
func TestSourceAttrs(t *testing.T) {
	s, err := Parse([]byte("v=0\r\nm=audio 4000 RTP/AVP 96\r\na=ssrc:4294967295 cname:user@example.com\r\n" +
		"a=ssrc:12 fmtp:96 a=b\r\na=ssrc:12 x\r\na=ssrc:4294967296 cname:x\r\na=ssrc:-1 cname:x\r\na=ssrc:7\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Media[0].SourceAttrs()
	want := []SourceAttr{{4294967295, "cname", "user@example.com"}, {12, "fmtp", "96 a=b"}, {12, "x", ""}}

	if !slices.Equal(got, want) || err == nil {
		t.Errorf("SourceAttrs = %+v, %v", got, err)
	}

	for _, a := range want {
		if again, err := ParseSourceAttr(a.String()); err != nil || again != a {
			t.Errorf("%+v does not round-trip: %+v, %v", a, again, err)
		}
	}
}

func TestFmtpFormatCase(t *testing.T) {
	s, err := Parse([]byte("v=0\r\nm=application 1234 udp mcptt\r\na=fmtp:MCPTT mc_queueing\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	if v, ok := s.Media[0].Fmtp("mcptt"); !ok || v != "mc_queueing" {
		t.Errorf("Fmtp(mcptt) = %q, %v", v, ok)
	}
}
