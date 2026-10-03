package testue

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/sip/sdp"
)

const telephoneEvent = "telephone-event"

// offered lists the formats of the test UE's offers: AMR-WB then AMR (IR.92
// §3.2.1), each with telephone-event at its clock rate (IR.92 §3.3).
var offered = []sdp.RTPMap{
	{Payload: 116, Encoding: "AMR-WB", ClockRate: 16000, Params: "1"},
	{Payload: 118, Encoding: "AMR", ClockRate: 8000, Params: "1"},
	{Payload: 111, Encoding: telephoneEvent, ClockRate: 16000},
	{Payload: 110, Encoding: telephoneEvent, ClockRate: 8000},
}

var ErrNoCodec = errors.New("testue: no acceptable audio codec in the offer")

// media is the SDP state of a call (RFC 3264) and its qos preconditions (RFC
// 3312). The test UE has no bearer to wait for: its first description
// reports its resources as not reserved, as a phone does before the dedicated
// bearer is up, and every later one reports them reserved.
type media struct {
	addr    netip.Addr
	port    uint16
	session uint64
	version uint64

	local, remote *sdp.Session

	precondition bool
	sent         int
	reported     string
	remoteQoS    string

	// direction is the direction this side wants: sendonly while holding.
	direction sdp.Direction
}

func newMedia(addr netip.Addr, port uint16, precondition bool) *media {
	return &media{
		addr:         addr,
		port:         port,
		session:      uint64(time.Now().Unix()),
		precondition: precondition,
		remoteQoS:    sdp.QoSNone,
		direction:    sdp.SendRecv,
	}
}

// met reports whether the preconditions are met on both segments.
func (m *media) met() bool {
	return !m.precondition || (m.reported == sdp.QoSSendRecv && m.remoteQoS == sdp.QoSSendRecv)
}

func (m *media) header() []string {
	ip := sdp.IP4
	if m.addr.Is6() {
		ip = sdp.IP6
	}

	return []string{
		"v=0",
		"o=- " + strconv.FormatUint(m.session, 10) + " " + strconv.FormatUint(m.version, 10) + " IN " + ip + " " + m.addr.String(),
		"s=-",
		"c=IN " + ip + " " + m.addr.String(),
		"t=0 0",
	}
}

func (m *media) audio(formats []sdp.RTPMap, direction sdp.Direction, preconditions []string) []string {
	pts := make([]string, 0, len(formats))
	attrs := make([]string, 0, 2*len(formats))

	for _, f := range formats {
		pt := strconv.Itoa(int(f.Payload))
		pts = append(pts, pt)
		attrs = append(attrs, "a=rtpmap:"+f.String())

		switch {
		case strings.HasPrefix(strings.ToUpper(f.Encoding), "AMR"):
			attrs = append(attrs, "a=fmtp:"+pt+" mode-change-capability=2;max-red=220")
		case strings.EqualFold(f.Encoding, telephoneEvent):
			attrs = append(attrs, "a=fmtp:"+pt+" 0-15")
		}
	}

	out := []string{
		"m=audio " + strconv.Itoa(int(m.port)) + " RTP/AVP " + strings.Join(pts, " "),
		"b=AS:41",
		"b=RS:512",
		"b=RR:1537",
	}

	out = append(out, attrs...)
	out = append(out, preconditions...)

	return append(out, "a="+string(direction), "a=ptime:20", "a=maxptime:240")
}

// qos returns the precondition lines of the next description.
func (m *media) qos(remote string, conf bool) []string {
	if !m.precondition {
		return nil
	}

	local := sdp.QoSNone
	if m.sent > 0 {
		local = sdp.QoSSendRecv
	}

	out := []string{
		"a=curr:qos local " + local,
		"a=curr:qos remote " + m.remoteQoS,
		"a=des:qos mandatory local sendrecv",
		"a=des:qos " + remote + " remote sendrecv",
	}

	if conf {
		out = append(out, "a=conf:qos remote sendrecv")
	}

	return out
}

func (m *media) build(lines []string) (*sdp.Session, error) {
	s, err := sdp.Parse([]byte(strings.Join(lines, "\r\n") + "\r\n"))
	if err != nil {
		return nil, fmt.Errorf("testue: build SDP: %w", err)
	}

	if m.precondition {
		m.reported = sdp.QoSNone
		if m.sent > 0 {
			m.reported = sdp.QoSSendRecv
		}
	}

	m.version++
	m.sent++
	m.local = s

	return s, nil
}

// offer builds an offer in the direction this side wants. The first offer
// with preconditions has the remote segment optional, as IR.92 phones send
// it; later ones have both segments mandatory.
func (m *media) offer() (*sdp.Session, error) {
	strength := sdp.StrengthMandatory
	if m.sent == 0 {
		strength = sdp.StrengthOptional
	}

	return m.build(append(m.header(), m.audio(offered, m.direction, m.qos(strength, false))...))
}

// answer answers offer: the first audio stream with a codec of ours is
// accepted, with the offer's payload types; every other stream is rejected
// with port 0 (RFC 3264 §6).
func (m *media) answer(offer *sdp.Session) (*sdp.Session, error) {
	lines := m.header()
	accepted := false

	for i, om := range offer.Media {
		desc, err := om.Desc()
		if err != nil {
			return nil, fmt.Errorf("testue: offer: %w", err)
		}

		var formats []sdp.RTPMap

		if !accepted && desc.Type == sdp.Audio && om.Port() != 0 {
			formats = choose(om)
		}

		if formats == nil {
			desc.Port = 0
			lines = append(lines, "m="+desc.String())

			continue
		}

		accepted = true

		if err := m.receive(offer, i); err != nil {
			return nil, err
		}

		// The answerer makes the remote segment mandatory and asks to be
		// told once the offerer's resources are reserved (RFC 3312 §6),
		// until they are.
		qos := m.qos(sdp.StrengthMandatory, m.remoteQoS != sdp.QoSSendRecv)

		lines = append(lines, m.audio(formats, sdp.Effective(offer.MediaDirection(i), m.direction), qos)...)
	}

	if !accepted {
		return nil, ErrNoCodec
	}

	m.remote = offer

	return m.build(lines)
}

// choose picks the offer's first codec of ours and its telephone-event.
func choose(offer *sdp.Media) []sdp.RTPMap {
	codecs, _ := offer.Codecs()

	for _, c := range codecs {
		if strings.EqualFold(c.Encoding, telephoneEvent) || !slices.ContainsFunc(offered, func(o sdp.RTPMap) bool {
			return strings.EqualFold(o.Encoding, c.Encoding) && o.ClockRate == c.ClockRate
		}) {
			continue
		}

		out := []sdp.RTPMap{c}

		for _, e := range codecs {
			if strings.EqualFold(e.Encoding, telephoneEvent) && e.ClockRate == c.ClockRate {
				out = append(out, e)
				break
			}
		}

		return out
	}

	return nil
}

// receive records the peer's curr:qos local status in stream i, which is the
// status of our remote segment.
func (m *media) receive(s *sdp.Session, i int) error {
	pres, err := s.Media[i].Preconditions()
	if err != nil {
		return fmt.Errorf("testue: preconditions: %w", err)
	}

	for _, p := range pres {
		if p.Type != sdp.QoS {
			continue
		}

		m.precondition = true

		if p.Kind == sdp.Current && p.Status == sdp.StatusLocal {
			m.remoteQoS = p.Direction
		}
	}

	return nil
}

// answered records the peer's answer to our offer.
func (m *media) answered(answer *sdp.Session) error {
	m.remote = answer

	for i, am := range answer.Media {
		if am.Port() != 0 {
			return m.receive(answer, i)
		}
	}

	return nil
}
