package testue

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/sip/sdp"
)

const telephoneEvent = "telephone-event"

// format is a payload type with its fmtp parameters.
type format struct {
	sdp.RTPMap

	fmtp string
}

// offered lists the formats of the test UE's offers: AMR-WB then AMR (IR.92
// §3.2.1), each with telephone-event at its clock rate (IR.92 §3.3).
var offered = []format{
	{RTPMap: sdp.RTPMap{Payload: 116, Encoding: "AMR-WB", ClockRate: 16000, Params: "1"}, fmtp: amrFmtp},
	{RTPMap: sdp.RTPMap{Payload: 118, Encoding: "AMR", ClockRate: 8000, Params: "1"}, fmtp: amrFmtp},
	{RTPMap: sdp.RTPMap{Payload: 111, Encoding: telephoneEvent, ClockRate: 16000}, fmtp: "0-15"},
	{RTPMap: sdp.RTPMap{Payload: 110, Encoding: telephoneEvent, ClockRate: 8000}, fmtp: "0-15"},
}

const amrFmtp = "mode-change-capability=2;max-red=220"

// mirroredAMR are the AMR parameters an answer copies from the offer (RFC 4867
// §8.3.1, TS 26.114 §6.2.2.2).
var mirroredAMR = []string{"octet-align", "mode-set", "crc", "robust-sorting", "interleaving", "channels"}

var ErrNoCodec = errors.New("testue: no acceptable audio codec in the offer")

// sessionIDs makes the o= session ids of a process unique (RFC 4566 §5.2).
var sessionIDs atomic.Uint64

func init() {
	sessionIDs.Store(uint64(time.Now().Unix()) << 16)
}

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

	// streams is the m= line layout of the session (RFC 3264 §8): the
	// rejected streams' m= lines, and "" for the audio stream.
	streams []string

	// audio is the negotiated audio format, kept in later offers.
	audioFormats []format

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
		session:      sessionIDs.Add(1),
		precondition: precondition,
		remoteQoS:    sdp.QoSNone,
		direction:    sdp.SendRecv,
		streams:      []string{""},
		audioFormats: offered,
	}
}

// met reports whether the preconditions are met on both segments.
func (m *media) met() bool {
	return !m.precondition || (m.reported == sdp.QoSSendRecv && m.remoteQoS == sdp.QoSSendRecv)
}

// held reports whether the peer holds the call: our last description
// receives only, or nothing (RFC 3264 §8.4).
func (m *media) held() bool {
	if m.local == nil {
		return false
	}

	for i, lm := range m.local.Media {
		if lm.Type() == sdp.Audio && lm.Port() != 0 {
			d := m.local.MediaDirection(i)
			return d == sdp.RecvOnly || d == sdp.Inactive
		}
	}

	return false
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

func (m *media) audio(formats []format, direction sdp.Direction, preconditions []string) []string {
	pts := make([]string, 0, len(formats))
	attrs := make([]string, 0, 2*len(formats))

	for _, f := range formats {
		pt := strconv.Itoa(int(f.Payload))
		pts = append(pts, pt)
		attrs = append(attrs, "a=rtpmap:"+f.String())

		if f.fmtp != "" {
			attrs = append(attrs, "a=fmtp:"+pt+" "+f.fmtp)
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

// offer builds an offer in direction, with the session's m= lines in their
// order (RFC 3264 §8). The first offer with preconditions has the remote
// segment optional, as IR.92 phones send it; later ones have both segments
// mandatory.
func (m *media) offer(direction sdp.Direction) (*sdp.Session, error) {
	strength := sdp.StrengthMandatory
	if m.sent == 0 {
		strength = sdp.StrengthOptional
	}

	lines := m.header()

	for _, s := range m.streams {
		if s != "" {
			lines = append(lines, s)
			continue
		}

		lines = append(lines, m.audio(m.audioFormats, direction, m.qos(strength, false))...)
	}

	return m.build(lines)
}

// answer answers offer: the first audio stream with a codec of ours is
// accepted, with the offer's payload types; every other stream is rejected
// with port 0 (RFC 3264 §6).
func (m *media) answer(offer *sdp.Session) (*sdp.Session, error) {
	lines := m.header()
	streams := make([]string, 0, len(offer.Media))
	accepted := false

	for i, om := range offer.Media {
		desc, err := om.Desc()
		if err != nil {
			return nil, fmt.Errorf("testue: offer: %w", err)
		}

		var formats []format

		if !accepted && desc.Type == sdp.Audio && om.Port() != 0 {
			formats = choose(om)
		}

		if formats == nil {
			desc.Port = 0
			lines = append(lines, "m="+desc.String())
			streams = append(streams, "m="+desc.String())

			continue
		}

		accepted = true

		if _, err := m.receive(offer, i); err != nil {
			return nil, err
		}

		// The answerer makes the remote segment mandatory and asks to be
		// told once the offerer's resources are reserved (RFC 3312 §6),
		// until they are.
		qos := m.qos(sdp.StrengthMandatory, m.remoteQoS != sdp.QoSSendRecv)

		lines = append(lines, m.audio(formats, sdp.Effective(offer.MediaDirection(i), m.direction), qos)...)
		streams = append(streams, "")
		m.audioFormats = formats
	}

	if !accepted {
		return nil, ErrNoCodec
	}

	m.remote, m.streams = offer, streams

	return m.build(lines)
}

// choose picks the offer's first codec of ours and its telephone-event. Of
// the payload types of that codec, it prefers bandwidth-efficient to
// octet-aligned (TS 26.114 §6.2.2.2), and it answers with the offer's
// parameters that must not change.
func choose(offer *sdp.Media) []format {
	codecs, _ := offer.Codecs()

	ours := func(c sdp.RTPMap) bool {
		return !strings.EqualFold(c.Encoding, telephoneEvent) && slices.ContainsFunc(offered, func(o format) bool {
			return strings.EqualFold(o.Encoding, c.Encoding) && o.ClockRate == c.ClockRate
		})
	}

	i := slices.IndexFunc(codecs, ours)
	if i < 0 {
		return nil
	}

	c := codecs[i]
	params := fmtpParams(offer, c.Payload)

	if params["octet-align"] == "1" {
		for _, o := range codecs[i+1:] {
			if strings.EqualFold(o.Encoding, c.Encoding) && o.ClockRate == c.ClockRate && fmtpParams(offer, o.Payload)["octet-align"] != "1" {
				c, params = o, fmtpParams(offer, o.Payload)
				break
			}
		}
	}

	fmtp := amrFmtp

	for _, name := range mirroredAMR {
		if v, ok := params[name]; ok {
			fmtp += ";" + name + "=" + v
		}
	}

	out := []format{{RTPMap: c, fmtp: fmtp}}

	for _, e := range codecs {
		if strings.EqualFold(e.Encoding, telephoneEvent) && e.ClockRate == c.ClockRate {
			out = append(out, format{RTPMap: e, fmtp: "0-15"})
			break
		}
	}

	return out
}

func fmtpParams(m *sdp.Media, pt uint8) map[string]string {
	out := map[string]string{}

	v, ok := m.Fmtp(strconv.Itoa(int(pt)))
	if !ok {
		return out
	}

	for p := range strings.SplitSeq(v, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(p), "=")
		if name != "" {
			out[strings.ToLower(name)] = strings.TrimSpace(value)
		}
	}

	return out
}

// receive records the peer's curr:qos local status in stream i, which is the
// status of our remote segment, and reports whether the stream has qos
// preconditions.
func (m *media) receive(s *sdp.Session, i int) (bool, error) {
	pres, err := s.Media[i].Preconditions()
	if err != nil {
		return false, fmt.Errorf("testue: preconditions: %w", err)
	}

	found := false

	for _, p := range pres {
		if p.Type != sdp.QoS {
			continue
		}

		found = true

		if m.precondition && p.Kind == sdp.Current && p.Status == sdp.StatusLocal {
			m.remoteQoS = p.Direction
		}
	}

	return found, nil
}

// answered records the peer's answer to our offer. An answer to the first
// offer without preconditions means the peer does not support them (RFC 3312
// §11).
func (m *media) answered(answer *sdp.Session) error {
	first := m.remote == nil
	m.remote = answer

	for i, am := range answer.Media {
		if am.Port() == 0 || am.Type() != sdp.Audio {
			continue
		}

		found, err := m.receive(answer, i)
		if err != nil {
			return err
		}

		if first && !found {
			m.precondition = false
		}

		return nil
	}

	return nil
}
