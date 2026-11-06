package ibcf

import (
	"math"
	"slices"
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
	policedHeadroom    = 1.25
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
	pts   []string
	dir   string
	props []bwProp
}

type bwKey struct {
	pt, dir string
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

	pt := "*"
	if l.pts != nil {
		pt = strings.Join(l.pts, ",")
	}

	sb.WriteString(bwInfo + ":" + pt + " " + l.dir + " ")

	for i, p := range l.props {
		if i > 0 {
			sb.WriteString(";")
		}

		sb.WriteString(p.name + "=" + p.value)
	}

	return sb.String()
}

func (l *bwLine) dirs() []string {
	if l.dir == "sendrecv" {
		return []string{"send", "recv"}
	}

	return []string{l.dir}
}

func (l *bwLine) keys(formats []string) []bwKey {
	pts := l.pts
	if pts == nil {
		pts = formats
	}

	var keys []bwKey

	for _, pt := range pts {
		for _, dir := range l.dirs() {
			keys = append(keys, bwKey{pt: pt, dir: dir})
		}
	}

	return keys
}

// TS 26.114 clause 19.3.2
func parseBWInfo(v string) (bwLine, bool) {
	ptDef, rest, ok := strings.Cut(strings.TrimSpace(v), " ")
	if !ok {
		return bwLine{}, false
	}

	pts, ok := parsePayloadTypes(ptDef)
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

	l := bwLine{pts: pts, dir: dir}

	for _, def := range strings.Split(rest, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(def), "=")
		if !ok || name == "" {
			return bwLine{}, false
		}

		l.props = append(l.props, bwProp{name: name, value: value})
	}

	return l, true
}

// TS 26.114 clause 19.3.2
func parsePayloadTypes(def string) ([]string, bool) {
	if def == "*" {
		return nil, true
	}

	pts := strings.Split(def, ",")

	for _, pt := range pts {
		if len(pt) == 0 || len(pt) > 3 || strings.Trim(pt, "0123456789") != "" {
			return nil, false
		}
	}

	return pts, true
}

// TS 26.114 clause 19.3.2
func ipVersion(l *bwLine) int {
	if v, ok := l.get(bwIPVersion); ok && strings.TrimSpace(v) == "4" {
		return 4
	}

	return 6
}

type bwRelated struct {
	v4, v6           bool
	maxRate, minRate float64
}

type bwClass struct {
	keep             bool
	maxRate, minRate float64
}

// TS 26.114 clause 19.2.6
// TS 26.114 clause 19.2.7
// TS 26.114 clause 19.2.8
// TS 26.114 clause 19.3.2
// TS 26.114 clause 19.4
// TS 29.162 clause 9.1.5
func recalculate(md *sdp.Media, to4 bool, maxRate, minRate float64) {
	d, err := md.Desc()
	if err != nil {
		return
	}

	type parsed struct {
		at   int
		line bwLine
	}

	var lines []parsed

	for i, l := range md.Lines {
		if l.Type != 'a' || !strings.HasPrefix(strings.ToLower(l.Value), bwInfo+":") {
			continue
		}

		if b, ok := parseBWInfo(l.Value[len(bwInfo)+1:]); ok {
			lines = append(lines, parsed{at: i, line: b})
		}
	}

	if len(lines) == 0 {
		return
	}

	related := map[bwKey]*bwRelated{}

	for _, p := range lines {
		for _, k := range p.line.keys(d.Formats) {
			r := related[k]
			if r == nil {
				r = &bwRelated{maxRate: maxRate, minRate: minRate}
				related[k] = r
			}

			if ipVersion(&p.line) == 4 {
				r.v4 = true
			} else {
				r.v6 = true
			}

			if v, ok := rateOf(&p.line, bwMaxPacketRate); ok {
				r.maxRate = v
			}

			if v, ok := rateOf(&p.line, bwMinPacketRate); ok {
				r.minRate = v
			}
		}
	}

	target := 6
	if to4 {
		target = 4
	}

	replaced := map[int][]string{}

	for _, p := range lines {
		var (
			classes []bwClass
			members [][]bwKey
		)

		version := ipVersion(&p.line)

		for _, k := range p.line.keys(d.Formats) {
			r := related[k]

			c := bwClass{keep: true}
			if (!r.v4 || !r.v6) && version != target {
				c = bwClass{maxRate: r.maxRate, minRate: r.minRate}
			}

			i := slices.Index(classes, c)
			if i < 0 {
				classes, members = append(classes, c), append(members, nil)
				i = len(classes) - 1
			}

			members[i] = append(members[i], k)
		}

		if len(classes) == 1 {
			if !classes[0].keep {
				l := p.line
				convert(&l, target, classes[0].maxRate, classes[0].minRate)
				replaced[p.at] = []string{l.String()}
			}

			continue
		}

		var out []string

		for i, c := range classes {
			for _, part := range split(&p.line, members[i]) {
				if !c.keep {
					convert(&part, target, c.maxRate, c.minRate)
				}

				out = append(out, part.String())
			}
		}

		replaced[p.at] = out
	}

	if len(replaced) == 0 {
		return
	}

	var rebuilt sdp.Lines

	for i, l := range md.Lines {
		values, ok := replaced[i]
		if !ok {
			rebuilt = append(rebuilt, l)
			continue
		}

		for _, v := range values {
			rebuilt = append(rebuilt, sdp.Line{Type: 'a', Value: v})
		}
	}

	md.Lines = rebuilt
}

func split(l *bwLine, keys []bwKey) []bwLine {
	var (
		order []string
		dirs  = map[string][]string{}
	)

	for _, k := range keys {
		if _, ok := dirs[k.pt]; !ok {
			order = append(order, k.pt)
		}

		dirs[k.pt] = append(dirs[k.pt], k.dir)
	}

	var (
		out   []bwLine
		index = map[string]int{}
	)

	for _, pt := range order {
		dir := dirs[pt][0]
		if len(dirs[pt]) > 1 {
			dir = "sendrecv"
		}

		i, ok := index[dir]
		if !ok {
			i = len(out)
			index[dir] = i
			out = append(out, bwLine{dir: dir, props: slices.Clone(l.props)})
		}

		out[i].pts = append(out[i].pts, pt)
	}

	return out
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

// RFC 3550 section 6.2
// RFC 4566 section 5.8
// TS 29.162 clause 10.2.9
// TS 29.235 clause A.7.1.2.2.8.2
func policed(md *sdp.Media) uint64 {
	as, _ := md.Bandwidth(sdp.BandwidthAS)

	codecs, _ := md.Codecs()

	var codec uint64

	for _, c := range codecs {
		codec = max(codec, codecBitrates[strings.ToUpper(c.Encoding)])
	}

	payload := max(as*1000, codec)
	if payload == 0 {
		return 0
	}

	rate, _ := packetRates(md)
	rate = max(rate, policedPacketRate)

	header := float64(rtpHeader+udpHeader+ipv6Header) * 8 * rate

	return uint64(math.Ceil((float64(payload) + header) * policedHeadroom))
}
