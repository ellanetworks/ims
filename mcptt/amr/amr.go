package amr

import (
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/sip/sdp"
)

type Codec struct {
	Encoding   string
	ClockRate  uint32
	FrameBits  []int
	SID        uint8
	SIDBits    int
	SpeechLost bool
}

var (
	AMR = Codec{Encoding: "AMR", ClockRate: 8000, FrameBits: []int{95, 103, 118, 134, 148, 159, 204, 244}, SID: 8, SIDBits: 39}
	WB  = Codec{
		Encoding: "AMR-WB", ClockRate: 16000, FrameBits: []int{132, 177, 253, 285, 317, 365, 397, 461, 477},
		SID: 9, SIDBits: 40, SpeechLost: true,
	}
)

func Of(r sdp.RTPMap) (Codec, bool) {
	for _, c := range []Codec{AMR, WB} {
		if strings.EqualFold(r.Encoding, c.Encoding) && r.ClockRate == c.ClockRate {
			return c, true
		}
	}

	return Codec{}, false
}

// RFC 4867 section 8.3.1
// RFC 4867 section 8.1
var configuration = []struct{ name, absent string }{
	{"octet-align", "0"}, {"crc", "0"}, {"robust-sorting", "0"}, {"interleaving", ""},
}

// RFC 4867 section 8.3.1
func SameConfiguration(a, b sdp.FormatParams) bool {
	for _, p := range configuration {
		if value(a, p.name, p.absent) != value(b, p.name, p.absent) {
			return false
		}
	}

	return true
}

// RFC 4867 section 8.3.1
func Configuration(fp sdp.FormatParams) sdp.FormatParams {
	var out sdp.FormatParams

	for _, f := range fp {
		if strings.EqualFold(f.Name, "mode-set") || slices.ContainsFunc(configuration, func(p struct{ name, absent string }) bool {
			return strings.EqualFold(f.Name, p.name)
		}) {
			out = append(out, f)
		}
	}

	return out
}

// RFC 4867 section 8.1
func OctetAligned(fp sdp.FormatParams) bool { return value(fp, "octet-align", "0") == "1" }

func (c Codec) ModeSet(fp sdp.FormatParams) []int {
	v, ok := fp.Get("mode-set")
	if !ok {
		return nil
	}

	var modes []int

	for s := range strings.SplitSeq(v, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 && n < len(c.FrameBits) && !slices.Contains(modes, n) {
			modes = append(modes, n)
		}
	}

	return modes
}

func (c Codec) MaxMode(fp sdp.FormatParams) int {
	if modes := c.ModeSet(fp); len(modes) > 0 {
		return slices.Max(modes)
	}

	return len(c.FrameBits) - 1
}

// TS 26.114 annex K
func (c Codec) BandwidthAS(mode int, octetAligned, ipv6 bool) uint64 {
	bits := 10 + c.FrameBits[mode]
	if octetAligned {
		bits = 16 + (c.FrameBits[mode]+7)/8*8
	}

	ip := 160
	if ipv6 {
		ip = 320
	}

	total := (bits+7)/8*8 + 96 + 64 + ip

	return uint64((total*50 + 999) / 1000)
}

func value(fp sdp.FormatParams, name, absent string) string {
	if v, ok := fp.Get(name); ok {
		return v
	}

	return absent
}
