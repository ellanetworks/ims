package testue

import (
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/sip/sdp"
)

// RFC 5939 section 3.3.1
const capNegBase = "cap-v0"

// RFC 5939 section 3.6.2
var capNegAttrs = []string{"csup", "creq", "acap", "tcap", "pcfg", "acfg"}

var knownAttrs = map[string]bool{
	"rtpmap": true, "fmtp": true, "ptime": true, "maxptime": true, "rtcp-fb": true, "extmap": true, "rtcp": true,
	"curr": true, "des": true, "conf": true, "framerate": true, "imageattr": true,
	"sendrecv": true, "sendonly": true, "recvonly": true, "inactive": true,
}

// RFC 5939 section 3.6.2
var sessionAttrs = map[string]bool{"sendrecv": true, "sendonly": true, "recvonly": true, "inactive": true, "extmap": true}

var supportedProtos = []string{avp, avpf}

type capability struct {
	value   string
	session bool
}

// RFC 5939 section 3.4.1
// RFC 5939 section 3.4.2
type capabilities struct {
	transports map[string]capability
	attributes map[string]capability
}

// RFC 5939 section 3.4.1
// RFC 5939 section 3.4.2
func collectCapabilities(offer *sdp.Session, m *sdp.Media) capabilities {
	c := capabilities{transports: map[string]capability{}, attributes: map[string]capability{}}

	for _, level := range []struct {
		lines   sdp.Lines
		session bool
	}{{offer.Lines, true}, {m.Lines, false}} {
		for _, v := range level.lines.Attrs("tcap") {
			fields := strings.Fields(v)
			if len(fields) < 2 {
				continue
			}

			first, err := strconv.Atoi(fields[0])
			if err != nil || first < 1 {
				continue
			}

			for j, p := range fields[1:] {
				c.transports[strconv.Itoa(first+j)] = capability{value: p, session: level.session}
			}
		}

		for _, v := range level.lines.Attrs("acap") {
			num, par, ok := strings.Cut(v, " ")
			if n, err := strconv.Atoi(num); !ok || err != nil || n < 1 {
				continue
			}

			c.attributes[num] = capability{value: strings.TrimSpace(par), session: level.session}
		}
	}

	return c
}

// RFC 5939 section 3.5.1
type attributeList struct {
	mandatory, optional []string
}

// RFC 5939 section 3.5.1
type potentialConfig struct {
	number     int
	del        string
	attributes []attributeList
	transports []string
	hasAttrs   bool
}

// RFC 5939 section 3.5.1
func parsePotentialConfig(v string) (potentialConfig, bool) {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return potentialConfig{}, false
	}

	n, err := strconv.Atoi(fields[0])
	if err != nil || n < 1 {
		return potentialConfig{}, false
	}

	pc := potentialConfig{number: n}
	seen := map[string]bool{}

	for _, f := range fields[1:] {
		name, list, ok := strings.Cut(f, "=")
		if !ok || list == "" || seen[name] {
			return potentialConfig{}, false
		}

		seen[name] = true

		switch name {
		case "t":
			pc.transports = strings.Split(list, "|")
		case "a":
			if !parseAttributeConfig(&pc, list) {
				return potentialConfig{}, false
			}
		default:
			if strings.HasPrefix(name, "+") {
				return potentialConfig{}, false
			}
		}
	}

	return pc, true
}

func parseAttributeConfig(pc *potentialConfig, list string) bool {
	pc.hasAttrs = true

	if rest, ok := strings.CutPrefix(list, "-"); ok {
		del, caps, _ := strings.Cut(rest, ":")
		if del != "m" && del != "s" && del != "ms" {
			return false
		}

		pc.del, list = del, caps

		if list == "" {
			pc.attributes = []attributeList{{}}
			return true
		}
	}

	for alt := range strings.SplitSeq(list, "|") {
		var al attributeList

		mandatory, optional, hasOptional := strings.Cut(alt, "[")
		if hasOptional {
			inner, ok := strings.CutSuffix(optional, "]")
			if !ok {
				return false
			}

			al.optional = strings.Split(inner, ",")
		}

		if mandatory = strings.TrimSuffix(mandatory, ","); mandatory != "" {
			al.mandatory = strings.Split(mandatory, ",")
		}

		for _, n := range slices.Concat(al.mandatory, al.optional) {
			if v, err := strconv.Atoi(n); err != nil || v < 1 {
				return false
			}
		}

		pc.attributes = append(pc.attributes, al)
	}

	return true
}

// RFC 5939 section 3.6.2
func (pc potentialConfig) valid(caps capabilities) bool {
	for _, t := range pc.transports {
		if _, ok := caps.transports[t]; !ok {
			return false
		}
	}

	for _, al := range pc.attributes {
		for _, n := range slices.Concat(al.mandatory, al.optional) {
			c, ok := caps.attributes[n]
			if !ok {
				return false
			}

			if name := attrName(c.value); c.session && knownAttrs[name] && !sessionAttrs[name] {
				return false
			}
		}
	}

	return true
}

func attrName(par string) string {
	name, _, _ := strings.Cut(par, ":")
	return strings.ToLower(strings.TrimSpace(name))
}

type candidate struct {
	config    potentialConfig
	attrs     *attributeList
	transport string
	used      []string
}

func (pc potentialConfig) candidates() []candidate {
	attrs := []*attributeList{nil}
	if len(pc.attributes) > 0 {
		attrs = attrs[:0]

		for i := range pc.attributes {
			attrs = append(attrs, &pc.attributes[i])
		}
	}

	transports := pc.transports
	if len(transports) == 0 {
		transports = []string{""}
	}

	var out []candidate

	for _, a := range attrs {
		for _, t := range transports {
			out = append(out, candidate{config: pc, attrs: a, transport: t})
		}
	}

	return out
}

// RFC 5939 section 3.5.2
func (c candidate) acfg() string {
	out := "a=acfg:" + strconv.Itoa(c.config.number)

	if c.transport != "" {
		out += " t=" + c.transport
	}

	if c.config.hasAttrs {
		list := strings.Join(c.attrs.mandatory, ",")
		if len(c.used) > 0 {
			list = strings.TrimPrefix(list+",["+strings.Join(c.used, ",")+"]", ",")
		}

		if c.config.del != "" {
			list = "-" + c.config.del + ":" + list
			list = strings.TrimSuffix(list, ":")
		}

		out += " a=" + list
	}

	return out
}

// RFC 5939 section 3.6.2
type negotiated struct {
	offer   *sdp.Session
	media   [][]string
	session []string
}

// RFC 5939 section 3.6.2
func negotiate(offer *sdp.Session, accepts func(*sdp.Media) []uint8) negotiated {
	n := negotiated{offer: offer.Clone(), media: make([][]string, len(offer.Media))}

	defer stripCapNeg(n.offer)

	if !requiredSupported(offer.Lines) {
		n.session = []string{"a=csup:" + capNegBase}
		return n
	}

	var (
		delSession  bool
		sessionAdds []string
	)

	for i, m := range offer.Media {
		if !requiredSupported(m.Lines) {
			n.media[i] = []string{"a=csup:" + capNegBase}
			continue
		}

		c, media, adds, ok := selectConfig(offer, m, accepts)
		if !ok {
			continue
		}

		n.offer.Media[i] = media
		n.media[i] = []string{c.acfg()}
		delSession = delSession || strings.Contains(c.config.del, "s")

		sessionAdds = append(sessionAdds, adds...)
	}

	if delSession {
		n.offer.Lines = slices.DeleteFunc(n.offer.Lines, func(l sdp.Line) bool { return l.Type == 'a' })
	}

	n.offer.Lines = insertAttrs(n.offer.Lines, sessionAdds)

	return n
}

// RFC 5939 section 3.3.2
func requiredSupported(ls sdp.Lines) bool {
	for _, v := range ls.Attrs("creq") {
		for tag := range strings.SplitSeq(v, ",") {
			if strings.TrimSpace(tag) != capNegBase {
				return false
			}
		}
	}

	return true
}

// RFC 5939 section 3.5.1
// RFC 5939 section 3.6.2
func selectConfig(offer *sdp.Session, m *sdp.Media, accepts func(*sdp.Media) []uint8) (candidate, *sdp.Media, []string, bool) {
	caps := collectCapabilities(offer, m)

	var configs []potentialConfig

	count := map[int]int{}

	for _, v := range m.Attrs("pcfg") {
		if pc, ok := parsePotentialConfig(v); ok {
			configs = append(configs, pc)
			count[pc.number]++
		}
	}

	configs = slices.DeleteFunc(configs, func(pc potentialConfig) bool { return count[pc.number] > 1 || !pc.valid(caps) })
	slices.SortStableFunc(configs, func(a, b potentialConfig) int { return a.number - b.number })

	for _, pc := range configs {
		for _, c := range pc.candidates() {
			if media, adds, ok := c.apply(m, caps, accepts); ok {
				return c, media, adds, true
			}
		}
	}

	return candidate{}, nil, nil, false
}

// RFC 5939 section 3.6.2
func (c *candidate) apply(m *sdp.Media, caps capabilities, accepts func(*sdp.Media) []uint8) (*sdp.Media, []string, bool) {
	media := m.Clone()

	desc, err := media.Desc()
	if err != nil {
		return nil, nil, false
	}

	if c.transport != "" {
		desc.Proto = caps.transports[c.transport].value
		media.SetDesc(desc)
	}

	if !slices.ContainsFunc(supportedProtos, func(p string) bool { return strings.EqualFold(p, desc.Proto) }) {
		return nil, nil, false
	}

	if strings.Contains(c.config.del, "m") {
		media.Lines = slices.DeleteFunc(media.Lines, func(l sdp.Line) bool { return l.Type == 'a' })
	}

	var (
		mediaAdds, sessionAdds []string
		required               []string
	)

	c.used = nil

	if c.attrs != nil {
		for _, num := range c.attrs.mandatory {
			cp := caps.attributes[num]
			if !knownAttrs[attrName(cp.value)] {
				return nil, nil, false
			}

			if name := attrName(cp.value); name == "rtpmap" || name == "fmtp" {
				required = append(required, cp.value)
			}

			if cp.session && sessionAttrs[attrName(cp.value)] {
				sessionAdds = append(sessionAdds, "a="+cp.value)
			} else if !cp.session {
				mediaAdds = append(mediaAdds, "a="+cp.value)
			}
		}

		for _, num := range c.attrs.optional {
			cp := caps.attributes[num]
			if !knownAttrs[attrName(cp.value)] || cp.session && !sessionAttrs[attrName(cp.value)] {
				continue
			}

			c.used = append(c.used, num)

			if cp.session {
				sessionAdds = append(sessionAdds, "a="+cp.value)
			} else {
				mediaAdds = append(mediaAdds, "a="+cp.value)
			}
		}
	}

	media.Lines = insertAttrs(media.Lines, mediaAdds)

	pts := accepts(media)
	if len(pts) == 0 {
		return nil, nil, false
	}

	for _, r := range required {
		_, value, _ := strings.Cut(r, ":")
		pt, _, _ := strings.Cut(value, " ")

		if n, err := strconv.Atoi(pt); err != nil || !slices.Contains(pts, uint8(n)) {
			return nil, nil, false
		}
	}

	return media, sessionAdds, true
}

// RFC 5939 section 3.6.2
func insertAttrs(ls sdp.Lines, attrs []string) sdp.Lines {
	if len(attrs) == 0 {
		return ls
	}

	at := slices.IndexFunc(ls, func(l sdp.Line) bool { return l.Type == 'a' })
	if at < 0 {
		at = len(ls)
	}

	added := make(sdp.Lines, 0, len(attrs))
	for _, a := range attrs {
		added = append(added, sdp.Line{Type: 'a', Value: strings.TrimPrefix(a, "a=")})
	}

	return slices.Insert(slices.Clone(ls), at, added...)
}

// RFC 5939 section 3.6.2
func stripCapNeg(s *sdp.Session) {
	isCapNeg := func(l sdp.Line) bool {
		if l.Type != 'a' {
			return false
		}

		return slices.Contains(capNegAttrs, attrName(l.Value))
	}

	s.Lines = slices.DeleteFunc(s.Lines, isCapNeg)

	for _, m := range s.Media {
		m.Lines = slices.DeleteFunc(m.Lines, isCapNeg)
	}
}
