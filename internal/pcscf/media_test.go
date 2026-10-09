package pcscf

import (
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/internal/rxpolicy"
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

// rxMediaComponents returns the components as the Rx backend sends them.
func rxMediaComponents(x sdpExchange, flows map[int]flowNumbers) ([]rx.MediaComponent, error) {
	c, err := mediaComponents(x, flows)

	return rxpolicy.MediaComponents(c), err
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

	got, err := rxMediaComponents(sdpExchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{})
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
		x    sdpExchange
	}{
		{"originating", sdpExchange{offer: ue, answer: network, offerFromUE: true}},
		{"terminating", sdpExchange{offer: network, answer: ue, offerFromUE: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rxMediaComponents(tc.x, map[int]flowNumbers{})
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

	got, err := rxMediaComponents(sdpExchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{})
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

	got, _ = rxMediaComponents(sdpExchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{})
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

			got, err := rxMediaComponents(sdpExchange{offer: offer, answer: answer, offerFromUE: tc.offerFromUE}, flows)
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

	got, err := rxMediaComponents(sdpExchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{})
	if err != nil {
		t.Fatal(err)
	}

	if r := (sdpExchange{offer: offer, answer: answer}).removed(); len(r) != 1 || r[0] != "2:video" {
		t.Errorf("removed %q, want 2:video", r)
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

	got, err := rxMediaComponents(sdpExchange{offer: offer, answer: answer, offerFromUE: true}, flows)
	if err != nil {
		t.Fatal(err)
	}

	subs := got[0].SubComponents
	if subs[0].FlowNumber != 1 || subs[0].FlowUsage == nil || subs[1].FlowNumber != 2 || subs[1].FlowUsage != nil {
		t.Fatalf("sub-components %+v, want RTCP as flow 1 below the RTP port", subs)
	}

	moved := audioSDP(t, "10.0.0.1", "3000")

	got, _ = rxMediaComponents(sdpExchange{offer: moved, answer: answer, offerFromUE: true}, flows)
	if subs := got[0].SubComponents; subs[0].FlowNumber != 1 || subs[0].FlowUsage == nil || subs[1].FlowNumber != 2 {
		t.Fatalf("sub-components %+v, want the numbers kept", subs)
	}
}

// TS 29.214 Annex A.1: IPv6 sources take the 64-bit prefix.
func TestIPv6FlowDescriptions(t *testing.T) {
	offer := audioSDP(t, "2001:db8:1::5", "4000", "a=rtcp-mux")
	answer := audioSDP(t, "2001:db8:2::7", "5000", "a=rtcp-mux")

	got, err := rxMediaComponents(sdpExchange{offer: offer, answer: answer, offerFromUE: false}, map[int]flowNumbers{})
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

	if _, err := rxMediaComponents(sdpExchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{}); err == nil {
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
	got := rxpolicy.SubscriptionIDs(subscribers([]string{"sip:+15551234567@ims.test;user=phone", "tel:+15551234567", "sip:alice@ims.test"}))
	want := []rx.SubscriptionID{
		{Type: rx.SubscriptionIDE164, Data: "15551234567"},
		{Type: rx.SubscriptionIDSIPURI, Data: "sip:alice@ims.test"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

const (
	crosscallCall  = "4g/crosscall-core-z5/call_precondition_failure_580/"
	pixelVideoCall = "5g/pixel-10a/call_to_crosscall-core-z5_video_callee_bye/"
)

func corpusSDP(t *testing.T, call, name string) *sdp.Session {
	t.Helper()

	b, err := os.ReadFile("../../sip/internal/corpus/testdata/ella/live/" + call + name)
	if err != nil {
		t.Fatal(err)
	}

	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))

	_, body, ok := bytes.Cut(b, []byte("\n\n"))
	if !ok {
		t.Fatalf("%s has no body", name)
	}

	s, err := sdp.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

// The offer and answer of two Crosscall Core-Z5 phones, as the originating P-CSCF sees them.
func TestMediaComponentFromLivePhones(t *testing.T) {
	got, err := rxMediaComponents(sdpExchange{
		offer: corpusSDP(t, crosscallCall, "001-INVITE.sip"), answer: corpusSDP(t, crosscallCall, "006-183-INVITE.sip"), offerFromUE: true,
	}, map[int]flowNumbers{})
	if err != nil {
		t.Fatal(err)
	}

	c := got[0]

	if *c.Type != rx.MediaAudio || *c.FlowStatus != rx.FlowStatusEnabled || *c.MaxRequestedBandwidthUL != 41000 ||
		*c.MaxRequestedBandwidthDL != 41000 || *c.RRBandwidth != 2000 || *c.RSBandwidth != 600 {
		t.Errorf("component %+v, want enabled audio at 41 kbit/s with RR 2000 and RS 600", c)
	}

	want := [][]string{
		{"permit in 17 from 10.46.0.10 to 10.46.0.9 50040", "permit out 17 from 10.46.0.9 to 10.46.0.10 50038"},
		{"permit in 17 from 10.46.0.10 to 10.46.0.9 50041", "permit out 17 from 10.46.0.9 to 10.46.0.10 50039"},
	}

	for i, s := range c.SubComponents {
		if !reflect.DeepEqual(s.FlowDescriptions, want[i]) {
			t.Errorf("flow %d: %q, want %q", s.FlowNumber, s.FlowDescriptions, want[i])
		}
	}
}

// TS 29.213 Table 6.2.1: a Pixel 10a video call to a Crosscall Core-Z5, as the originating P-CSCF sees it. Each m-line
// is its own component, its bandwidth from its own b= lines, the session-level ones ignored.
func TestMediaComponentsFromLiveVideoCall(t *testing.T) {
	got, err := rxMediaComponents(sdpExchange{
		offer: corpusSDP(t, pixelVideoCall, "001-INVITE.sip"), answer: corpusSDP(t, pixelVideoCall, "006-183-INVITE.sip"),
		offerFromUE: true,
	}, map[int]flowNumbers{})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("%d components, want audio and video", len(got))
	}

	for _, w := range []struct {
		number         uint32
		kind           rx.MediaType
		ul, dl, rr, rs uint32
		in, out        uint16
	}{
		{1, rx.MediaAudio, 41000, 42000, 2000, 600, 50032, 7010},
		{2, rx.MediaVideo, 401000, 560000, 6000, 5200, 60010, 26300},
	} {
		c := got[w.number-1]

		if c.Number != w.number || *c.Type != w.kind || *c.FlowStatus != rx.FlowStatusEnabled ||
			*c.MaxRequestedBandwidthUL != rx.Bandwidth(w.ul) || *c.MaxRequestedBandwidthDL != rx.Bandwidth(w.dl) ||
			*c.RRBandwidth != w.rr || *c.RSBandwidth != w.rs {
			t.Errorf("component %d: %+v, want enabled %v at %d/%d bit/s with RR %d and RS %d", w.number, c, w.kind, w.ul, w.dl, w.rr, w.rs)
		}

		want := [][]string{
			{fmt.Sprintf("permit in 17 from 10.46.0.7 to 10.46.0.8 %d", w.in), fmt.Sprintf("permit out 17 from 10.46.0.8 to 10.46.0.7 %d", w.out)},
			{fmt.Sprintf("permit in 17 from 10.46.0.7 to 10.46.0.8 %d", w.in+1), fmt.Sprintf("permit out 17 from 10.46.0.8 to 10.46.0.7 %d", w.out+1)},
		}

		for i, s := range c.SubComponents {
			if !reflect.DeepEqual(s.FlowDescriptions, want[i]) {
				t.Errorf("component %d flow %d: %q, want %q", w.number, s.FlowNumber, s.FlowDescriptions, want[i])
			}
		}
	}
}

// TS 29.213 Table 6.2.1/6.2.2: TCP media is ENABLED with both directions whatever its direction attribute.
func TestTCPMedia(t *testing.T) {
	offer := mustSDP(t, "v=0", "o=- 1 1 IN IP4 10.0.0.1", "s=-", "c=IN IP4 10.0.0.1", "t=0 0",
		"m=message 4000 TCP/MSRP *", "b=AS:64", "a=sendonly")
	answer := mustSDP(t, "v=0", "o=- 1 1 IN IP4 10.0.0.2", "s=-", "c=IN IP4 10.0.0.2", "t=0 0",
		"m=message 5000 TCP/MSRP *", "b=AS:64", "a=recvonly")

	got, err := rxMediaComponents(sdpExchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{})
	if err != nil {
		t.Fatal(err)
	}

	c := got[0]

	if *c.Type != rx.MediaMessage || *c.FlowStatus != rx.FlowStatusEnabled || len(c.SubComponents) != 1 ||
		!reflect.DeepEqual(c.SubComponents[0].FlowDescriptions, []string{
			"permit in 6 from 10.0.0.1 to 10.0.0.2 5000", "permit out 6 from 10.0.0.2 to 10.0.0.1 4000",
		}) {
		t.Fatalf("component %+v, want enabled TCP flows both ways", c)
	}
}

// Bandwidth comes from the media description only, never from the session level.
func TestSessionLevelBandwidthIgnored(t *testing.T) {
	offer := mustSDP(t, "v=0", "o=- 1 1 IN IP4 10.0.0.1", "s=-", "c=IN IP4 10.0.0.1", "b=AS:100", "t=0 0", "m=audio 4000 RTP/AVP 0")
	answer := mustSDP(t, "v=0", "o=- 1 1 IN IP4 10.0.0.2", "s=-", "c=IN IP4 10.0.0.2", "b=AS:100", "t=0 0", "m=audio 5000 RTP/AVP 0")

	got, err := rxMediaComponents(sdpExchange{offer: offer, answer: answer, offerFromUE: true}, map[int]flowNumbers{})
	if err != nil {
		t.Fatal(err)
	}

	if got[0].MaxRequestedBandwidthUL != nil || got[0].MaxRequestedBandwidthDL != nil {
		t.Fatalf("bandwidth %v %v, want none without a media b=AS", got[0].MaxRequestedBandwidthUL, got[0].MaxRequestedBandwidthDL)
	}
}

// TS 24.229 §7.2A.5.2.7
func TestChargingInfo(t *testing.T) {
	gprs := rx.IPCAN3GPPGPRS
	fiveGS := rx.IPCAN3GPP5GS
	non3GPP5GS := rx.IPCANNon3GPP5GS
	non3GPPEPS := rx.IPCANNon3GPPEPS
	ids := func(n int) []rx.AccessNetworkChargingIdentifier {
		out := make([]rx.AccessNetworkChargingIdentifier, n)
		for i := range out {
			out[i] = rx.AccessNetworkChargingIdentifier{Value: []byte{byte(i)}}
		}

		return out
	}

	for name, tc := range map[string]struct {
		a    rx.AAAnswer
		want string
	}{
		"none":       {rx.AAAnswer{}, ""},
		"no address": {rx.AAAnswer{AccessNetworkChargingIdentifiers: ids(1)}, ""},
		"not EPS": {rx.AAAnswer{
			AccessNetworkChargingIdentifiers: ids(1), AccessNetworkChargingAddress: netip.MustParseAddr("192.0.2.1"),
			AccessNetwork: rx.AccessNetwork{IPCANType: &gprs},
		}, ""},
		"5GS": {rx.AAAnswer{
			AccessNetworkChargingIdentifiers: []rx.AccessNetworkChargingIdentifier{
				{Value: []byte{0xab, 0x01}, Flows: []rx.Flows{{MediaComponentNumber: 1, FlowNumbers: []uint32{1}}}},
			},
			AccessNetworkChargingAddress: netip.MustParseAddr("192.0.2.2"),
			AccessNetwork:                rx.AccessNetwork{IPCANType: &fiveGS},
		}, `smf=192.0.2.2;5gs-info="5gs-item=1;5gscid=AB01;flow-id=({1,1})"`},
		// TS 29.214 Table E.2-1, TS 24.229 §7.2A.5.2.10
		"non-3GPP 5GS": {rx.AAAnswer{
			AccessNetworkChargingIdentifiers: ids(1), AccessNetworkChargingAddress: netip.MustParseAddr("192.0.2.2"),
			AccessNetwork: rx.AccessNetwork{IPCANType: &non3GPP5GS},
		}, `smf=192.0.2.2;5gs-info="5gs-item=1;5gscid=00"`},
		// TS 24.229 §7.2A.5.2.3: EPC via WLAN has no ecid.
		"non-3GPP EPS": {rx.AAAnswer{
			AccessNetworkChargingIdentifiers: ids(1), AccessNetworkChargingAddress: netip.MustParseAddr("192.0.2.2"),
			AccessNetwork: rx.AccessNetwork{IPCANType: &non3GPPEPS},
		}, ""},
		// TS 24.229 §7.2A.5.2.10: one item per PDU session, whose identifier comes once per QoS flow.
		"one item per identifier": {rx.AAAnswer{
			AccessNetworkChargingIdentifiers: []rx.AccessNetworkChargingIdentifier{
				{Value: []byte{0xab}, Flows: []rx.Flows{{MediaComponentNumber: 1, FlowNumbers: []uint32{1}}}},
				{Value: []byte{0xcd}, Flows: []rx.Flows{{MediaComponentNumber: 3, FlowNumbers: []uint32{1}}}},
				{Value: []byte{0xab}, Flows: []rx.Flows{
					{MediaComponentNumber: 2, FlowNumbers: []uint32{1}}, {MediaComponentNumber: 1, FlowNumbers: []uint32{1}},
				}},
			},
			AccessNetworkChargingAddress: netip.MustParseAddr("192.0.2.2"),
			AccessNetwork:                rx.AccessNetwork{IPCANType: &fiveGS},
		}, `smf=192.0.2.2;5gs-info="5gs-item=1;5gscid=AB;flow-id=({1,1},{2,1}),5gs-item=2;5gscid=CD;flow-id=({3,1})"`},
		// TS 24.229 Table 7.2A.5: ecid and 5gscid are 1*HEXDIG.
		"empty identifier": {rx.AAAnswer{
			AccessNetworkChargingIdentifiers: []rx.AccessNetworkChargingIdentifier{{}, {Value: []byte{1}}},
			AccessNetworkChargingAddress:     netip.MustParseAddr("192.0.2.1"),
		}, `pdngw=192.0.2.1;eps-info="eps-item=1;eps-sig=no;ecid=01"`},
		"only empty identifiers": {rx.AAAnswer{
			AccessNetworkChargingIdentifiers: []rx.AccessNetworkChargingIdentifier{{}},
			AccessNetworkChargingAddress:     netip.MustParseAddr("192.0.2.1"),
		}, ""},
		"IPv6 gateway": {rx.AAAnswer{
			AccessNetworkChargingIdentifiers: ids(1), AccessNetworkChargingAddress: netip.MustParseAddr("2001:db8::1"),
		}, `pdngw=[2001:db8::1];eps-info="eps-item=1;eps-sig=no;ecid=00"`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := chargingInfo(rxpolicy.AnswerCharging(tc.a), nil); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	many := chargingInfo(rxpolicy.AnswerCharging(rx.AAAnswer{AccessNetworkChargingIdentifiers: ids(12), AccessNetworkChargingAddress: netip.MustParseAddr("192.0.2.1")}), nil)
	if strings.Count(many, "eps-item=") != maxItems {
		t.Fatalf("%q, want at most %d eps-item", many, maxItems)
	}
}
