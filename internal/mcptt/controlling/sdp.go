package controlling

import (
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/mcptt/amr"
	"github.com/ellanetworks/ims/mcptt/controlchannel"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip/sdp"
)

var errMedia = errors.New("controlling: SDP without the speech of the call or a control channel")

// TS 26.179 clause 4.1.2
const avp = "RTP/AVP"

// TS 24.379 clause 6.3.3.1.1
type speech struct {
	codec  sdp.RTPMap
	fmtp   sdp.FormatParams
	media  *sdp.Media
	as     uint64
	origin stream
}

type stream struct {
	pt    uint8
	proto string
	fmtp  sdp.FormatParams
}

func rtpAudio(m *sdp.Media) (sdp.MediaDesc, bool) {
	d, err := m.Desc()

	return d, err == nil && d.Type == sdp.Audio && m.Port() != 0 && (d.Proto == avp || d.Proto == "RTP/AVPF")
}

// TS 24.379 clause 10.1.1.4.2
func chooseSpeech(s *sdp.Session, encodings []string) (sp speech, ai, ci int, err error) {
	ci = controlchannel.Channel(s)
	if ci < 0 || s.Media[ci].Port() == 0 {
		return speech{}, 0, 0, errMedia
	}

	for i, m := range s.Media {
		d, ok := rtpAudio(m)
		if !ok {
			continue
		}

		codecs, _ := m.Codecs()

		for _, e := range encodings {
			for _, c := range codecs {
				if !strings.EqualFold(c.Encoding, e) {
					continue
				}

				fp, _ := m.FormatParams(strconv.Itoa(int(c.Payload)))
				as, _ := m.Bandwidth(sdp.BandwidthAS)

				return speech{codec: c, fmtp: fp, media: m, as: as, origin: stream{pt: c.Payload, proto: d.Proto, fmtp: fp}}, i, ci, nil
			}
		}
	}

	return speech{}, 0, 0, errMedia
}

// RFC 3264 section 6.1
func (sp speech) joining(s *sdp.Session) (st stream, ai, ci int, err error) {
	ci = controlchannel.Channel(s)
	if ci < 0 || s.Media[ci].Port() == 0 {
		return stream{}, 0, 0, errMedia
	}

	for i, m := range s.Media {
		d, ok := rtpAudio(m)
		if !ok {
			continue
		}

		codecs, _ := m.Codecs()

		for _, c := range codecs {
			fp, _ := m.FormatParams(strconv.Itoa(int(c.Payload)))

			if answer, ok := sp.compatible(c, fp); ok {
				return stream{pt: c.Payload, proto: d.Proto, fmtp: answer}, i, ci, nil
			}
		}
	}

	return stream{}, 0, 0, errMedia
}

// RFC 4867 section 8.3.1
func (sp speech) compatible(c sdp.RTPMap, fp sdp.FormatParams) (sdp.FormatParams, bool) {
	if !strings.EqualFold(c.Encoding, sp.codec.Encoding) || c.ClockRate != sp.codec.ClockRate || channels(c) != channels(sp.codec) {
		return nil, false
	}

	codec, ok := amr.Of(c)
	if !ok {
		return sp.fmtp, sameParams(fp, sp.fmtp)
	}

	if !amr.SameConfiguration(fp, sp.fmtp) {
		return nil, false
	}

	set, ok := fp.Get("mode-set")
	if !ok {
		return sp.fmtp, true
	}

	if !sp.fmtp.Has("mode-set") || !sameSet(codec.ModeSet(fp), codec.ModeSet(sp.fmtp)) {
		return nil, false
	}

	out := slices.Clone(sp.fmtp)
	for i, p := range out {
		if strings.EqualFold(p.Name, "mode-set") {
			out[i].Value = set
		}
	}

	return out, true
}

// RFC 8866 section 6.6
func channels(r sdp.RTPMap) string {
	if r.Params == "" {
		return "1"
	}

	return r.Params
}

// RFC 4855 section 3
func sameParams(a, b sdp.FormatParams) bool {
	if len(a) != len(b) {
		return false
	}

	for _, p := range a {
		if v, ok := b.Get(p.Name); !ok || v != p.Value {
			return false
		}
	}

	return true
}

func sameSet(a, b []int) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)

	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

// TS 26.114 annex K
// TS 26.179 clause 4.1.3
func (sp speech) bandwidthAS(ipv6 bool) uint64 {
	c, ok := amr.Of(sp.codec)
	if !ok {
		return sp.as
	}

	return c.BandwidthAS(c.MaxMode(sp.fmtp), amr.OctetAligned(sp.fmtp), ipv6)
}

// TS 24.379 clause 6.3.3.1.1
// TS 24.379 clause 6.3.3.2.1
func (sp speech) lines(l *media.Leg, st stream, dir sdp.Direction, mux bool) *sdp.Media {
	pt := strconv.Itoa(int(st.pt))

	m := &sdp.Media{}
	m.SetDesc(sdp.MediaDesc{Type: sdp.Audio, Port: l.Audio().Port(), Proto: st.proto, Formats: []string{pt}})
	m.Add('i', "speech")

	if as := sp.bandwidthAS(l.Audio().Addr().Is6()); as > 0 {
		m.SetBandwidth(sdp.BandwidthAS, as)
	}

	r := sp.codec
	r.Payload = st.pt
	m.AddAttr("rtpmap", r.String())

	if len(st.fmtp) > 0 {
		m.AddAttr("fmtp", pt+" "+st.fmtp.String())
	}

	for _, name := range []string{"ptime", "maxptime"} {
		if v, ok := sp.media.Attr(name); ok {
			m.AddAttr(name, v)
		}
	}

	if mux {
		m.AddAttr("rtcp-mux", "")
	}

	if dir != sdp.SendRecv {
		m.SetDirection(dir)
	}

	return m
}

func header(addr netip.Addr, id uint64) (*sdp.Session, error) {
	ip := sdp.IP4
	if addr.Is6() {
		ip = sdp.IP6
	}

	return sdp.Parse([]byte("v=0\r\no=- " + strconv.FormatUint(id, 10) + " 1 IN " + ip + " " + addr.String() +
		"\r\ns=-\r\nc=IN " + ip + " " + addr.String() + "\r\nt=0 0\r\n"))
}

// TS 24.379 clause 6.3.3.2.1
// TS 24.380 clause 14.3
// RFC 3264 section 6.1
// RFC 3264 section 6
func (sp speech) answer(offer *sdp.Session, st stream, ai, ci int, l *media.Leg, id uint64, mux bool, control controlchannel.Params) ([]byte, error) {
	s, err := header(l.Audio().Addr(), id)
	if err != nil {
		return nil, err
	}

	for i, m := range offer.Media {
		switch i {
		case ai:
			s.Media = append(s.Media, sp.lines(l, st, offer.MediaDirection(ai).Reverse(), mux))
		case ci:
			s.Media = append(s.Media, controlchannel.NewMedia(l.Control().Port(), control))
		default:
			r := m.Clone()
			r.Disable()
			s.Media = append(s.Media, r)
		}
	}

	return s.Bytes(), nil
}

// TS 24.379 clause 6.3.3.1.1
// RFC 3264 section 5.1
func (sp speech) offer(l *media.Leg, id uint64, control controlchannel.Params) ([]byte, error) {
	s, err := header(l.Audio().Addr(), id)
	if err != nil {
		return nil, err
	}

	st := stream{pt: sp.codec.Payload, proto: avp, fmtp: sp.fmtp}
	s.Media = append(s.Media, sp.lines(l, st, sdp.SendRecv, false),
		controlchannel.NewMedia(l.Control().Port(), controlchannel.Offer(control)))

	return s.Bytes(), nil
}

// RFC 3264 section 6.1
// RFC 3264 section 5.1
func remoteOf(s *sdp.Session, ai, ci int) (media.Remote, error) {
	audio, err1 := s.RTPEndpoint(ai)
	rtcp, err2 := s.RTCPEndpoint(ai)
	control, err3 := s.RTPEndpoint(ci)

	if err := errors.Join(err1, err2, err3); err != nil {
		return media.Remote{}, err
	}

	ap := func(e sdp.Endpoint) netip.AddrPort { return netip.AddrPortFrom(e.Addr, e.Port) }

	dirs := map[sdp.Direction]media.Direction{
		sdp.SendRecv: media.SendRecv, sdp.SendOnly: media.SendOnly, sdp.RecvOnly: media.RecvOnly, sdp.Inactive: media.Inactive,
	}

	return media.Remote{Audio: ap(audio), RTCP: ap(rtcp), Control: ap(control), Direction: dirs[s.MediaDirection(ai).Reverse()]}, nil
}

// TS 24.380 clause 14.4
func (sp speech) takeAnswer(b []byte, offered controlchannel.Params) (media.Remote, controlchannel.Params, error) {
	s, err := sdp.Parse(b)
	if err != nil {
		return media.Remote{}, controlchannel.Params{}, err
	}

	if len(s.Media) != 2 || s.Media[0].Port() == 0 || s.Media[1].Port() == 0 || !controlchannel.IsChannel(s.Media[1]) {
		return media.Remote{}, controlchannel.Params{}, errMedia
	}

	d, err := s.Media[0].Desc()
	if err != nil || len(d.Formats) == 0 || d.Formats[0] != strconv.Itoa(int(sp.codec.Payload)) {
		return media.Remote{}, controlchannel.Params{}, errMedia
	}

	rem, err := remoteOf(s, 0, 1)
	if err != nil {
		return media.Remote{}, controlchannel.Params{}, err
	}

	p, _ := controlchannel.MediaParams(s.Media[1])
	accepted, _ := offered.Accept(p)

	return rem, accepted, nil
}
