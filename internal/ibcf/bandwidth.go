package ibcf

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/sip/sdp"
)

// TS 26.114 clause K.2
const (
	rtpHeader  = 12
	udpHeader  = 8
	ipv4Header = 20
	ipv6Header = 40
)

const (
	defaultPtime       = 20 * time.Millisecond
	policedPacketRate  = 100
	bwInfo             = "bw-info"
	bwIPVersion        = "IpVer"
	bwMaxPacketRate    = "MaxPRate"
	bwMinPacketRate    = "MinPRate"
	bwMinSupported     = "MinSupBw"
	bwMaxSupported     = "MaxSupBw"
	bwMaxDesired       = "MaxDesBw"
	bwMinDesired       = "MinDesBw"
	bandwidthPrecision = 100
)

var codecBitrates = map[string]uint64{
	"PCMU": 64000, "PCMA": 64000, "G722": 64000, "G7221": 48000, "G729": 8000, "G723": 6300, "GSM": 13200,
	"ILBC": 15200, "AMR": 12200, "AMR-WB": 23850, "EVS": 128000, "OPUS": 510000, "SPEEX": 44200,
}

func packetRates(md *sdp.Media) (maxRate, minRate float64) {
	ptime, ok := md.Ptime()
	if !ok {
		ptime = defaultPtime
	}

	maxRate = float64(time.Second) / float64(ptime)
	minRate = maxRate

	if maxptime, ok := md.MaxPtime(); ok && maxptime > ptime {
		minRate = float64(time.Second) / float64(maxptime)
	}

	return maxRate, minRate
}

// TS 26.114 clause K.2
func versionDelta(rate float64) float64 {
	return (ipv6Header - ipv4Header) * 8 * rate / 1000
}

// TS 29.162 clause 9.1.5
func interwork(s *sdp.Session, md *sdp.Media, from4, to4 bool) {
	if from4 == to4 {
		return
	}

	sign := 1.0
	if to4 {
		sign = -1
	}

	maxRate, minRate := packetRates(md)

	for _, ls := range []*sdp.Lines{&md.Lines, &s.Lines} {
		as, ok := ls.Bandwidth(sdp.BandwidthAS)
		if !ok || as == 0 {
			continue
		}

		v := math.Ceil(float64(as) + sign*versionDelta(maxRate))
		ls.SetBandwidth(sdp.BandwidthAS, uint64(max(1, v)))
	}

	recalculate(md, to4, maxRate, minRate)
}

type bwProp struct {
	name, value string
}

type bwLine struct {
	at      int
	pt, dir string
	props   []bwProp
}

func (l *bwLine) get(name string) (string, bool) {
	for _, p := range l.props {
		if strings.EqualFold(p.name, name) {
			return p.value, true
		}
	}

	return "", false
}

func (l *bwLine) put(name, value string) {
	for i, p := range l.props {
		if strings.EqualFold(p.name, name) {
			l.props[i].value = value
			return
		}
	}

	l.props = append(l.props, bwProp{name: name, value: value})
}

func (l *bwLine) String() string {
	var sb strings.Builder

	sb.WriteString(bwInfo + ":" + l.pt + " " + l.dir + " ")

	for i, p := range l.props {
		if i > 0 {
			sb.WriteString(";")
		}

		sb.WriteString(p.name + "=" + p.value)
	}

	return sb.String()
}

// TS 26.114 clause 19.3.2
func parseBWInfo(v string) (bwLine, bool) {
	pt, rest, ok := strings.Cut(strings.TrimSpace(v), " ")
	if !ok {
		return bwLine{}, false
	}

	dir, rest, ok := strings.Cut(strings.TrimLeft(rest, " "), " ")
	if !ok {
		return bwLine{}, false
	}

	switch dir {
	case "send", "recv", "sendrecv":
	default:
		return bwLine{}, false
	}

	l := bwLine{pt: pt, dir: dir}

	for _, def := range strings.Split(rest, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(def), "=")
		if !ok || name == "" {
			return bwLine{}, false
		}

		l.props = append(l.props, bwProp{name: name, value: value})
	}

	return l, true
}

func ipVersion(l *bwLine) int {
	v, ok := l.get(bwIPVersion)
	if !ok {
		return 0
	}

	n, err := strconv.Atoi(v)
	if err != nil || n != 4 && n != 6 {
		return 0
	}

	return n
}

// TS 26.114 clause 19.2.6
// TS 26.114 clause 19.2.7
// TS 26.114 clause 19.2.8
// TS 26.114 clause 19.4
// TS 29.162 clause 9.1.5
func recalculate(md *sdp.Media, to4 bool, maxRate, minRate float64) {
	var lines []*bwLine

	for i, l := range md.Lines {
		if l.Type != 'a' || !strings.HasPrefix(strings.ToLower(l.Value), bwInfo+":") {
			continue
		}

		if b, ok := parseBWInfo(l.Value[len(bwInfo)+1:]); ok {
			b.at = i
			lines = append(lines, &b)
		}
	}

	target := 6
	if to4 {
		target = 4
	}

	groups := map[string][]*bwLine{}
	for _, l := range lines {
		groups[l.pt+" "+l.dir] = append(groups[l.pt+" "+l.dir], l)
	}

	for _, group := range groups {
		explicit := map[int]bool{}

		for _, l := range group {
			if v := ipVersion(l); v != 0 {
				explicit[v] = true
			}
		}

		if explicit[4] && explicit[6] {
			continue
		}

		implied := 6
		if explicit[4] {
			implied = 4
		}

		groupMax, groupMin := maxRate, minRate

		for _, l := range group {
			if r, ok := rateOf(l, bwMaxPacketRate); ok {
				groupMax = r
			}

			if r, ok := rateOf(l, bwMinPacketRate); ok {
				groupMin = r
			}
		}

		for _, l := range group {
			version := ipVersion(l)
			if version == 0 {
				version = implied
			}

			if version == target {
				continue
			}

			convert(l, target, groupMax, groupMin)
			md.Lines[l.at].Value = l.String()
		}
	}
}

func rateOf(l *bwLine, name string) (float64, bool) {
	v, ok := l.get(name)
	if !ok {
		return 0, false
	}

	r, err := strconv.ParseFloat(v, 64)
	if err != nil || r <= 0 {
		return 0, false
	}

	return r, true
}

func convert(l *bwLine, target int, maxRate, minRate float64) {
	sign := 1.0
	if target == 4 {
		sign = -1
	}

	if r, ok := rateOf(l, bwMaxPacketRate); ok {
		maxRate = r
	}

	if r, ok := rateOf(l, bwMinPacketRate); ok {
		minRate = r
	}

	var usesMax, usesMin bool

	for i, p := range l.props {
		rate := maxRate

		switch {
		case strings.EqualFold(p.name, bwMaxSupported), strings.EqualFold(p.name, bwMaxDesired), strings.EqualFold(p.name, bwMinDesired):
			usesMax = true
		case strings.EqualFold(p.name, bwMinSupported):
			usesMin, rate = true, minRate
		default:
			continue
		}

		v, err := strconv.ParseFloat(p.value, 64)
		if err != nil {
			continue
		}

		l.props[i].value = formatBandwidth(max(0, v+sign*versionDelta(rate)))
	}

	l.put(bwIPVersion, strconv.Itoa(target))

	if usesMax {
		l.put(bwMaxPacketRate, formatBandwidth(maxRate))
	}

	if usesMin {
		l.put(bwMinPacketRate, formatBandwidth(minRate))
	}
}

func formatBandwidth(v float64) string {
	return strconv.FormatFloat(math.Round(v*bandwidthPrecision)/bandwidthPrecision, 'f', -1, 64)
}

// TS 29.162 clause 10.2.9
// TS 29.235 clause A.7.1.2.2.8.2
func policed(md *sdp.Media, is4 bool) uint64 {
	as, _ := md.Bandwidth(sdp.BandwidthAS)
	rate := as * 1000

	codecs, _ := md.Codecs()

	var codec uint64

	for _, c := range codecs {
		codec = max(codec, codecBitrates[strings.ToUpper(c.Encoding)])
	}

	if codec == 0 {
		return rate
	}

	header := uint64(rtpHeader + udpHeader + ipv6Header)
	if is4 {
		header = rtpHeader + udpHeader + ipv4Header
	}

	return max(rate, codec+header*8*policedPacketRate)
}
