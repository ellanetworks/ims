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

// TS 26.179 clause 4.1.2
// TS 26.179 clause 4.1.3
const (
	offeredPT   = 96
	offeredFmtp = "mode-change-capability=2;max-red=0"
	avp         = "RTP/AVP"
)

// RFC 4867 section 8.1
type audioFormat struct {
	pt           uint8
	octetAligned bool
	mode         int
	modeSet      []int
	fmtp         string
	maxMode      int
}

// RFC 4867 section 8.2
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

type remoteMedia struct {
	audio, rtcp, control netip.AddrPort
}

var errMedia = errors.New("client: SDP without MCPTT speech or control channel")

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

// TS 24.379 clause 6.2.1
// TS 24.379 clause 6.2.2
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

// TS 24.379 clause 6.2.1
// TS 24.380 clause 14.2
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

type negotiated struct {
	format           audioFormat
	remote           remoteMedia
	control, offered controlchannel.Params
}

// RFC 3264 section 6
// TS 24.380 clause 14.4
// RFC 4867 section 8.3.1
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

	params, _ := controlchannel.MediaParams(s.Media[1])
	n.control, _ = local.Accept(params)

	return n, nil
}

// TS 24.379 clause 6.2.2
// TS 24.380 clause 14.3
// RFC 3264 section 6
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

// RFC 4867 section 8.2
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

// RFC 4867 section 8.3.1
// TS 26.179 clause 4.1.3
func answerFmtp(fp sdp.FormatParams) string {
	return append(amr.Configuration(fp), sdp.ParseFormatParams(offeredFmtp)...).String()
}

// RFC 4867 section 8.2
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

func param(fp sdp.FormatParams, name string) string {
	v, _ := fp.Get(name)
	return v
}
