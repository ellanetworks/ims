package testue

import (
	"cmp"
	"encoding/hex"
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

// GSMA NG.114 section 3.3.2.1
// GSMA IR.94 section 3.3.1
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

	// GSMA IR.94 section 2.4.2
	// TS 26.114 clause 6.2.3
	cvoURN = "urn:3gpp:video-orientation"
	cvoID  = "7"
)

// RFC 6184 section 8.2.2
// RFC 7798 section 7.2.2
var mirroredVideo = map[string][]string{
	h264: {"profile-level-id", "packetization-mode"},
	h265: {"profile-space", "tier-flag", "profile-id", "level-id", "profile-compatibility-indicator", "interop-constraints"},
}

// TS 26.114 clause 6.2.3.2
// TS 26.114 clause 7.3.3
// TS 26.114 clause A.4
// RFC 4585 section 4.2
var offeredFeedback = []string{"trr-int 5000", "nack", "nack pli", "ccm fir", "ccm tmmbr"}

var mirroredAMR = []string{"octet-align", "mode-set", "crc", "robust-sorting", "interleaving", "channels"}

var ErrNoCodec = errors.New("testue: no acceptable audio codec in the offer")

var ErrNoStream = errors.New("testue: no such stream")

var sessionIDs atomic.Uint64

func init() {
	sessionIDs.Store(uint64(time.Now().Unix()) << 16)
}

// RFC 3264 section 5
type stream struct {
	kind     string
	proto    string
	feedback []string
	cvo      string
	disabled string

	formats   []format
	direction sdp.Direction

	sent      int
	reported  string
	remoteQoS string
}

func newStream(kind string, formats []format) *stream {
	s := &stream{kind: kind, proto: avp, formats: formats, direction: sdp.SendRecv, remoteQoS: sdp.QoSNone}

	if kind == sdp.Video {
		s.proto, s.cvo, s.feedback = avpf, cvoID, offeredFeedback
	}

	return s
}

func (s *stream) active() bool { return s.disabled == "" }

// RFC 3264 section 8.2
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
	video        bool
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

func (m *media) find(kind string) int {
	return slices.IndexFunc(m.streams, func(s *stream) bool { return s.active() && s.kind == kind })
}

// RFC 3264 section 8.1
// RFC 3264 section 8.3
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

// RFC 3264 section 8.2
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
		c.feedback = slices.Clone(s.feedback)
		out[i] = &c
	}

	return out
}

func (m *media) fork() *media {
	f := *m
	f.streams = cloneStreams(m.streams)

	return &f
}

type mediaState struct {
	streams       []*stream
	local, remote *sdp.Session
	precondition  bool
}

func (m *media) save() mediaState {
	return mediaState{streams: cloneStreams(m.streams), local: m.local, remote: m.remote, precondition: m.precondition}
}

func (m *media) restore(st mediaState) {
	m.streams, m.local, m.remote, m.precondition = st.streams, st.local, st.remote, st.precondition
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

// RFC 3312 section 5
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

func (m *media) remoteMet() bool {
	for _, s := range m.active() {
		if s.remoteQoS != sdp.QoSSendRecv {
			return false
		}
	}

	return true
}

// GSMA IR.94 section 2.3.2
func (m *media) setDirection(d sdp.Direction) {
	for _, s := range m.active() {
		s.direction = d
	}
}

// RFC 3264 section 8.4
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

// RFC 3312 section 8
func (m *media) failure(last *sdp.Session) (*sdp.Session, error) {
	if !m.precondition || last == nil {
		return nil, nil
	}

	lines := m.header()

	for _, lm := range last.Media {
		desc, err := lm.Desc()
		if err != nil {
			return nil, fmt.Errorf("testue: failure: %w", err)
		}

		desc.Port = 0
		lines = append(lines, "m="+desc.String())

		pres, _ := lm.Preconditions()

		remote := sdp.QoSNone

		for _, p := range pres {
			if p.Type == sdp.QoS && p.Kind == sdp.Current && p.Status == sdp.StatusLocal {
				remote = p.Direction
			}
		}

		if len(pres) > 0 {
			lines = append(lines,
				"a=curr:qos local none",
				"a=curr:qos remote "+remote,
				"a=des:qos failure local sendrecv",
				"a=des:qos mandatory remote sendrecv",
				"a=inactive")
		}
	}

	s, err := sdp.Parse([]byte(strings.Join(lines, "\r\n") + "\r\n"))
	if err != nil {
		return nil, fmt.Errorf("testue: failure: %w", err)
	}

	return s, nil
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

// RFC 3550 section 11
func (m *media) portFor(kind string) uint16 {
	if kind == sdp.Video {
		return m.port + 2
	}

	return m.port
}

func (m *media) mline(s *stream, direction sdp.Direction, preconditions, extra []string) []string {
	desc := s.desc()
	desc.Port = m.portFor(s.kind)

	out := []string{"m=" + desc.String()}

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

	if s.proto == avpf {
		for _, fb := range s.feedback {
			out = append(out, "a=rtcp-fb:* "+fb)
		}
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

// RFC 3264
// GSMA IR.92
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
	neg := negotiate(offer, func(om *sdp.Media) []uint8 { return payloadTypes(m.formats(om)) })
	offer = neg.offer

	lines := append(m.header(), neg.session...)
	streams := make([]*stream, 0, len(offer.Media))
	accepted := map[string]bool{}

	for i, om := range offer.Media {
		desc, err := om.Desc()
		if err != nil {
			return nil, fmt.Errorf("testue: offer: %w", err)
		}

		var formats []format
		if !accepted[desc.Type] {
			formats = m.formats(om)
		}

		if formats == nil {
			s := &stream{kind: desc.Type}
			s.disable(desc)
			lines = append(lines, s.disabled)
			streams = append(streams, s)

			continue
		}

		accepted[desc.Type] = true

		s := newStream(desc.Type, nil)
		if i < len(m.streams) && m.streams[i].active() && m.streams[i].kind == desc.Type {
			s = cloneStreams(m.streams[i : i+1])[0]
		}

		s.formats, s.proto, s.feedback = formats, desc.Proto, nil

		if strings.EqualFold(s.proto, avpf) {
			s.feedback = answerFeedback(om, formats[0].Payload)
		}

		if s.kind == sdp.Video {
			s.cvo = cvo(om)
		}

		if _, err := m.receive(s, offer, i); err != nil {
			return nil, err
		}

		qos := m.qos(s, sdp.StrengthMandatory, s.remoteQoS != sdp.QoSSendRecv)

		lines = append(lines, m.mline(s, sdp.Effective(offer.MediaDirection(i), s.direction), qos, neg.media[i])...)
		streams = append(streams, s)
	}

	if !accepted[sdp.Audio] {
		return nil, ErrNoCodec
	}

	m.remote, m.streams = offer, streams

	return m.build(lines)
}

func (m *media) formats(om *sdp.Media) []format {
	desc, err := om.Desc()
	if err != nil || om.Port() == 0 || !slices.ContainsFunc(supportedProtos, func(p string) bool { return strings.EqualFold(p, desc.Proto) }) {
		return nil
	}

	switch {
	case desc.Type == sdp.Audio:
		return choose(om)
	case desc.Type == sdp.Video && m.video:
		return chooseVideo(om)
	}

	return nil
}

func payloadTypes(formats []format) []uint8 {
	out := make([]uint8, 0, len(formats))
	for _, f := range formats {
		out = append(out, f.Payload)
	}

	return out
}

// TS 26.114 clause 6.2.2.2
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

	if param(params, "octet-align") == "1" {
		for _, o := range codecs[i+1:] {
			if strings.EqualFold(o.Encoding, c.Encoding) && o.ClockRate == c.ClockRate && param(fmtpParams(offer, o.Payload), "octet-align") != "1" {
				c, params = o, fmtpParams(offer, o.Payload)
				break
			}
		}
	}

	fmtp := amrFmtp

	for _, name := range mirroredAMR {
		if v, ok := params.Get(name); ok {
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

// GSMA IR.94 section 3.3.1
// GSMA NG.114 section 3.3.2.1
// RFC 6184 section 6.3
func chooseVideo(offer *sdp.Media) []format {
	codecs, _ := offer.Codecs()

	for _, c := range codecs {
		params := fmtpParams(offer, c.Payload)

		if c.ClockRate != 90000 || !videoProfile(c.Encoding, params) {
			continue
		}

		var fmtp []string

		for _, name := range mirroredVideo[strings.ToUpper(c.Encoding)] {
			if v, ok := params.Get(name); ok {
				fmtp = append(fmtp, name+"="+v)
			}
		}

		return []format{{RTPMap: c, fmtp: strings.Join(fmtp, ";")}}
	}

	return nil
}

func videoProfile(encoding string, params sdp.FormatParams) bool {
	switch strings.ToUpper(encoding) {
	case h265:
		return cmp.Or(param(params, "profile-space"), "0") == "0" && cmp.Or(param(params, "profile-id"), "1") == "1"
	case h264:
		return param(params, "packetization-mode") == "1" && h264Constrained(param(params, "profile-level-id"))
	}

	return false
}

// RFC 6184 section 8.1
// ITU-T H.264 section A.2.1.1
// ITU-T H.264 section A.2.4.2
func h264Constrained(profileLevelID string) bool {
	b, err := hex.DecodeString(profileLevelID)
	if err != nil || len(b) != 3 {
		return false
	}

	switch b[0] {
	case 0x42:
		return b[1]&0x40 != 0
	case 0x64:
		return b[1]&0x0c == 0x0c
	}

	return false
}

// RFC 4585 section 4.2
// TS 26.114 clause 6.2.3.2
func answerFeedback(offer *sdp.Media, pt uint8) []string {
	var offered []string

	for _, v := range offer.Attrs("rtcp-fb") {
		target, value, ok := strings.Cut(strings.TrimSpace(v), " ")
		if ok && (target == "*" || target == strconv.Itoa(int(pt))) {
			offered = append(offered, strings.Join(strings.Fields(value), " "))
		}
	}

	var out []string

	for _, ours := range offeredFeedback {
		kind, _, _ := strings.Cut(ours, " ")

		for _, o := range offered {
			if strings.EqualFold(o, ours) || kind == "trr-int" && strings.HasPrefix(strings.ToLower(o), "trr-int ") {
				out = append(out, o)
				break
			}
		}
	}

	return out
}

// RFC 8285 section 6
// TS 26.114 clause 6.2.3.3
func cvo(offer *sdp.Media) string {
	for _, v := range offer.Attrs("extmap") {
		fields := strings.Fields(v)
		if len(fields) < 2 || !strings.EqualFold(fields[1], cvoURN) {
			continue
		}

		id, dir, ok := strings.Cut(fields[0], "/")
		if !ok {
			return id
		}

		switch sdp.Direction(dir) {
		case sdp.SendOnly:
			dir = string(sdp.RecvOnly)
		case sdp.RecvOnly:
			dir = string(sdp.SendOnly)
		}

		return id + "/" + dir
	}

	return ""
}

func fmtpParams(m *sdp.Media, pt uint8) sdp.FormatParams {
	ps, _ := m.FormatParams(strconv.Itoa(int(pt)))
	return ps
}

func param(ps sdp.FormatParams, name string) string {
	v, _ := ps.Get(name)
	return v
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

// RFC 3264 section 6
// RFC 3312 section 11
func (m *media) answered(answer *sdp.Session) error {
	if len(answer.Media) != len(m.streams) {
		return fmt.Errorf("testue: answer with %d m-lines to an offer with %d (RFC 3264 §6)", len(answer.Media), len(m.streams))
	}

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
