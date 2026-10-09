package testue

import (
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/sip/sdp"
)

// The answerer side of SDP Capability Negotiation (RFC 5939), which an MTSI client supporting it supports completely
// (TS 26.114 §6.2.1a.1). The UE never offers capability negotiation: it offers AVPF for video directly (§6.2.1a.2).

// capNegBase is the option tag of the base framework, the only one the UE supports (RFC 5939 §3.3.1).
const capNegBase = "cap-v0"

// capNegAttrs are the capability negotiation attributes, removed from the offer the answer is made to (RFC 5939
// §3.6.2).
var capNegAttrs = []string{"csup", "creq", "acap", "tcap", "pcfg", "acfg"}

// knownAttrs are the attributes the UE negotiates. A potential configuration needing another one is not supported
// (RFC 5939 §3.6.2).
var knownAttrs = map[string]bool{
	"rtpmap": true, "fmtp": true, "ptime": true, "maxptime": true, "rtcp-fb": true, "extmap": true, "rtcp": true,
	"curr": true, "des": true, "conf": true, "framerate": true, "imageattr": true,
	"sendrecv": true, "sendonly": true, "recvonly": true, "inactive": true,
}

// sessionAttrs are the known attributes that may be at the session level (RFC 3264 §5.1, RFC 8285 §5); the others
// are media-level only (RFC 5939 §3.6.2 item 3).
var sessionAttrs = map[string]bool{"sendrecv": true, "sendonly": true, "recvonly": true, "inactive": true, "extmap": true}

var supportedProtos = []string{avp, avpf}

type capability struct {
	value   string
	session bool
}

// capabilities are the tcap and acap capabilities a media description can reference: its own, and the session's
// (RFC 5939 §3.4.1, §3.4.2).
type capabilities struct {
	transports map[string]capability
	attributes map[string]capability
}

func collectCapabilities(offer *sdp.Session, m *sdp.Media) capabilities {
	c := capabilities{transports: map[string]capability{}, attributes: map[string]capability{}}

	for _, level := range []struct {
		lines   sdp.Lines
		session bool
	}{{offer.Lines, true}, {m.Lines, false}} {
		// RFC 5939 §3.4.2: a=tcap:<trpr-cap-num> <proto-list>, the protocols numbered from the first.
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

		// RFC 5939 §3.4.1: a=acap:<att-cap-num> <att-par>.
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

// attributeList is one alternative attribute capability list of a potential configuration (RFC 5939 §3.5.1).
type attributeList struct {
	mandatory, optional []string
}

// potentialConfig is an a=pcfg attribute (RFC 5939 §3.5.1).
type potentialConfig struct {
	number int
	// del is the delete-attributes of the attribute configuration list: "", "m", "s" or "ms".
	del        string
	attributes []attributeList
	transports []string
	hasAttrs   bool
}

// parsePotentialConfig parses an a=pcfg value. It is invalid when its syntax is, or it has a list twice or an
// unsupported mandatory extension (RFC 5939 §3.5.1, §3.6.2).
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
			// RFC 5939 §3.5.1: an unknown extension is ignored, unless it is mandatory.
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

// valid tells whether the potential configuration references only capabilities the media description can, and
// session-level attribute capabilities only of session-level attributes (RFC 5939 §3.6.2 items 3 and 4).
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

// candidate is one alternative of a potential configuration: an attribute list and a transport, either absent.
type candidate struct {
	config    potentialConfig
	attrs     *attributeList
	transport string
	// used are the optional attribute capabilities the UE takes.
	used []string
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

// acfg is the actual configuration attribute of the answer: the configuration number, and the lists used, with the
// alternatives taken and the known optional capabilities only (RFC 5939 §3.5.2).
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

// negotiated is an offer as the potential configurations the answerer selects leave it (RFC 5939 §3.6.2).
type negotiated struct {
	// offer is the offer the answer is made to, without capability negotiation attributes.
	offer *sdp.Session
	// media are the capability negotiation attributes of each m-line's answer: the actual configuration attribute
	// when it uses a potential configuration, the supported option tags when the media description required
	// others, nothing otherwise.
	media [][]string
	// session are those of the answer's session level: the supported option tags, when the session required others
	// (RFC 5939 §3.6.2).
	session []string
}

// negotiate selects, for each m-line, the most preferred valid potential configuration the UE supports, accepts
// telling whether the UE would take the media description, with the payload types the formats it takes have (RFC
// 5939 §3.6.2). An m-line without one keeps its actual configuration.
func negotiate(offer *sdp.Session, accepts func(*sdp.Media) []uint8) negotiated {
	n := negotiated{offer: offer.Clone(), media: make([][]string, len(offer.Media))}

	defer stripCapNeg(n.offer)

	// RFC 5939 §3.6.2: a required extension the answerer lacks at the session level rules out every potential
	// configuration.
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

// requiredSupported tells whether the UE supports every extension a=creq requires (RFC 5939 §3.3.2).
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

// selectConfig returns the most preferred valid and supported potential configuration of the media description: the
// lowest configuration number, then the alternatives in order (RFC 5939 §3.5.1, §3.6.2). It returns the media
// description it leads to, and the session-level attributes it adds.
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

	// RFC 5939 §3.6.2 item 2: the configuration number is unique within the media description.
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

// apply builds the media description of the candidate and checks the UE supports it: its transport, its mandatory
// attribute capabilities, and the formats any mandatory rtpmap or fmtp names (RFC 5939 §3.6.2). It sets the optional
// capabilities used.
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

		// RFC 5939 §3.5.1, §3.6.2: optional capabilities are taken when known.
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

	// RFC 5939 §3.6.2: a mandatory rtpmap or fmtp capability is for a format negotiated successfully.
	for _, r := range required {
		_, value, _ := strings.Cut(r, ":")
		pt, _, _ := strings.Cut(value, " ")

		if n, err := strconv.Atoi(pt); err != nil || !slices.Contains(pts, uint8(n)) {
			return nil, nil, false
		}
	}

	return media, sessionAdds, true
}

// insertAttrs adds the attributes before the attributes already there, in order (RFC 5939 §3.6.2).
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

// stripCapNeg removes the capability negotiation attributes (RFC 5939 §3.6.2).
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
