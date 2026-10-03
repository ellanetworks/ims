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
		if got := NewWarning(tc.code, "pcscf.ims.example.org").String(); got != tc.want {
			t.Errorf("NewWarning(%d) = %q, want %q", tc.code, got, tc.want)
		}
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

	if _, err := h.Warnings(); err == nil {
		t.Error("Warnings(): no error")
	}
}
