package n5

import (
	"encoding/json"
	"testing"
)

// TS 29.571 §5.2.2: feature n is bit n-1, the lowest features in the last character.
func TestFeatures(t *testing.T) {
	f := Features(FeatureIMSSBI, FeaturePatchCorrection)
	if f != "8000010" {
		t.Fatalf("Features(IMS_SBI, PatchCorrection) = %q, want 8000010", f)
	}

	if !f.Has(FeatureIMSSBI) || !f.Has(FeaturePatchCorrection) || f.Has(FeatureNetLoc) || f.Has(0) {
		t.Fatalf("%q has the wrong features", f)
	}

	for _, tc := range []struct {
		a, b SupportedFeatures
		want SupportedFeatures
	}{
		{"8000010", "12", "10"},
		{"8000010", "0000000000000012", "10"},
		{"8000010", "8000010", "8000010"},
		{"8000010", "", "0"},
		{"8000010", "zz", ""},
		{"8000010", "-1", ""},
	} {
		if got := tc.a.Intersect(tc.b); got != tc.want {
			t.Errorf("%q ∩ %q = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}

	if !SupportedFeatures("A0").Has(8) || !SupportedFeatures("a0").Has(6) || SupportedFeatures("A0").Has(5) {
		t.Fatal("hexadecimal digits are case-insensitive")
	}
}

func TestBitRate(t *testing.T) {
	b, err := json.Marshal(BitRate(64000))
	if err != nil || string(b) != `"64000 bps"` {
		t.Fatalf("Marshal = %s, %v; want \"64000 bps\"", b, err)
	}

	for in, want := range map[string]BitRate{
		"0 bps":       0,
		"64 Kbps":     64000,
		"41.5 Kbps":   41500,
		"1.0005 Kbps": 1001,
		"0.0004 Kbps": 0,
		"2 Mbps":      2000000,
		"1.25 Gbps":   1250000000,
		"3 Tbps":      3000000000000,
	} {
		var r BitRate
		if err := r.UnmarshalText([]byte(in)); err != nil || r != want {
			t.Errorf("%q = %d, %v; want %d", in, r, err, want)
		}
	}

	invalid := []string{
		"", "64", "64 kbps", "64Kbps", "64  Kbps", "-1 bps", "+1 bps", "1. bps", ".5 bps", "1e3 bps", "1 bps ", "20000000 Tbps",
	}

	for _, in := range invalid {
		var r BitRate
		if err := r.UnmarshalText([]byte(in)); err == nil {
			t.Errorf("%q accepted as %d", in, r)
		}
	}
}
