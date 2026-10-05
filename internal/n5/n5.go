// Package n5 is the NF service consumer side of Npcf_PolicyAuthorization (TS 29.514) over cleartext HTTP/2.
package n5

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// TS 29.514 §5.3.1
const (
	appSessionsPath    = "/npcf-policyauthorization/v1/app-sessions"
	eventsSubscription = "events-subscription"
	deleteOperation    = "delete"
)

// TS 29.514 §5.2.2.2
const (
	ContentJSON       = "application/json"
	ContentMergePatch = "application/merge-patch+json"
	ContentProblem    = "application/problem+json"
)

// TS 29.514 §5.8
const (
	FeatureIMSSBI              = 5
	FeatureNetLoc              = 6
	FeatureProvAFSignalFlow    = 7
	FeatureRANNASCause         = 14
	FeaturePCSCFRestorationEnh = 19
	FeaturePatchCorrection     = 28
)

// SupportedFeatures is a feature bitmask in hexadecimal, the highest-numbered features first (TS 29.571 §5.2.2,
// TS 29.500 §6.6.2). Feature n is bit n-1.
type SupportedFeatures string

func Features(n ...int) SupportedFeatures {
	var b big.Int

	for _, f := range n {
		if f > 0 {
			b.SetBit(&b, f-1, 1)
		}
	}

	return SupportedFeatures(b.Text(16))
}

func (f SupportedFeatures) bits() (*big.Int, error) {
	if f == "" {
		return new(big.Int), nil
	}

	if strings.ContainsAny(string(f), "+-xX_") {
		return nil, fmt.Errorf("invalid supported features %q", string(f))
	}

	b, ok := new(big.Int).SetString(string(f), 16)
	if !ok {
		return nil, fmt.Errorf("invalid supported features %q", string(f))
	}

	return b, nil
}

func (f SupportedFeatures) Valid() bool {
	_, err := f.bits()
	return err == nil
}

// Has reports whether feature n is set. A malformed bitmask has none.
func (f SupportedFeatures) Has(n int) bool {
	b, err := f.bits()

	return err == nil && n > 0 && b.Bit(n-1) == 1
}

// Intersect returns the features set in both, as the PCF negotiates them (TS 29.500 §6.6.2).
func (f SupportedFeatures) Intersect(g SupportedFeatures) SupportedFeatures {
	a, errA := f.bits()
	b, errB := g.bits()

	if errA != nil || errB != nil {
		return ""
	}

	return SupportedFeatures(new(big.Int).And(a, b).Text(16))
}

// BitRate is a bit rate in bit/s, encoded as a TS 29.571 §5.2.2 BitRate string.
type BitRate uint64

var bitRateUnits = map[string]uint64{"bps": 1, "Kbps": 1e3, "Mbps": 1e6, "Gbps": 1e9, "Tbps": 1e12}

// MarshalText writes the rate in bps, so that no precision is lost.
func (r BitRate) MarshalText() ([]byte, error) {
	return []byte(strconv.FormatUint(uint64(r), 10) + " bps"), nil
}

// UnmarshalText reads `^\d+(\.\d+)? (bps|Kbps|Mbps|Gbps|Tbps)$`, rounding a fraction of a bit/s to the nearest.
func (r *BitRate) UnmarshalText(text []byte) error {
	num, unit, ok := strings.Cut(string(text), " ")
	mult, known := bitRateUnits[unit]

	whole, frac, hasFrac := strings.Cut(num, ".")
	if !ok || !known || !digits(whole) || hasFrac && !digits(frac) {
		return fmt.Errorf("invalid bit rate %q", string(text))
	}

	v, ok := new(big.Rat).SetString(num)
	if !ok {
		return fmt.Errorf("invalid bit rate %q", string(text))
	}

	v.Mul(v, new(big.Rat).SetInt64(int64(mult)))

	n := new(big.Int).Quo(new(big.Int).Add(new(big.Int).Mul(v.Num(), big.NewInt(2)), v.Denom()),
		new(big.Int).Mul(v.Denom(), big.NewInt(2)))
	if !n.IsUint64() {
		return fmt.Errorf("bit rate %q out of range", string(text))
	}

	*r = BitRate(n.Uint64())

	return nil
}

func digits(s string) bool {
	if s == "" {
		return false
	}

	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}

	return true
}
