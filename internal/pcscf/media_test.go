package pcscf

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/sip/sdp"
)

func mustSDP(t *testing.T, lines ...string) *sdp.Session {
	t.Helper()

	s, err := sdp.Parse([]byte(strings.Join(lines, "\r\n") + "\r\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	return s
}

func audioSDP(t *testing.T, addr string, port string, extra ...string) *sdp.Session {
	t.Helper()

	ip := "IP4"
	if strings.Contains(addr, ":") {
		ip = "IP6"
	}

	lines := []string{
		"v=0", "o=- 1 1 IN " + ip + " " + addr, "s=-", "c=IN " + ip + " " + addr, "t=0 0",
		"m=audio " + port + " RTP/AVP 116", "b=AS:41", "b=RS:600", "b=RR:2000", "a=rtpmap:116 AMR-WB/16000",
	}

	return mustSDP(t, append(lines, extra...)...)
}

func TestMediaComponentFromTheCallerSide(t *testing.T) {
	offer := audioSDP(t, "10.0.0.1", "4000")
	answer := audioSDP(t, "10.0.0.2", "5000", "b=AS:38")

	got, err := mediaComponents(exchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{})
	if err != nil {
		t.Fatalf("mediaComponents: %v", err)
	}

	want := []rx.MediaComponent{{
		Number:                  1,
		Type:                    new(rx.MediaAudio),
		FlowStatus:              new(rx.FlowStatusEnabled),
		MaxRequestedBandwidthUL: new(rx.Bandwidth(41000)),
		MaxRequestedBandwidthDL: new(rx.Bandwidth(41000)),
		RRBandwidth:             new(uint32(2000)),
		RSBandwidth:             new(uint32(600)),
		CodecData: []rx.CodecData{
			{Direction: rx.CodecUplink, Kind: rx.CodecOffer, SDP: "m=audio 4000 RTP/AVP 116\na=rtpmap:116 AMR-WB/16000"},
			{Direction: rx.CodecDownlink, Kind: rx.CodecAnswer, SDP: "m=audio 5000 RTP/AVP 116\na=rtpmap:116 AMR-WB/16000"},
		},
		SubComponents: []rx.MediaSubComponent{
			{FlowNumber: 1, FlowDescriptions: []string{
				"permit in 17 from 10.0.0.1 to 10.0.0.2 5000",
				"permit out 17 from 10.0.0.2 to 10.0.0.1 4000",
			}},
			{FlowNumber: 2, FlowUsage: new(rx.FlowUsageRTCP), FlowDescriptions: []string{
				"permit in 17 from 10.0.0.1 to 10.0.0.2 5001",
				"permit out 17 from 10.0.0.2 to 10.0.0.1 4001",
			}},
		},
	}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// TS 29.213 Table 6.2.1: UL from the downlink SDP, DL from the uplink SDP.
func TestBandwidthDirections(t *testing.T) {
	ue := audioSDP(t, "10.0.0.1", "4000")
	ue.Media[0].SetBandwidth(sdp.BandwidthAS, 30)

	network := audioSDP(t, "10.0.0.2", "5000")
	network.Media[0].SetBandwidth(sdp.BandwidthAS, 50)

	for _, tc := range []struct {
		name string
		x    exchange
	}{
		{"originating", exchange{offer: ue, answer: network, offerFromUE: true}},
		{"terminating", exchange{offer: network, answer: ue, offerFromUE: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mediaComponents(tc.x, map[int]flowNumbers{})
			if err != nil {
				t.Fatal(err)
			}

			if *got[0].MaxRequestedBandwidthUL != 50000 || *got[0].MaxRequestedBandwidthDL != 30000 {
				t.Fatalf("UL %d DL %d, want 50000 from the network and 30000 from the UE",
					*got[0].MaxRequestedBandwidthUL, *got[0].MaxRequestedBandwidthDL)
			}
		})
	}
}

func TestRTCPMux(t *testing.T) {
	offer := audioSDP(t, "10.0.0.1", "4000", "a=rtcp-mux", "a=inactive")
	answer := audioSDP(t, "10.0.0.2", "5000", "a=rtcp-mux", "a=inactive")

	got, err := mediaComponents(exchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{})
	if err != nil {
		t.Fatal(err)
	}

	c := got[0]

	if *c.FlowStatus != rx.FlowStatusEnabled {
		t.Errorf("Flow-Status %s, want ENABLED with rtcp-mux", *c.FlowStatus)
	}

	if *c.MaxRequestedBandwidthUL != 41000+2000+600 {
		t.Errorf("UL %d, want AS×1000+RR+RS", *c.MaxRequestedBandwidthUL)
	}

	if len(c.SubComponents) != 1 || c.SubComponents[0].FlowUsage != nil || len(c.SubComponents[0].FlowDescriptions) != 2 {
		t.Fatalf("sub-components %+v, want one RTP/RTCP flow in both directions", c.SubComponents)
	}

	offer.Media[0].Del('b')
	answer.Media[0].Del('b')
	offer.Media[0].SetBandwidth(sdp.BandwidthAS, 40)
	answer.Media[0].SetBandwidth(sdp.BandwidthAS, 40)

	got, _ = mediaComponents(exchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{})
	if *got[0].MaxRequestedBandwidthDL != 42000 {
		t.Errorf("DL %d, want AS×1050 without RR and RS", *got[0].MaxRequestedBandwidthDL)
	}
}

func TestFlowStatusAndDirections(t *testing.T) {
	for _, tc := range []struct {
		name             string
		offer, answer    string
		offerFromUE      bool
		status           rx.FlowStatus
		uplink, downlink bool
		previousSendRecv bool
	}{
		{"UE answers recvonly", "a=sendonly", "a=recvonly", false, rx.FlowStatusEnabledDownlink, false, true, false},
		{"network answers recvonly", "a=sendonly", "a=recvonly", true, rx.FlowStatusEnabledUplink, true, false, false},
		{"UE answers sendonly", "a=recvonly", "a=sendonly", false, rx.FlowStatusEnabledUplink, true, false, false},
		{"network answers sendonly", "a=recvonly", "a=sendonly", true, rx.FlowStatusEnabledDownlink, false, true, false},
		{"inactive offer wins", "a=inactive", "a=sendrecv", true, rx.FlowStatusDisabled, true, true, false},
		{"hold after sendrecv keeps both", "a=sendonly", "a=recvonly", true, rx.FlowStatusEnabledUplink, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			offer := audioSDP(t, "10.0.0.1", "4000", tc.offer)
			answer := audioSDP(t, "10.0.0.2", "5000", tc.answer)

			flows := map[int]flowNumbers{}
			if tc.previousSendRecv {
				flows[0] = flowNumbers{rtp: 1, rtcp: 2, sendrecv: true}
			}

			got, err := mediaComponents(exchange{offer: offer, answer: answer, offerFromUE: tc.offerFromUE}, flows)
			if err != nil {
				t.Fatal(err)
			}

			if *got[0].FlowStatus != tc.status {
				t.Errorf("Flow-Status %s, want %s", *got[0].FlowStatus, tc.status)
			}

			var up, down bool

			for _, d := range got[0].SubComponents[0].FlowDescriptions {
				up = up || strings.HasPrefix(d, "permit in")
				down = down || strings.HasPrefix(d, "permit out")
			}

			if up != tc.uplink || down != tc.downlink {
				t.Errorf("RTP flows %q, want uplink %t downlink %t", got[0].SubComponents[0].FlowDescriptions, tc.uplink, tc.downlink)
			}

			if n := len(got[0].SubComponents[1].FlowDescriptions); n != 2 {
				t.Errorf("RTCP flows %d, want both directions", n)
			}
		})
	}
}

func TestRemovedMedia(t *testing.T) {
	offer := audioSDP(t, "10.0.0.1", "4000", "m=video 4002 RTP/AVP 99")
	answer := audioSDP(t, "10.0.0.2", "5000", "m=video 0 RTP/AVP 99")

	got, err := mediaComponents(exchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 || got[1].Number != 2 || *got[1].Type != rx.MediaVideo || *got[1].FlowStatus != rx.FlowStatusRemoved ||
		got[1].SubComponents != nil {
		t.Fatalf("video %+v, want component 2 REMOVED without flows", got[1])
	}
}

// TS 29.213 Table 6.2.2: Flow-Number by increasing downlink destination port, kept for the session.
func TestFlowNumbers(t *testing.T) {
	offer := audioSDP(t, "10.0.0.1", "4000", "a=rtcp:3999")
	answer := audioSDP(t, "10.0.0.2", "5000")

	flows := map[int]flowNumbers{}

	got, err := mediaComponents(exchange{offer: offer, answer: answer, offerFromUE: true}, flows)
	if err != nil {
		t.Fatal(err)
	}

	subs := got[0].SubComponents
	if subs[0].FlowNumber != 1 || subs[0].FlowUsage == nil || subs[1].FlowNumber != 2 || subs[1].FlowUsage != nil {
		t.Fatalf("sub-components %+v, want RTCP as flow 1 below the RTP port", subs)
	}

	moved := audioSDP(t, "10.0.0.1", "3000")

	got, _ = mediaComponents(exchange{offer: moved, answer: answer, offerFromUE: true}, flows)
	if subs := got[0].SubComponents; subs[0].FlowNumber != 1 || subs[0].FlowUsage == nil || subs[1].FlowNumber != 2 {
		t.Fatalf("sub-components %+v, want the numbers kept", subs)
	}
}

// TS 29.214 Annex A.1: IPv6 sources take the 64-bit prefix.
func TestIPv6FlowDescriptions(t *testing.T) {
	offer := audioSDP(t, "2001:db8:1::5", "4000", "a=rtcp-mux")
	answer := audioSDP(t, "2001:db8:2::7", "5000", "a=rtcp-mux")

	got, err := mediaComponents(exchange{offer: offer, answer: answer, offerFromUE: false}, map[int]flowNumbers{})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"permit in 17 from 2001:db8:2::/64 to 2001:db8:1::5 4000",
		"permit out 17 from 2001:db8:1::/64 to 2001:db8:2::7 5000",
	}

	if d := got[0].SubComponents[0].FlowDescriptions; !reflect.DeepEqual(d, want) {
		t.Fatalf("flows %q, want %q", d, want)
	}

	if cd := got[0].CodecData; cd[0].Direction != rx.CodecDownlink || cd[1].Direction != rx.CodecUplink {
		t.Fatalf("Codec-Data %+v, want a downlink offer and an uplink answer", cd)
	}
}

func TestMixedFamiliesRefused(t *testing.T) {
	offer := audioSDP(t, "10.0.0.1", "4000")
	answer := audioSDP(t, "2001:db8::7", "5000")

	if _, err := mediaComponents(exchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{}); err == nil {
		t.Fatal("mediaComponents accepted IPv4 and IPv6 media")
	}
}

func TestICSIRef(t *testing.T) {
	got := icsiRef([]string{`*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`})
	if got != "urn:urn-7:3gpp-service.ims.icsi.mmtel" {
		t.Fatalf("icsiRef = %q", got)
	}
}

func TestSubscriptionIDs(t *testing.T) {
	got := subscriptionIDs([]string{"sip:+15551234567@ims.test;user=phone", "tel:+15551234567", "sip:alice@ims.test"})
	want := []rx.SubscriptionID{
		{Type: rx.SubscriptionIDE164, Data: "15551234567"},
		{Type: rx.SubscriptionIDE164, Data: "15551234567"},
		{Type: rx.SubscriptionIDSIPURI, Data: "sip:alice@ims.test"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
