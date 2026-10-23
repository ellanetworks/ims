package amr

import (
	"slices"
	"testing"

	"github.com/ellanetworks/ims/sip/sdp"
)

// TS 26.114 Tables K.1 to K.8: b=AS at ptime 20, by codec, mode, framing and address family.
func TestBandwidthAS(t *testing.T) {
	for _, tc := range []struct {
		c                  Codec
		mode               int
		octetAligned, ipv6 bool
		as                 uint64
	}{
		{AMR, 0, false, false, 22},
		{AMR, 7, false, false, 29},
		{AMR, 7, false, true, 37},
		{AMR, 7, true, false, 30},
		{AMR, 6, true, false, 28},
		{WB, 0, false, false, 24},
		{WB, 2, false, false, 30},
		{WB, 3, false, false, 31},
		{WB, 8, false, false, 41},
		{WB, 0, false, true, 32},
		{WB, 4, false, true, 41},
		{WB, 8, false, true, 49},
		{WB, 1, true, false, 26},
		{WB, 3, true, false, 32},
		{WB, 8, true, false, 41},
		{WB, 5, true, true, 44},
		{WB, 8, true, true, 49},
	} {
		if as := tc.c.BandwidthAS(tc.mode, tc.octetAligned, tc.ipv6); as != tc.as {
			t.Errorf("%s mode %d octet-aligned %v IPv6 %v: b=AS:%d, want %d", tc.c.Encoding, tc.mode, tc.octetAligned, tc.ipv6, as, tc.as)
		}
	}
}

// RFC 4867 §8.3.1: the payload format configuration, absent parameters of their defaults; the mode-set.
func TestConfiguration(t *testing.T) {
	p := sdp.ParseFormatParams

	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"", "octet-align=0;crc=0", true},
		{"mode-set=0,2;max-red=0", "mode-change-capability=2", true},
		{"", "octet-align=1", false},
		{"OCTET-ALIGN=1", "octet-align=1", true},
		{"octet-align=1", "octet-align=1;crc=1", false},
		{"", "interleaving=4", false},
		{"robust-sorting=1;octet-align=1", "octet-align=1", false},
	} {
		if got := SameConfiguration(p(tc.a), p(tc.b)); got != tc.same {
			t.Errorf("%q, %q: %v", tc.a, tc.b, got)
		}
	}

	if got := Configuration(p("mode-set=0,1;octet-align=1;max-red=0;x=1")).String(); got != "mode-set=0,1;octet-align=1" {
		t.Errorf("configuration %s", got)
	}

	if m := WB.ModeSet(p("mode-set=8, 2,2,9")); !slices.Equal(m, []int{8, 2}) || WB.MaxMode(p("mode-set=0,3")) != 3 ||
		WB.MaxMode(nil) != 8 || AMR.MaxMode(nil) != 7 || WB.ModeSet(nil) != nil {
		t.Errorf("mode-set %v", m)
	}
}
