package client

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/mcptt/amr"
	"github.com/ellanetworks/ims/mcptt/controlchannel"
	"github.com/ellanetworks/ims/sip/sdp"
)

// The audio of the client's offers (TS 26.179 §4.1.2, §4.1.3): one AMR-WB payload type, bandwidth-efficient, all
// modes, mode-change-capability 2 and max-red 0, one channel, ptime 20 and maxptime 240.
const (
	offeredPT   = 96
	offeredFmtp = "mode-change-capability=2;max-red=0"
	avp         = "RTP/AVP"
)

// audioFormat is the AMR-WB payload type of a call, and how the client sends it: the mode of its frames, within the
// mode-set, nil for all modes (RFC 4867 §8.1).
type audioFormat struct {
	pt           uint8
	octetAligned bool
	mode         int
	modeSet      []int
	// fmtp are its parameters in the client's SDP; maxMode the highest mode of its mode-set.
	fmtp    string
	maxMode int
}

// modes are the modes the client may send in (RFC 4867 §8.2).
func (f audioFormat) modes() []int {
	if f.modeSet != nil {
		return f.modeSet
	}

	all := make([]int, len(amr.WB.FrameBits))
	for i := range all {
		all[i] = i
	}

	return all
}

// remoteMedia are the media addresses of the server, of its SDP.
type remoteMedia struct {
	audio, rtcp, control netip.AddrPort
}

// errMedia is the error of an SDP without the media of a call.
var errMedia = errors.New("client: SDP without MCPTT speech or control channel")

// header is the session-level part of an SDP of the client.
func header(addr netip.Addr, id uint64, version int) (*sdp.Session, error) {
	ip := sdp.IP4
	if addr.Is6() {
		ip = sdp.IP6
	}

	return sdp.Parse([]byte(strings.Join([]string{
		"v=0",
		"o=- " + strconv.FormatUint(id, 10) + " " + strconv.Itoa(version) + " IN " + ip + " " + addr.String(),
		"s=-",
		"c=IN " + ip + " " + addr.String(),
		"t=0 0",
	}, "\r\n") + "\r\n"))
}

// audioMedia is the m=audio of the MCPTT speech (TS 24.379 §6.2.1 step 2, §6.2.2 step 3): i=speech, its bandwidth,
// the AMR-WB payload type, its direction when not sendrecv.
func audioMedia(addr netip.Addr, port uint16, f audioFormat, dir sdp.Direction) *sdp.Media {
	pt := strconv.Itoa(int(f.pt))

	m := &sdp.Media{}
	m.SetDesc(sdp.MediaDesc{Type: sdp.Audio, Port: port, Proto: avp, Formats: []string{pt}})
	m.Add('i', "speech")
	m.SetBandwidth(sdp.BandwidthAS, amr.WB.BandwidthAS(f.maxMode, f.octetAligned, addr.Is6()))
	m.AddAttr("rtpmap", sdp.RTPMap{Payload: f.pt, Encoding: "AMR-WB", ClockRate: clockRate, Params: "1"}.String())

	if f.fmtp != "" {
		m.AddAttr("fmtp", pt+" "+f.fmtp)
	}

	m.AddAttr("ptime", "20")
	m.AddAttr("maxptime", "240")

	if dir != "" && dir != sdp.SendRecv {
		m.SetDirection(dir)
	}

	return m
}

// offer is the SDP offer of an initial INVITE (TS 24.379 §6.2.1): the speech, then the control channel with the
// fmtp:MCPTT parameters of local (TS 24.380 §14.2).
func offer(addr netip.Addr, id uint64, audio, control uint16, local controlchannel.Params) ([]byte, error) {
	s, err := header(addr, id, 1)
	if err != nil {
		return nil, err
	}

	s.Media = append(s.Media,
		audioMedia(addr, audio, audioFormat{pt: offeredPT, mode: defaultMode, fmtp: offeredFmtp, maxMode: len(amr.WB.FrameBits) - 1}, sdp.SendRecv),
		controlchannel.NewMedia(control, controlchannel.Offer(local)))

	return s.Bytes(), nil
}

// negotiated is what an offer/answer exchange of a call settled.
type negotiated struct {
	format audioFormat
	remote remoteMedia
	// control are the fmtp:MCPTT parameters as the client takes them: of the answer to its offer (TS 24.380 §14.4),
	// or of its answer to the server's offer, along with the server's offered.
	control, offered controlchannel.Params
}

// takeAnswer is the answer to offer (RFC 3264 §6, TS 24.380 §14.4): the speech accepted in the payload type
// offered, of the same octet-align (RFC 4867 §8.3.1), and the control channel.
func takeAnswer(b []byte, local controlchannel.Params) (negotiated, error) {
	var n negotiated

	s, err := sdp.Parse(b)
	if err != nil {
		return n, err
	}

	if len(s.Media) != 2 || s.Media[0].Port() == 0 || s.Media[1].Port() == 0 || !controlchannel.IsChannel(s.Media[1]) {
		return n, errMedia
	}

	a := s.Media[0]

	desc, err := a.Desc()
	if err != nil || !slices.Contains(desc.Formats, strconv.Itoa(offeredPT)) {
		return n, fmt.Errorf("%w: AMR-WB not accepted", errMedia)
	}

	fp, _ := a.FormatParams(strconv.Itoa(offeredPT))

	mode, ok := chooseMode(fp)
	if !ok || fp.Has("octet-align") && param(fp, "octet-align") != "0" {
		return n, fmt.Errorf("%w: AMR-WB parameters %s", errMedia, fp)
	}

	n.format = audioFormat{pt: offeredPT, mode: mode, modeSet: amr.WB.ModeSet(fp)}

	if n.remote, err = remoteOf(s, 0, 1); err != nil {
		return n, err
	}

	// A malformed parameter is left out (§12.1.2), and an mc_priority above the one offered discarded.
	params, _ := controlchannel.MediaParams(s.Media[1])
	n.control, _ = local.Accept(params)

	return n, nil
}

// answerOffer is the answer to an offer of the server (TS 24.379 §6.2.2, TS 24.380 §14.3): its first speech stream
// of AMR-WB the source can send accepted, in the direction that reverses its own, and its control channel answered
// with local; the other streams refused (RFC 3264 §6).
func answerOffer(b []byte, addr netip.Addr, id uint64, audio, control uint16, local controlchannel.Params) ([]byte, negotiated, error) {
	var n negotiated

	s, err := sdp.Parse(b)
	if err != nil {
		return nil, n, err
	}

	ans, err := header(addr, id, 1)
	if err != nil {
		return nil, n, err
	}

	ai, ci := -1, -1

	for i, m := range s.Media {
		desc, err := m.Desc()

		switch {
		case err != nil || m.Port() == 0:
		// RTP/SAVP refused while security is out of scope [PM 9]: a deviation from TS 26.179 §4.1.2, which has
		// MCPTT clients not reject an offer for its RTP profile.
		case ai < 0 && desc.Type == sdp.Audio && desc.Proto == avp:
			f, ok := amrwb(m, desc)
			if !ok {
				break
			}

			ai, n.format = i, f
			ans.Media = append(ans.Media, audioMedia(addr, audio, f, s.MediaDirection(i).Reverse()))

			continue
		case ci < 0 && controlchannel.IsChannel(m):
			offered, _ := controlchannel.MediaParams(m)

			a, err := controlchannel.Answer(offered, local)
			if err != nil {
				break
			}

			ci, n.control, n.offered = i, a, offered
			ans.Media = append(ans.Media, controlchannel.NewMedia(control, a))

			continue
		}

		r := m.Clone()
		r.Disable()
		ans.Media = append(ans.Media, r)
	}

	if ai < 0 || ci < 0 {
		return nil, n, errMedia
	}

	if n.remote, err = remoteOf(s, ai, ci); err != nil {
		return nil, n, err
	}

	return ans.Bytes(), n, nil
}

// remoteOf are the addresses of the speech of index ai and the control channel of index ci of s.
func remoteOf(s *sdp.Session, ai, ci int) (remoteMedia, error) {
	audio, err1 := s.RTPEndpoint(ai)
	rtcp, err2 := s.RTCPEndpoint(ai)
	control, err3 := s.RTPEndpoint(ci)

	if err := errors.Join(err1, err2, err3); err != nil {
		return remoteMedia{}, err
	}

	ap := func(e sdp.Endpoint) netip.AddrPort { return netip.AddrPortFrom(e.Addr, e.Port) }

	return remoteMedia{audio: ap(audio), rtcp: ap(rtcp), control: ap(control)}, nil
}

// amrwb is the first payload type of m of AMR-WB the source can send (RFC 4867 §8.2): one channel, a mode it may
// use, without frame CRCs, robust sorting or interleaving, which it does not implement.
func amrwb(m *sdp.Media, desc sdp.MediaDesc) (audioFormat, bool) {
	maps, _ := m.RTPMaps()

	for _, f := range desc.Formats {
		i := slices.IndexFunc(maps, func(r sdp.RTPMap) bool { return strconv.Itoa(int(r.Payload)) == f })
		if i < 0 {
			continue
		}

		r := maps[i]
		if !strings.EqualFold(r.Encoding, "AMR-WB") || r.ClockRate != clockRate || r.Params != "" && r.Params != "1" {
			continue
		}

		fp, _ := m.FormatParams(f)
		if param(fp, "crc") == "1" || param(fp, "robust-sorting") == "1" || fp.Has("interleaving") {
			continue
		}

		mode, ok := chooseMode(fp)
		if !ok {
			continue
		}

		return audioFormat{
			pt: r.Payload, octetAligned: amr.OctetAligned(fp), mode: mode, modeSet: amr.WB.ModeSet(fp), fmtp: answerFmtp(fp),
			maxMode: amr.WB.MaxMode(fp),
		}, true
	}

	return audioFormat{}, false
}

// answerFmtp are the AMR-WB parameters of the answer to the offered fp (RFC 4867 §8.3.1): the payload format
// configuration and the mode-set as offered, the client's mode-change-capability and max-red (TS 26.179 §4.1.3);
// the parameters it does not know removed, and those declaring the server's own (mode-change-period,
// mode-change-neighbor) left out.
func answerFmtp(fp sdp.FormatParams) string {
	return append(amr.Configuration(fp), sdp.ParseFormatParams(offeredFmtp)...).String()
}

// chooseMode is the mode of the frames within the mode-set (RFC 4867 §8.2): the default one, or else the first.
func chooseMode(fp sdp.FormatParams) (int, bool) {
	if !fp.Has("mode-set") {
		return defaultMode, true
	}

	modes := amr.WB.ModeSet(fp)

	switch {
	case slices.Contains(modes, defaultMode):
		return defaultMode, true
	case len(modes) > 0:
		return modes[0], true
	}

	return 0, false
}

// param is the value of the parameter name of fp, "" without it.
func param(fp sdp.FormatParams, name string) string {
	v, _ := fp.Get(name)
	return v
}
