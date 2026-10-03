package sip

import (
	"slices"
	"testing"
)

func TestParsePrivacy(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Privacy
		out  string
	}{
		{"id", Privacy{"id"}, "id"},
		{"header;id", Privacy{"header", "id"}, "header;id"},
		{"header ; ID ; critical", Privacy{"header", "ID", "critical"}, "header;ID;critical"},
		{"user, id", Privacy{"user", "id"}, "user;id"},
		{"none", Privacy{"none"}, "none"},
		{"", nil, ""},
		{"id;", Privacy{"id"}, "id"},
	} {
		got, err := ParsePrivacy(tc.in)
		if err != nil {
			t.Errorf("ParsePrivacy(%q): %v", tc.in, err)
			continue
		}

		if !slices.Equal(got, tc.want) || got.String() != tc.out {
			t.Errorf("ParsePrivacy(%q) = %q (%s), want %q", tc.in, got, got, tc.want)
		}
	}

	for _, in := range []string{`"id"`, "id;<x>", "id;he@der"} {
		if p, err := ParsePrivacy(in); err == nil {
			t.Errorf("ParsePrivacy(%q) = %q, want error", in, p)
		}
	}
}

func TestPrivacyHas(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"id", true},
		{"ID", true},
		{"header;id", true},
		{"id;critical", true},
		{"header;user", false},
		{"identity", false},
		{"none", false},
	} {
		p, err := ParsePrivacy(tc.in)
		if err != nil {
			t.Fatal(err)
		}

		if got := p.Has(PrivacyID); got != tc.want {
			t.Errorf("Privacy(%q).Has(id) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestHeaderPrivacy(t *testing.T) {
	var h Header

	p, err := h.Privacy()
	if err != nil || p != nil {
		t.Errorf("Privacy() without header = %q, %v", p, err)
	}

	h.Add("Privacy", "header")
	h.Add("privacy", "id")

	p, err = h.Privacy()
	if err != nil || !slices.Equal(p, Privacy{"header", "id"}) || !p.Has(PrivacyID) || !p.Has(PrivacyHeader) || p.Has(PrivacySession) {
		t.Errorf("Privacy() = %q, %v", p, err)
	}

	h.Add("Privacy", "<bad>")

	if _, err := h.Privacy(); err == nil {
		t.Error("Privacy(): no error")
	}
}
