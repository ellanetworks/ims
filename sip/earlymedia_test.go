package sip

import (
	"slices"
	"testing"
)

func TestParseEarlyMedia(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want EarlyMedia
		out  string
	}{
		{"supported", EarlyMedia{"supported"}, "supported"},
		{"sendrecv", EarlyMedia{"sendrecv"}, "sendrecv"},
		{"inactive, gated", EarlyMedia{"inactive", "gated"}, "inactive, gated"},
		{"sendonly,recvonly ,  inactive", EarlyMedia{"sendonly", "recvonly", "inactive"}, "sendonly, recvonly, inactive"},
		{"", nil, ""},
		{"x-extension", EarlyMedia{"x-extension"}, "x-extension"},
	} {
		got, err := ParseEarlyMedia(tc.in)
		if err != nil {
			t.Errorf("ParseEarlyMedia(%q): %v", tc.in, err)
			continue
		}

		if !slices.Equal(got, tc.want) || got.String() != tc.out {
			t.Errorf("ParseEarlyMedia(%q) = %q (%s), want %q", tc.in, got, got, tc.want)
		}
	}

	for _, in := range []string{`"supported"`, "sendrecv;gated", "send recv"} {
		if em, err := ParseEarlyMedia(in); err == nil {
			t.Errorf("ParseEarlyMedia(%q) = %q, want error", in, em)
		}
	}
}

func TestHeaderEarlyMedia(t *testing.T) {
	var h Header

	if em, ok, err := h.EarlyMedia(); em != nil || ok || err != nil {
		t.Errorf("EarlyMedia() without header = %q, %v, %v", em, ok, err)
	}

	h.Add("P-Early-Media", "")

	if em, ok, err := h.EarlyMedia(); em != nil || !ok || err != nil {
		t.Errorf("EarlyMedia() with empty header = %q, %v, %v", em, ok, err)
	}

	h.Add("p-early-media", "SendRecv, gated")

	em, ok, err := h.EarlyMedia()
	if err != nil || !ok || !em.Has(EarlyMediaSendRecv) || !em.Has(EarlyMediaGated) || em.Has(EarlyMediaSupported) {
		t.Errorf("EarlyMedia() = %q, %v, %v", em, ok, err)
	}

	h.Add("P-Early-Media", "a b")

	if _, ok, err := h.EarlyMedia(); !ok || err == nil {
		t.Errorf("EarlyMedia() with bad value: %v, %v", ok, err)
	}
}
