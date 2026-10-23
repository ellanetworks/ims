// Package amr is the AMR and AMR-WB speech codecs in RTP (RFC 4867) and SDP: the frames of their modes, the
// configuration of a payload type (§8.3.1), the mode-set, and the b=AS of a stream (TS 26.114 Annex K).
package amr

import (
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/sip/sdp"
)

// Codec is AMR or AMR-WB.
type Codec struct {
	// Encoding and ClockRate are its a=rtpmap (RFC 4867 §8.1).
	Encoding  string
	ClockRate uint32
	// FrameBits are the bits of a speech frame of each mode, SID excluded.
	FrameBits []int
	// SID and SIDBits are the frame type of the comfort noise frame and its bits (TS 26.101, TS 26.201 Table 1a);
	// SpeechLost whether the codec has the SPEECH_LOST frame type.
	SID        uint8
	SIDBits    int
	SpeechLost bool
}

var (
	// AMR is AMR (TS 26.114 Table K.1).
	AMR = Codec{Encoding: "AMR", ClockRate: 8000, FrameBits: []int{95, 103, 118, 134, 148, 159, 204, 244}, SID: 8, SIDBits: 39}
	// WB is AMR-WB (TS 26.114 Table K.5).
	WB = Codec{
		Encoding: "AMR-WB", ClockRate: 16000, FrameBits: []int{132, 177, 253, 285, 317, 365, 397, 461, 477},
		SID: 9, SIDBits: 40, SpeechLost: true,
	}
)

// Of is the codec of an a=rtpmap, AMR or AMR-WB.
func Of(r sdp.RTPMap) (Codec, bool) {
	for _, c := range []Codec{AMR, WB} {
		if strings.EqualFold(r.Encoding, c.Encoding) && r.ClockRate == c.ClockRate {
			return c, true
		}
	}

	return Codec{}, false
}

// configuration are the parameters of a payload type's transport format configuration, which an SDP answer keeps
// unmodified (RFC 4867 §8.3.1), with their values when absent (§8.1); "channels", the fifth, is of the a=rtpmap.
var configuration = []struct{ name, absent string }{
	{"octet-align", "0"}, {"crc", "0"}, {"robust-sorting", "0"}, {"interleaving", ""},
}

// SameConfiguration reports whether a and b have the same payload format configuration, which no other is
// compatible with (RFC 4867 §8.3.1): octet-align, crc, robust-sorting and interleaving, absent ones of their default.
func SameConfiguration(a, b sdp.FormatParams) bool {
	for _, p := range configuration {
		if value(a, p.name, p.absent) != value(b, p.name, p.absent) {
			return false
		}
	}

	return true
}

// Configuration are the parameters of fp of the payload format configuration and the mode-set, which an answer
// returns as offered (RFC 4867 §8.3.1).
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

// OctetAligned reports whether fp is of the octet-aligned mode (RFC 4867 §8.1).
func OctetAligned(fp sdp.FormatParams) bool { return value(fp, "octet-align", "0") == "1" }

// ModeSet are the modes of the mode-set of fp that c has, nil without one: all its modes.
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

// MaxMode is the highest mode of the mode-set of fp, of all modes without one.
func (c Codec) MaxMode(fp sdp.FormatParams) int {
	if modes := c.ModeSet(fp); len(modes) > 0 {
		return slices.Max(modes)
	}

	return len(c.FrameBits) - 1
}

// BandwidthAS is the b=AS in kbit/s of a stream of c (TS 24.229 §6.1.1), as TS 26.114 Annex K computes it for ptime
// 20 (Tables K.1 to K.8): one frame of mode a packet, its payload header and ToC (10 bits bandwidth-efficient, 16
// octet-aligned) and its frame (octet-aligned, on whole octets) on whole octets, the RTP, UDP and IP headers, 50
// packets a second, rounded up.
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

// value is the value of the parameter name of fp, absent without it.
func value(fp sdp.FormatParams, name, absent string) string {
	if v, ok := fp.Get(name); ok {
		return v
	}

	return absent
}
