package sip

import (
	"slices"
	"testing"
)

func TestParseWarning(t *testing.T) {
	for _, tc := range []struct {
		in, out string
		want    Warning
	}{
		{`304 pcscf.ims.example.org "Media type not available"`, "", Warning{304, "pcscf.ims.example.org", "Media type not available"}},
		{`301 [2001:db8::1]:5060 "Incompatible network address formats"`, "", Warning{301, "[2001:db8::1]:5060", "Incompatible network address formats"}},
		{`399 isi.edu "Mal\"formed"`, "", Warning{399, "isi.edu", `Mal"formed`}},
		{" 370  10.0.0.1:5060\t\"\" ", `370 10.0.0.1:5060 ""`, Warning{370, "10.0.0.1:5060", ""}},
	} {
		got, err := ParseWarning(tc.in)
		if err != nil {
			t.Errorf("ParseWarning(%q): %v", tc.in, err)
			continue
		}

		if got != tc.want {
			t.Errorf("ParseWarning(%q) = %+v, want %+v", tc.in, got, tc.want)
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

func TestParseWarningErrors(t *testing.T) {
	for _, in := range []string{
		"", "304", `304 "text"`, `30 host "text"`, `3040 host "text"`, `abc host "text"`,
		"304 host text", `304 host "unterminated`, `304 host "a" b`, `304 ho"st "a"`, "304 host \"a\nb\"",
		`301 2001:db8::1 "x"`, `301 a;b=c "x"`, `301 h<o>st "x"`, `301 h\st "x"`, `301 [2001:db8::1 "x"`,
		`301 host:99999 "x"`, `301 [10.0.0.1] "x"`, "399 host \"\x01\"", "399 host \"\x7f\"",
	} {
		if w, err := ParseWarning(in); err == nil {
			t.Errorf("ParseWarning(%q) = %+v, want error", in, w)
		}
	}
}

func TestNewWarning(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{
		{WarnMediaTypeNotAvailable, `304 pcscf.ims.example.org "Media type not available"`},
		{WarnIncompatibleAddressFormats, `301 pcscf.ims.example.org "Incompatible network address formats"`},
		{WarnIncompatibleMediaFormat, `305 pcscf.ims.example.org "Incompatible media format"`},
		{WarnInsufficientBandwidth, `370 pcscf.ims.example.org "Insufficient bandwidth"`},
		{388, `388 pcscf.ims.example.org ""`},
	} {
		w, err := NewWarning(tc.code, "pcscf.ims.example.org")
		if err != nil {
			t.Fatalf("NewWarning(%d): %v", tc.code, err)
		}

		if got := w.String(); got != tc.want {
			t.Errorf("NewWarning(%d) = %q, want %q", tc.code, got, tc.want)
		}
	}

	for _, tc := range []struct{ agent, want string }{
		{"10.0.0.1", "10.0.0.1"},
		{"10.0.0.1:5060", "10.0.0.1:5060"},
		{"2001:db8::1", "[2001:db8::1]"},
		{"[2001:db8::1]:5060", "[2001:db8::1]:5060"},
		{"pcscf", "pcscf"},
		{"pcscf.ims.example.org:5060", "pcscf.ims.example.org:5060"},
	} {
		w, err := NewWarning(WarnIncompatibleAddressFormats, tc.agent)
		if err != nil {
			t.Errorf("NewWarning(301, %q): %v", tc.agent, err)
			continue
		}

		if w.Agent != tc.want {
			t.Errorf("NewWarning(301, %q).Agent = %q, want %q", tc.agent, w.Agent, tc.want)
		}

		if _, err := ParseWarning(w.String()); err != nil {
			t.Errorf("ParseWarning(%q): %v", w, err)
		}
	}

	for _, tc := range []struct {
		code  int
		agent string
	}{
		{301, ""}, {301, "a b"}, {301, "fe80::1%eth0"}, {301, "a;b"}, {301, "h:x"}, {1000, "x"}, {-1, "x"},
	} {
		if w, err := NewWarning(tc.code, tc.agent); err == nil {
			t.Errorf("NewWarning(%d, %q) = %q, want error", tc.code, tc.agent, w)
		}
	}

	w := Warning{Code: WarnMiscellaneous, Agent: "x", Text: "a\x00b"}
	if s := w.String(); s != `399 x "a b"` {
		t.Errorf("String() = %q", s)
	}
}

func TestHeaderWarnings(t *testing.T) {
	var h Header

	h.Add("Warning", `304 a "Media, type not available", 301 b "x"`)
	h.Add("warning", `370 c "y"`)

	ws, err := h.Warnings()
	if err != nil {
		t.Fatal(err)
	}

	var codes []int
	for _, w := range ws {
		codes = append(codes, w.Code)
	}

	if !slices.Equal(codes, []int{304, 301, 370}) || ws[0].Text != "Media, type not available" {
		t.Errorf("Warnings() = %+v", ws)
	}

	h.Add("Warning", "bad")

	if ws, err := h.Warnings(); err == nil || len(ws) != 3 {
		t.Errorf("Warnings() with a bad element = %d warnings, %v", len(ws), err)
	}
}
