package b2bua

import (
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// TS 24.229 clause 5.7.5.1
type Headers struct {
	Drop []string

	Field func(from, to *Leg, m sip.Message, f sip.Field) bool

	Part func(from, to *Leg, m sip.Message, p sip.BodyPart) bool

	Sticky []string
}

// RFC 3261 section 8.1.1
// RFC 3261 section 12
// RFC 3261 section 16
// RFC 7092 section 3.1
var perLeg = []string{
	"Via", "Route", "Record-Route", "Contact", "From", "To", "Call-ID", "CSeq", "Max-Forwards",
	"Content-Length", "Content-Type", "Content-Disposition", "Content-Encoding", "Content-Language", "Content-ID",
	"MIME-Version", "Supported", "Require", "Proxy-Require", "Unsupported", "Allow", "Allow-Events",
	"Session-Expires", "Min-SE", "RSeq", "RAck", "Authorization", "Proxy-Authorization", "WWW-Authenticate",
	"Proxy-Authenticate", "Authentication-Info", "Path", "Service-Route", "Security-Client", "Security-Server",
	"Security-Verify", "Replaces", "Target-Dialog", "Refer-Sub", "Event", "Subscription-State", "Timestamp",
	"User-Agent", "Server", "Expires", "Min-Expires",
}

var compactPerLeg = []string{"v", "f", "t", "i", "m", "l", "c", "e", "k", "o", "x"}

func (h Headers) crosses(from, to *Leg, m sip.Message, f sip.Field) bool {
	name := f.Name

	switch {
	case slices.ContainsFunc(perLeg, func(n string) bool { return strings.EqualFold(n, name) }),
		len(name) == 1 && slices.ContainsFunc(compactPerLeg, func(n string) bool { return strings.EqualFold(n, name) }),
		slices.ContainsFunc(h.Drop, func(n string) bool { return strings.EqualFold(n, name) }):
		return false
	case h.Field != nil:
		return h.Field(from, to, m, f)
	}

	return true
}

func (h Headers) copyHeader(dst *sip.Header, from, to *Leg, m sip.Message) {
	have := map[string]bool{}
	for _, f := range *dst {
		have[strings.ToLower(f.Name)] = true
	}

	for _, f := range m.Env().Header {
		if !have[strings.ToLower(f.Name)] && h.crosses(from, to, m, f) {
			dst.Add(f.Name, f.Value)
		}
	}
}

func (h Headers) sticky() []string {
	if h.Sticky == nil {
		return []string{"Resource-Priority"}
	}

	return h.Sticky
}

// TS 24.229 clause 5.10.6.3
func (h Headers) bodies(from, to *Leg, m sip.Message, own []sip.BodyPart, sdp []byte) []sip.BodyPart {
	var out []sip.BodyPart

	placed := false

	for _, part := range own {
		if part.IsSession() {
			if sdp != nil && !placed {
				out = append(out, ua.SDP(sdp))
				placed = true
			}

			continue
		}

		out = append(out, part)
	}

	parts, _ := m.Env().Content().Split()

	for _, part := range parts {
		switch {
		case part.IsSession():
			if sdp != nil && !placed {
				out = append(out, ua.SDP(sdp))
				placed = true
			}
		case slices.ContainsFunc(own, func(p sip.BodyPart) bool { return strings.EqualFold(p.MediaType(), part.MediaType()) }):
		case h.Part == nil || h.Part(from, to, m, part):
			out = append(out, part)
		}
	}

	if sdp != nil && !placed {
		out = append([]sip.BodyPart{ua.SDP(sdp)}, out...)
	}

	return out
}

func sessionSDP(e sip.Envelope) []byte {
	p, ok, err := e.Content().FindFunc(sip.BodyPart.IsSession)
	if !ok || err != nil {
		return nil
	}

	return p.Body
}
