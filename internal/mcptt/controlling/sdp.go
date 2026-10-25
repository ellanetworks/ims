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

// errMedia is the error of an SDP without the media of a call: its speech in the call's codec, its control channel.
var errMedia = errors.New("controlling: SDP without the speech of the call or a control channel")

// avp is the RTP profile of the server's offers, which every MCPTT client supports (TS 26.179 §4.1.2).
const avp = "RTP/AVP"

// speech is the speech of a call (TS 24.379 §6.3.3.1.1 step 1a): one codec, as the server relays without
// transcoding, of the originator's offer: its a=rtpmap, in the originator's payload type, and fmtp parameters, its
// media-level section for ptime and maxptime, its b=AS.
type speech struct {
	codec sdp.RTPMap
	fmtp  sdp.FormatParams
	media *sdp.Media
	as    uint64
	// origin is the stream of the originator.
	origin stream
}

// stream is the speech in the SDP of one participant: the call's codec in the payload type and the RTP profile the
// participant negotiated, of the fmtp parameters of the server's SDP to it.
type stream struct {
	pt    uint8
	proto string
	fmtp  sdp.FormatParams
}

// rtpAudio is the description of m if an RTP audio stream the server can take: RTP/AVP or RTP/AVPF, not disabled.
// RTP/SAVP is refused while security is out of scope [PM 9].
func rtpAudio(m *sdp.Media) (sdp.MediaDesc, bool) {
	d, err := m.Desc()

	return d, err == nil && d.Type == sdp.Audio && m.Port() != 0 && (d.Proto == avp || d.Proto == "RTP/AVPF")
}

// chooseSpeech is the speech of an offer (TS 24.379 §10.1.1.4.2 step 2): its first RTP audio stream with a codec of
// the group's preferred-voice-encodings, the first of them it offers; and the index of its control channel.
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

// joining is the stream of the offer of a member joining the call, which the answer takes its payload type, profile
// and direction from (RFC 3264 §6.1): its first RTP audio stream with a payload type of the call's codec that the
// relay forwards without repacking or transcoding; and the index of its control channel. errMedia without, a 488.
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

// compatible is the answer's fmtp parameters to a payload type of codec c and parameters fp if the call's media can
// be relayed to it and from it as is: the call's codec, clock rate and channels; of AMR or AMR-WB, the same payload
// format configuration, which no other is compatible with (RFC 4867 §8.3.1), and a mode-set the call's or none
// (§8.3.1: "the answerer SHALL return the mode-set unmodified or reject the payload type": the joiner receives
// every talker's modes, and its own go to every receiver); of another codec, the call's parameters, which the relay
// cannot tell compatible otherwise.
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

// channels is the number of channels of an audio a=rtpmap, 1 without (RFC 8866 §6.6).
func channels(r sdp.RTPMap) string {
	if r.Params == "" {
		return "1"
	}

	return r.Params
}

// sameParams reports whether a and b have the same parameters, names compared case-insensitively (RFC 4855 §3).
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

// sameSet reports whether a and b have the same elements.
func sameSet(a, b []int) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)

	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

// bandwidthAS is the b=AS of the call's speech (TS 24.229 §6.1.1), whatever the originator's offer had [PM 15]: of
// AMR and AMR-WB as TS 26.114 Annex K computes it for ptime 20 (TS 26.179 §4.1.3), of the highest mode of the
// call's mode-set, its packing and the address family of the server's media; of another codec, the originator's,
// 0 without one.
func (sp speech) bandwidthAS(ipv6 bool) uint64 {
	c, ok := amr.Of(sp.codec)
	if !ok {
		return sp.as
	}

	return c.BandwidthAS(c.MaxMode(sp.fmtp), amr.OctetAligned(sp.fmtp), ipv6)
}

// lines are the media-level lines of the speech in the server's SDP to a participant, at the leg's audio (TS 24.379
// §6.3.3.1.1, §6.3.3.2.1): i=speech, the b=AS of the call, the call's codec in the participant's payload type and
// profile, with st's fmtp parameters, the ptime and maxptime of the originator, RTCP multiplexing (RFC 5761) when
// mux, the direction when not sendrecv.
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

// header is the session-level part of an SDP of the server, of the address of its media.
func header(addr netip.Addr, id uint64) (*sdp.Session, error) {
	ip := sdp.IP4
	if addr.Is6() {
		ip = sdp.IP6
	}

	return sdp.Parse([]byte("v=0\r\no=- " + strconv.FormatUint(id, 10) + " 1 IN " + ip + " " + addr.String() +
		"\r\ns=-\r\nc=IN " + ip + " " + addr.String() + "\r\nt=0 0\r\n"))
}

// answer is the SDP answer to the offer of a participant (TS 24.379 §6.3.3.2.1, TS 24.380 §14.3): its speech st of
// index ai, in the direction that reverses the offer's, of its media or session level (RFC 3264 §6.1), and the
// control channel of index ci, at the leg's ports; the others refused (RFC 3264 §6).
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

// offer is the SDP offer to an invited member (TS 24.379 §6.3.3.1.1): the speech of the call, in its payload type,
// RTP/AVP, and sendrecv, the server's media plane's own whatever the originator's (RFC 3264 §5.1); the control
// channel; at the leg's ports.
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

// remoteOf is the media of the peer of s, its offer or its answer: the addresses of the stream of index ai and the
// control channel of index ci, and the direction of the stream, of the relay's side the reverse of the peer's, at
// its media or session level (RFC 3264 §6.1: the answer's to an offer; §5.1: of the answer, the server's offer
// being sendrecv).
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

// takeAnswer is the answer of an invited member to the offer of the server (TS 24.380 §14.4): its speech accepted
// in the payload type of the call, its control channel, and the fmtp:MCPTT parameters as the server takes them.
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

	// A malformed parameter is left out (§12.1.2), and an mc_priority above the one offered discarded.
	p, _ := controlchannel.MediaParams(s.Media[1])
	accepted, _ := offered.Accept(p)

	return rem, accepted, nil
}
