package pcscf

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/internal/policy"
	"github.com/ellanetworks/ims/sip/sdp"
)

var errMediaMismatch = errors.New("the SDP answer and offer have different m-lines")

type flowNumbers struct {
	rtp, rtcp uint32
	sendrecv  bool
}

type sdpExchange struct {
	offer, answer *sdp.Session
	offerFromUE   bool
}

// TS 29.213 Table 6.2.1: the m-lines sent with Flow-Status REMOVED.
func (x sdpExchange) removed() []string {
	var out []string

	for i, m := range x.answer.Media {
		if i < len(x.offer.Media) && (m.Port() == 0 || x.offer.Media[i].Port() == 0) {
			out = append(out, strconv.Itoa(i+1)+":"+x.offer.Media[i].Type())
		}
	}

	return out
}

func (x sdpExchange) uplink() *sdp.Session {
	if x.offerFromUE {
		return x.offer
	}

	return x.answer
}

func (x sdpExchange) downlink() *sdp.Session {
	if x.offerFromUE {
		return x.answer
	}

	return x.offer
}

// TS 29.213 §6.2, TS 29.214 §5.3.7, Annex A.1, TS 29.513 §7.2.3
func mediaComponents(x sdpExchange, flows map[int]flowNumbers) ([]policy.MediaComponent, error) {
	if len(x.offer.Media) != len(x.answer.Media) {
		return nil, errMediaMismatch
	}

	out := make([]policy.MediaComponent, 0, len(x.answer.Media))

	for i := range x.answer.Media {
		c, err := mediaComponent(x, i, flows)
		if err != nil {
			return nil, fmt.Errorf("media %d: %w", i+1, err)
		}

		out = append(out, c)
	}

	return out, nil
}

func mediaComponent(x sdpExchange, i int, flows map[int]flowNumbers) (policy.MediaComponent, error) {
	offer, answer := x.offer.Media[i], x.answer.Media[i]

	c := policy.MediaComponent{Number: uint32(i + 1), Type: policyMediaType(offer.Type())}

	if offer.Port() == 0 || answer.Port() == 0 {
		c.Status = policy.FlowRemoved
		return c, nil
	}

	desc, err := answer.Desc()
	if err != nil {
		return policy.MediaComponent{}, err
	}

	muxed := sdp.RTCPMuxed(x.offer, x.answer, i)
	dir := negotiatedDirection(x, i)
	tcp := tcpTransport(desc.Proto)

	c.Status = flowStatus(dir, !x.offerFromUE, muxed || tcp)

	rr, haveRR := bandwidth(x.answer, i, sdp.BandwidthRR)
	rs, haveRS := bandwidth(x.answer, i, sdp.BandwidthRS)

	if haveRR {
		c.RR = new(uint32(rr))
	}

	if haveRS {
		c.RS = new(uint32(rs))
	}

	requested := func(s *sdp.Session) *uint64 {
		as, ok := bandwidth(s, i, sdp.BandwidthAS)
		if !ok {
			return nil
		}

		switch {
		case !muxed || tcp:
			return new(as * 1000)
		case haveRR || haveRS:
			return new(as*1000 + rr + rs)
		default:
			return new(as * 1050)
		}
	}

	c.MaxRequestedUL = requested(x.downlink())
	c.MaxRequestedDL = requested(x.uplink())

	c.Codecs = []policy.Codec{
		{Uplink: x.offerFromUE, SDP: offer.CodecLines()},
		{Uplink: !x.offerFromUE, Answer: true, SDP: answer.CodecLines()},
	}

	subs, err := subComponents(x, i, dir, muxed, tcp, flows)
	if err != nil {
		return policy.MediaComponent{}, err
	}

	c.SubComponents = subs

	return c, nil
}

// TS 29.213 Table 6.2.1 NOTE 5
func negotiatedDirection(x sdpExchange, i int) sdp.Direction {
	if x.offer.MediaDirection(i) == sdp.Inactive {
		return sdp.Inactive
	}

	return x.answer.MediaDirection(i)
}

// TS 29.213 Table 6.2.1
func flowStatus(dir sdp.Direction, fromUE, enabled bool) policy.FlowStatus {
	switch {
	case enabled:
		return policy.FlowEnabled
	case dir == sdp.RecvOnly && fromUE, dir == sdp.SendOnly && !fromUE:
		return policy.FlowEnabledDownlink
	case dir == sdp.RecvOnly, dir == sdp.SendOnly:
		return policy.FlowEnabledUplink
	case dir == sdp.Inactive:
		return policy.FlowDisabled
	}

	return policy.FlowEnabled
}

func policyMediaType(t string) policy.MediaType {
	switch strings.ToLower(t) {
	case sdp.Audio:
		return policy.MediaAudio
	case sdp.Video:
		return policy.MediaVideo
	case sdp.Text:
		return policy.MediaText
	case "application":
		return policy.MediaApplication
	case "message":
		return policy.MediaMessage
	case "data":
		return policy.MediaData
	case "control":
		return policy.MediaControl
	}

	return policy.MediaOther
}

func tcpTransport(proto string) bool {
	return strings.EqualFold(proto, "TCP") || strings.EqualFold(proto, "TCP/MSRP")
}

func bandwidth(s *sdp.Session, i int, typ string) (uint64, bool) {
	return s.Media[i].Bandwidth(typ)
}

// TS 29.213 Table 6.2.2, TS 29.214 Annex A.1
func subComponents(x sdpExchange, i int, dir sdp.Direction, muxed, tcp bool, flows map[int]flowNumbers) ([]policy.SubComponent, error) {
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

	proto := policy.ProtocolUDP
	if tcp {
		proto = policy.ProtocolTCP
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

	rtp := policy.SubComponent{Flows: ipFlows(proto, ueRTP, remoteRTP, uplink, downlink)}

	numbers := flowNumbers{sendrecv: dir == sdp.SendRecv}

	if muxed || tcp {
		numbers.rtp = 1
		if seen {
			numbers.rtp = prev.rtp
		}

		rtp.FlowNumber = numbers.rtp
		flows[i] = numbers

		return []policy.SubComponent{rtp}, nil
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

	rtcp := policy.SubComponent{
		FlowNumber: numbers.rtcp,
		Flows:      ipFlows(policy.ProtocolUDP, ueRTCP, remoteRTCP, true, true),
		Usage:      policy.FlowUsageRTCP,
	}

	if rtp.FlowNumber < rtcp.FlowNumber {
		return []policy.SubComponent{rtp, rtcp}, nil
	}

	return []policy.SubComponent{rtcp, rtp}, nil
}

func ipFlows(proto policy.Protocol, ue, remote sdp.Endpoint, uplink, downlink bool) []policy.Flow {
	var out []policy.Flow

	if uplink {
		out = append(out, policy.Flow{
			Uplink: true, Protocol: proto,
			Source: sourcePrefix(ue.Addr), Destination: netip.PrefixFrom(remote.Addr, remote.Addr.BitLen()), DestinationPort: remote.Port,
		})
	}

	if downlink {
		out = append(out, policy.Flow{
			Protocol: proto,
			Source:   sourcePrefix(remote.Addr), Destination: netip.PrefixFrom(ue.Addr, ue.Addr.BitLen()), DestinationPort: ue.Port,
		})
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
