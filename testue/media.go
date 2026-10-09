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

type format struct {
	sdp.RTPMap

	fmtp string
}

var offered = []format{
	{RTPMap: sdp.RTPMap{Payload: 116, Encoding: "AMR-WB", ClockRate: 16000, Params: "1"}, fmtp: amrFmtp},
	{RTPMap: sdp.RTPMap{Payload: 118, Encoding: "AMR", ClockRate: 8000, Params: "1"}, fmtp: amrFmtp},
	{RTPMap: sdp.RTPMap{Payload: 111, Encoding: telephoneEvent, ClockRate: 16000}, fmtp: "0-15"},
	{RTPMap: sdp.RTPMap{Payload: 110, Encoding: telephoneEvent, ClockRate: 8000}, fmtp: "0-15"},
}

const amrFmtp = "mode-change-capability=2;max-red=220"

// NG.114 §3.3.2.1 order: H.265 Main Profile Level 3.1, H.264 Constrained High Profile Level 3.1, then H.264 Constrained
// Baseline Profile Level 3.1 (IR.94 §3.3.1).
var offeredVideo = []format{
	{RTPMap: sdp.RTPMap{Payload: 112, Encoding: h265, ClockRate: 90000}, fmtp: "profile-id=1;level-id=93"},
	{RTPMap: sdp.RTPMap{Payload: 113, Encoding: h264, ClockRate: 90000}, fmtp: "profile-level-id=640c1f;packetization-mode=1"},
	{RTPMap: sdp.RTPMap{Payload: 114, Encoding: h264, ClockRate: 90000}, fmtp: "profile-level-id=42e01f;packetization-mode=1"},
}

const (
	h264 = "H264"
	h265 = "H265"

	avp  = "RTP/AVP"
	avpf = "RTP/AVPF"

	// IR.94 §2.4.2, TS 26.114 §6.2.3: coordination of video orientation with 2 bits granularity.
	cvoURN = "urn:3gpp:video-orientation"
	cvoID  = "7"
)

// mirroredVideo are the fmtp parameters of the chosen video format the answer keeps (RFC 6184 §8.2.2, RFC 7798
// §7.2.2); the offerer's sprop parameter sets describe its own stream.
var mirroredVideo = map[string][]string{
	h264: {"profile-level-id", "packetization-mode"},
	h265: {"profile-id", "level-id"},
}

var mirroredAMR = []string{"octet-align", "mode-set", "crc", "robust-sorting", "interleaving", "channels"}

var ErrNoCodec = errors.New("testue: no acceptable audio codec in the offer")

var ErrNoStream = errors.New("testue: no such stream")

var sessionIDs atomic.Uint64

func init() {
	sessionIDs.Store(uint64(time.Now().Unix()) << 16)
}

// stream is one m-line of the session (RFC 3264 §5): ours while it carries media, or a rejected or removed one kept
// at its position with port 0 (RFC 3264 §8.2).
type stream struct {
	kind  string
	proto string
	// cvo is the extmap ID of the video orientation extension (IR.94 §2.4.2), empty when the stream has none.
	cvo string
	// disabled is the m-line replayed at port 0 once the stream is rejected or removed; empty while it is active.
	disabled string

	formats   []format
	direction sdp.Direction

	// RFC 3312: the preconditions are per m-line.
	sent      int
	reported  string
	remoteQoS string
}

func newStream(kind string, formats []format) *stream {
	s := &stream{kind: kind, proto: avp, formats: formats, direction: sdp.SendRecv, remoteQoS: sdp.QoSNone}

	// IR.94 §3.3.2: video is offered with the AVPF profile.
	if kind == sdp.Video {
		s.proto, s.cvo = avpf, cvoID
	}

	return s
}

func (s *stream) active() bool { return s.disabled == "" }

// disable keeps the m-line at its position with port 0 (RFC 3264 §8.2).
func (s *stream) disable(desc sdp.MediaDesc) {
	desc.Port = 0
	s.disabled = "m=" + desc.String()
}

func (s *stream) desc() sdp.MediaDesc {
	pts := make([]string, 0, len(s.formats))
	for _, f := range s.formats {
		pts = append(pts, strconv.Itoa(int(f.Payload)))
	}

	return sdp.MediaDesc{Type: s.kind, Proto: s.proto, Formats: pts}
}

func (s *stream) met() bool {
	return s.reported == sdp.QoSSendRecv && s.remoteQoS == sdp.QoSSendRecv
}

type media struct {
	addr    netip.Addr
	port    uint16
	session uint64
	version uint64

	local, remote *sdp.Session

	streams []*stream

	precondition bool
	// video tells whether the UE takes video streams (IR.94 §2.2.2): without it, video m-lines are answered with port 0.
	video bool
}

func newMedia(addr netip.Addr, port uint16, precondition, video bool) *media {
	return &media{
		addr:         addr,
		port:         port,
		session:      sessionIDs.Add(1),
		precondition: precondition,
		video:        video,
		streams:      []*stream{newStream(sdp.Audio, offered)},
	}
}

// find returns the index of the active stream of the kind, or -1.
func (m *media) find(kind string) int {
	return slices.IndexFunc(m.streams, func(s *stream) bool { return s.active() && s.kind == kind })
}

// addVideo adds a video stream, in the m-line of a removed one if any (RFC 3264 §8.1, §8.3).
func (m *media) addVideo() error {
	if !m.video {
		return fmt.Errorf("%w: video is not enabled on the UE", ErrCallState)
	}

	if m.find(sdp.Video) >= 0 {
		return fmt.Errorf("%w: the call has video", ErrCallState)
	}

	s := newStream(sdp.Video, offeredVideo)

	if i := slices.IndexFunc(m.streams, func(s *stream) bool { return !s.active() && s.kind == sdp.Video }); i >= 0 {
		m.streams[i] = s
		return nil
	}

	m.streams = append(m.streams, s)

	return nil
}

// remove sets the port of the active stream of the kind to zero (RFC 3264 §8.2).
func (m *media) remove(kind string) error {
	i := m.find(kind)
	if i < 0 {
		return fmt.Errorf("%w: no active %s stream", ErrNoStream, kind)
	}

	m.streams[i].disable(m.streams[i].desc())

	return nil
}

func cloneStreams(streams []*stream) []*stream {
	out := make([]*stream, len(streams))

	for i, s := range streams {
		c := *s
		c.formats = slices.Clone(s.formats)
		out[i] = &c
	}

	return out
}

// fork copies the media state for a separate dialog created by the same offer.
func (m *media) fork() *media {
	f := *m
	f.streams = cloneStreams(m.streams)

	return &f
}

// mediaState is what an offer changes, restored when the offer fails.
type mediaState struct {
	streams []*stream
	local   *sdp.Session
}

func (m *media) save() mediaState {
	return mediaState{streams: cloneStreams(m.streams), local: m.local}
}

func (m *media) restore(st mediaState) {
	m.streams, m.local = st.streams, st.local
}

func (m *media) active() []*stream {
	var out []*stream

	for _, s := range m.streams {
		if s.active() {
			out = append(out, s)
		}
	}

	return out
}

// met tells whether the preconditions of every active stream are met (RFC 3312 §5).
func (m *media) met() bool {
	if !m.precondition {
		return true
	}

	for _, s := range m.active() {
		if !s.met() {
			return false
		}
	}

	return true
}

// remoteMet tells whether the remote end reports its resources reserved for every active stream.
func (m *media) remoteMet() bool {
	for _, s := range m.active() {
		if s.remoteQoS != sdp.QoSSendRecv {
			return false
		}
	}

	return true
}

// setDirection sets the direction every active stream asks for, as on hold and resume (IR.94 §2.3.2).
func (m *media) setDirection(d sdp.Direction) {
	for _, s := range m.active() {
		s.direction = d
	}
}

// offeredDirection is the direction offered for stream i: a stream put on hold while the remote end holds it becomes inactive
// (RFC 3264 §8.4).
func (m *media) offeredDirection(i int) sdp.Direction {
	d := m.streams[i].direction
	if d != sdp.SendOnly || m.local == nil || i >= len(m.local.Media) || m.local.Media[i].Port() == 0 {
		return d
	}

	if l := m.local.MediaDirection(i); l == sdp.RecvOnly || l == sdp.Inactive {
		return sdp.Inactive
	}

	return d
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

// portFor gives each kind of stream its own RTP port, RTCP on the next one (RFC 3550 §11).
func (m *media) portFor(kind string) uint16 {
	if kind == sdp.Video {
		return m.port + 2
	}

	return m.port
}

// mline renders an active stream; extra are attributes only this SDP carries, as an answer's acfg (RFC 5939 §3.6.2).
func (m *media) mline(s *stream, direction sdp.Direction, preconditions, extra []string) []string {
	desc := s.desc()
	desc.Port = m.portFor(s.kind)

	out := []string{"m=" + desc.String()}

	// TS 26.114 §6.2.5, IR.92 §3.2.4, NG.114 §3.3.2.1: b=AS for the media, b=RS and b=RR for its RTCP.
	if s.kind == sdp.Video {
		out = append(out, "b=AS:560", "b=RS:5200", "b=RR:6000")
	} else {
		out = append(out, "b=AS:41", "b=RS:512", "b=RR:1537")
	}

	for _, f := range s.formats {
		out = append(out, "a=rtpmap:"+f.String())

		if f.fmtp != "" {
			out = append(out, "a=fmtp:"+strconv.Itoa(int(f.Payload))+" "+f.fmtp)
		}
	}

	// IR.94 §3.3.3: NACK, PLI, FIR and TMMBR feedback with the AVPF profile.
	if s.proto == avpf {
		out = append(out, "a=rtcp-fb:* nack", "a=rtcp-fb:* nack pli", "a=rtcp-fb:* ccm fir", "a=rtcp-fb:* ccm tmmbr")
	}

	if s.cvo != "" {
		out = append(out, "a=extmap:"+s.cvo+" "+cvoURN)
	}

	out = append(out, preconditions...)
	out = append(out, extra...)
	out = append(out, "a="+string(direction))

	if s.kind == sdp.Audio {
		out = append(out, "a=ptime:20", "a=maxptime:240")
	}

	return out
}

func (m *media) qos(s *stream, remote string, conf bool) []string {
	if !m.precondition {
		return nil
	}

	local := sdp.QoSNone
	if s.sent > 0 {
		local = sdp.QoSSendRecv
	}

	out := []string{
		"a=curr:qos local " + local,
		"a=curr:qos remote " + s.remoteQoS,
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

	for _, st := range m.active() {
		if m.precondition {
			st.reported = sdp.QoSNone
			if st.sent > 0 {
				st.reported = sdp.QoSSendRecv
			}
		}

		st.sent++
	}

	m.version++
	m.local = s

	return s, nil
}

// RFC 3264, IR.92
func (m *media) offer() (*sdp.Session, error) {
	lines := m.header()

	for i, s := range m.streams {
		if !s.active() {
			lines = append(lines, s.disabled)
			continue
		}

		strength := sdp.StrengthMandatory
		if s.sent == 0 {
			strength = sdp.StrengthOptional
		}

		lines = append(lines, m.mline(s, m.offeredDirection(i), m.qos(s, strength, false), nil)...)
	}

	return m.build(lines)
}

// RFC 3264
func (m *media) answer(offer *sdp.Session) (*sdp.Session, error) {
	lines := m.header()
	streams := make([]*stream, 0, len(offer.Media))
	accepted := map[string]bool{}

	for i, om := range offer.Media {
		desc, err := om.Desc()
		if err != nil {
			return nil, fmt.Errorf("testue: offer: %w", err)
		}

		var formats []format

		switch {
		case accepted[desc.Type] || om.Port() == 0:
		case desc.Type == sdp.Audio:
			formats = choose(om)
		case desc.Type == sdp.Video && m.video:
			formats = chooseVideo(om)
		}

		if formats == nil {
			s := &stream{kind: desc.Type}
			s.disable(desc)
			lines = append(lines, s.disabled)
			streams = append(streams, s)

			continue
		}

		accepted[desc.Type] = true

		// An m-line keeps its stream, and the stream its state, from one offer to the next (RFC 3264 §8).
		s := newStream(desc.Type, nil)
		if i < len(m.streams) && m.streams[i].active() && m.streams[i].kind == desc.Type {
			s = m.streams[i]
		}

		var extra []string

		s.formats = formats
		s.proto, extra = answerProto(om, desc.Proto)

		if s.kind == sdp.Video {
			s.cvo = cvo(om)
		}

		if _, err := m.receive(s, offer, i); err != nil {
			return nil, err
		}

		qos := m.qos(s, sdp.StrengthMandatory, s.remoteQoS != sdp.QoSSendRecv)

		lines = append(lines, m.mline(s, sdp.Effective(offer.MediaDirection(i), s.direction), qos, extra)...)
		streams = append(streams, s)
	}

	if !accepted[sdp.Audio] {
		return nil, ErrNoCodec
	}

	m.remote, m.streams = offer, streams

	return m.build(lines)
}

// TS 26.114 §6.2.2.2
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

// IR.94 §3.3.1, NG.114 §3.3.2.1: the first offered H.265 or H.264 format, H.264 with packetization mode 1
// (RFC 6184 §6.3).
func chooseVideo(offer *sdp.Media) []format {
	codecs, _ := offer.Codecs()

	for _, c := range codecs {
		if c.ClockRate != 90000 {
			continue
		}

		var names []string

		for enc, ps := range mirroredVideo {
			if strings.EqualFold(c.Encoding, enc) {
				names = ps
			}
		}

		params := fmtpParams(offer, c.Payload)

		if names == nil || strings.EqualFold(c.Encoding, h264) && params["packetization-mode"] != "1" {
			continue
		}

		var fmtp []string

		for _, name := range names {
			if v, ok := params[name]; ok {
				fmtp = append(fmtp, name+"="+v)
			}
		}

		return []format{{RTPMap: c, fmtp: strings.Join(fmtp, ";")}}
	}

	return nil
}

// answerProto answers the transport of an offered m-line: an AVP m-line offering AVPF through SDP capability
// negotiation is answered with AVPF and the configuration taken (RFC 5939 §3.6.2, IR.94 §3.3.2).
func answerProto(offer *sdp.Media, proto string) (string, []string) {
	if !strings.EqualFold(proto, avp) {
		return proto, nil
	}

	caps := map[string]string{}

	for _, v := range offer.Attrs("tcap") {
		fields := strings.Fields(v)
		if len(fields) < 2 {
			continue
		}

		first, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		for j, p := range fields[1:] {
			caps[strconv.Itoa(first+j)] = p
		}
	}

	for _, v := range offer.Attrs("pcfg") {
		fields := strings.Fields(v)
		if len(fields) < 2 {
			continue
		}

		for _, f := range fields[1:] {
			alts, ok := strings.CutPrefix(f, "t=")
			if !ok {
				continue
			}

			for t := range strings.SplitSeq(alts, "|") {
				if strings.EqualFold(caps[t], avpf) {
					return avpf, []string{"a=acfg:" + fields[0] + " t=" + t}
				}
			}
		}
	}

	return proto, nil
}

// cvo returns the extmap ID the offer gives video orientation, empty when it offers none (RFC 8285 §6).
func cvo(offer *sdp.Media) string {
	for _, v := range offer.Attrs("extmap") {
		fields := strings.Fields(v)
		if len(fields) >= 2 && strings.EqualFold(fields[1], cvoURN) {
			id, _, _ := strings.Cut(fields[0], "/")
			return id
		}
	}

	return ""
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

func (m *media) receive(st *stream, s *sdp.Session, i int) (bool, error) {
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
			st.remoteQoS = p.Direction
		}
	}

	return found, nil
}

// RFC 3264 §6, RFC 3312 §11: a stream the answer rejects stays at port 0; preconditions are dropped when the first
// answer has none on the streams it accepts.
func (m *media) answered(answer *sdp.Session) error {
	first := m.remote == nil
	m.remote = answer

	accepted, found := false, false

	for i, am := range answer.Media {
		if i >= len(m.streams) || !m.streams[i].active() {
			continue
		}

		s := m.streams[i]

		if am.Port() == 0 {
			desc, err := am.Desc()
			if err != nil {
				return fmt.Errorf("testue: answer: %w", err)
			}

			s.disable(desc)

			continue
		}

		accepted = true

		f, err := m.receive(s, answer, i)
		if err != nil {
			return err
		}

		found = found || f
	}

	if first && accepted && !found {
		m.precondition = false
	}

	return nil
}
