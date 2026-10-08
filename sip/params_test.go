package sip

import "testing"

// RFC 3261 §25.1
func TestParseQValue(t *testing.T) {
	for in, want := range map[string]float64{"0": 0, "0.": 0, "0.5": 0.5, "0.001": 0.001, "0.999": 0.999, "1": 1, "1.": 1, "1.000": 1} {
		if got, err := ParseQValue(in); err != nil || got != want {
			t.Errorf("ParseQValue(%q) = %v, %v, want %v", in, got, err, want)
		}
	}

	for _, in := range []string{"", ".5", "0.0001", "1.5", "1.001", "2", "5", "-0.5", "NaN", "Inf", "1e0", "0x1p-1", "0.5 ", "+0.5"} {
		if got, err := ParseQValue(in); err == nil {
			t.Errorf("ParseQValue(%q) = %v, want an error", in, got)
		}
	}
}
