package pcscf

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/sip/sdp"
)

var errMediaMismatch = errors.New("the SDP answer and offer have different m-lines")

type flowNumbers struct {
	rtp, rtcp uint32
	sendrecv  bool
}

type exchange struct {
	offer, answer *sdp.Session
	offerFromUE   bool
}

func (x exchange) uplink() *sdp.Session {
	if x.offerFromUE {
		return x.offer
	}

	return x.answer
}

func (x exchange) downlink() *sdp.Session {
	if x.offerFromUE {
		return x.answer
	}

	return x.offer
}

// TS 29.213 §6.2, TS 29.214 §5.3.7, Annex A.1
func mediaComponents(x exchange, flows map[int]flowNumbers) ([]rx.MediaComponent, error) {
	if len(x.offer.Media) != len(x.answer.Media) {
		return nil, errMediaMismatch
	}

	out := make([]rx.MediaComponent, 0, len(x.answer.Media))

	for i := range x.answer.Media {
		c, err := mediaComponent(x, i, flows)
		if err != nil {
			return nil, fmt.Errorf("media %d: %w", i+1, err)
		}

		out = append(out, c)
	}

	return out, nil
}

func mediaComponent(x exchange, i int, flows map[int]flowNumbers) (rx.MediaComponent, error) {
	offer, answer := x.offer.Media[i], x.answer.Media[i]

	c := rx.MediaComponent{Number: uint32(i + 1), Type: new(rxMediaType(offer.Type()))}

	if offer.Port() == 0 || answer.Port() == 0 {
		c.FlowStatus = new(rx.FlowStatusRemoved)
		return c, nil
	}

	desc, err := answer.Desc()
	if err != nil {
		return rx.MediaComponent{}, err
	}

	muxed := sdp.RTCPMuxed(x.offer, x.answer, i)
	dir := negotiatedDirection(x, i)
	tcp := tcpTransport(desc.Proto)

	c.FlowStatus = new(flowStatus(dir, !x.offerFromUE, muxed || tcp))

	rr, haveRR := bandwidth(x.answer, i, sdp.BandwidthRR)
	rs, haveRS := bandwidth(x.answer, i, sdp.BandwidthRS)

	if haveRR {
		c.RRBandwidth = new(uint32(rr))
	}

	if haveRS {
		c.RSBandwidth = new(uint32(rs))
	}

	requested := func(s *sdp.Session) *rx.Bandwidth {
		as, ok := bandwidth(s, i, sdp.BandwidthAS)
		if !ok {
			return nil
		}

		switch {
		case !muxed || tcp:
			return new(rx.Bandwidth(as * 1000))
		case haveRR || haveRS:
			return new(rx.Bandwidth(as*1000 + rr + rs))
		default:
			return new(rx.Bandwidth(as * 1050))
		}
	}

	c.MaxRequestedBandwidthUL = requested(x.downlink())
	c.MaxRequestedBandwidthDL = requested(x.uplink())

	offerDir, answerDir := rx.CodecDownlink, rx.CodecUplink
	if x.offerFromUE {
		offerDir, answerDir = rx.CodecUplink, rx.CodecDownlink
	}

	c.CodecData = []rx.CodecData{
		{Direction: offerDir, Kind: rx.CodecOffer, SDP: offer.CodecLines()},
		{Direction: answerDir, Kind: rx.CodecAnswer, SDP: answer.CodecLines()},
	}

	subs, err := subComponents(x, i, dir, muxed, tcp, flows)
	if err != nil {
		return rx.MediaComponent{}, err
	}

	c.SubComponents = subs

	return c, nil
}

// TS 29.213 Table 6.2.1 NOTE 5
func negotiatedDirection(x exchange, i int) sdp.Direction {
	if x.offer.MediaDirection(i) == sdp.Inactive {
		return sdp.Inactive
	}

	return x.answer.MediaDirection(i)
}

// TS 29.213 Table 6.2.1
func flowStatus(dir sdp.Direction, fromUE, enabled bool) rx.FlowStatus {
	switch {
	case enabled:
		return rx.FlowStatusEnabled
	case dir == sdp.RecvOnly && fromUE, dir == sdp.SendOnly && !fromUE:
		return rx.FlowStatusEnabledDownlink
	case dir == sdp.RecvOnly, dir == sdp.SendOnly:
		return rx.FlowStatusEnabledUplink
	case dir == sdp.Inactive:
		return rx.FlowStatusDisabled
	}

	return rx.FlowStatusEnabled
}

func rxMediaType(t string) rx.MediaType {
	switch strings.ToLower(t) {
	case sdp.Audio:
		return rx.MediaAudio
	case sdp.Video:
		return rx.MediaVideo
	case sdp.Text:
		return rx.MediaText
	case "application":
		return rx.MediaApplication
	case "message":
		return rx.MediaMessage
	case "data":
		return rx.MediaData
	case "control":
		return rx.MediaControl
	}

	return rx.MediaOther
}

func tcpTransport(proto string) bool {
	return strings.EqualFold(proto, "TCP") || strings.EqualFold(proto, "TCP/MSRP")
}

func bandwidth(s *sdp.Session, i int, typ string) (uint64, bool) {
	return s.Media[i].Bandwidth(typ)
}

// TS 29.213 Table 6.2.2, TS 29.214 Annex A.1
func subComponents(x exchange, i int, dir sdp.Direction, muxed, tcp bool, flows map[int]flowNumbers) ([]rx.MediaSubComponent, error) {
	up, down := x.uplink(), x.downlink()

	ueRTP, err := up.RTPEndpoint(i)
	if err != nil {
		return nil, err
	}

	remoteRTP, err := down.RTPEndpoint(i)
	if err != nil {
		return nil, err
	}

	if ueRTP.Addr.Is4() != remoteRTP.Addr.Is4() {
		return nil, fmt.Errorf("UE address %s and remote address %s of different families", ueRTP.Addr, remoteRTP.Addr)
	}

	proto := rx.ProtocolUDP
	if tcp {
		proto = rx.ProtocolTCP
	}

	prev, seen := flows[i]

	uplink, downlink := true, true

	if !tcp && !muxed && (!seen || !prev.sendrecv) {
		fromUE := !x.offerFromUE

		switch dir {
		case sdp.RecvOnly:
			uplink, downlink = !fromUE, fromUE
		case sdp.SendOnly:
			uplink, downlink = fromUE, !fromUE
		}
	}

	rtp := rx.MediaSubComponent{FlowDescriptions: flowDescriptions(proto, ueRTP, remoteRTP, uplink, downlink)}

	numbers := flowNumbers{sendrecv: dir == sdp.SendRecv}

	if muxed || tcp {
		numbers.rtp = 1
		if seen {
			numbers.rtp = prev.rtp
		}

		rtp.FlowNumber = numbers.rtp
		flows[i] = numbers

		return []rx.MediaSubComponent{rtp}, nil
	}

	ueRTCP, err := up.RTCPEndpoint(i)
	if err != nil {
		return nil, err
	}

	remoteRTCP, err := down.RTCPEndpoint(i)
	if err != nil {
		return nil, err
	}

	numbers.rtp, numbers.rtcp = 1, 2
	if ueRTCP.Port < ueRTP.Port {
		numbers.rtp, numbers.rtcp = 2, 1
	}

	switch {
	case seen && prev.rtcp != 0:
		numbers.rtp, numbers.rtcp = prev.rtp, prev.rtcp
	case seen:
		numbers.rtp, numbers.rtcp = prev.rtp, prev.rtp+1
	}

	flows[i] = numbers
	rtp.FlowNumber = numbers.rtp

	rtcp := rx.MediaSubComponent{
		FlowNumber:       numbers.rtcp,
		FlowDescriptions: flowDescriptions(rx.ProtocolUDP, ueRTCP, remoteRTCP, true, true),
		FlowUsage:        new(rx.FlowUsageRTCP),
	}

	if rtp.FlowNumber < rtcp.FlowNumber {
		return []rx.MediaSubComponent{rtp, rtcp}, nil
	}

	return []rx.MediaSubComponent{rtcp, rtp}, nil
}

func flowDescriptions(proto rx.Protocol, ue, remote sdp.Endpoint, uplink, downlink bool) []string {
	var out []string

	if uplink {
		out = append(out, rx.FlowDescription{
			Direction: rx.FlowDirectionIn, Protocol: proto,
			Source: sourcePrefix(ue.Addr), Destination: netip.PrefixFrom(remote.Addr, remote.Addr.BitLen()), DestinationPort: remote.Port,
		}.String())
	}

	if downlink {
		out = append(out, rx.FlowDescription{
			Direction: rx.FlowDirectionOut, Protocol: proto,
			Source: sourcePrefix(remote.Addr), Destination: netip.PrefixFrom(ue.Addr, ue.Addr.BitLen()), DestinationPort: ue.Port,
		}.String())
	}

	return out
}

// TS 29.214 Annex A.1
func sourcePrefix(a netip.Addr) netip.Prefix {
	if a.Is4() {
		return netip.PrefixFrom(a, 32)
	}

	p, _ := a.Prefix(64)

	return p
}
