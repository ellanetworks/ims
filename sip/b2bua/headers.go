package b2bua

import (
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// Headers is what crosses from a message of one leg to the one the relay makes of it on the other. The zero value
// passes every header field and body part through (TS 24.229 §5.7.5.1), but those of one hop, one dialog or one
// leg's capabilities, which the agent sets on each leg (perLeg); the application drops or rewrites more.
type Headers struct {
	// Drop are header fields not to cross, beyond those of one hop or one leg.
	Drop []string

	// Field, when set, decides on each header field that may cross: false leaves it out.
	Field func(from, to *Leg, m sip.Message, f sip.Field) bool

	// Part, when set, decides on each body part other than the session description: false leaves it out. The parts
	// cross byte-exact (TS 24.379 §6.3.2.1.5: mcptt-info, resource-lists).
	Part func(from, to *Leg, m sip.Message, p sip.BodyPart) bool

	// Sticky are header fields of the initial INVITE of the incoming leg carried by every request the agent sends on
	// the outgoing one, those it makes on its own included (ACK, PRACK, BYE, refreshes): Resource-Priority without
	// it (RFC 4412 §4.7, TS 24.229 §5.7.5.1); an empty list for none.
	Sticky []string
}

// perLeg are the header fields that belong to one hop, one dialog or the capabilities of one leg's peer: the agent
// sets them on each leg, and the relay never copies them (RFC 3261 §8.1.1, §12, §16; RFC 7092 §3.1). Replaces and
// Target-Dialog name a dialog of one leg.
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

// copyHeader adds to dst the header fields of m that cross from one leg to the other, but those dst sets already: the
// application's.
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

// bodies are the body parts a message relayed from m carries: the parts of the application, then those of m that
// cross with no part of their media type among them, byte-exact, with sdp in place of the session description, or
// added first.
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

	parts, _ := m.Env().Content().Parts()

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

// sessionSDP is the session description of a message.
func sessionSDP(e sip.Envelope) []byte {
	p, ok, err := e.Content().FindFunc(sip.BodyPart.IsSession)
	if !ok || err != nil {
		return nil
	}

	return p.Body
}
