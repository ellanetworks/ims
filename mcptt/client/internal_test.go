package client

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/mcptt/controlchannel"
	"github.com/ellanetworks/ims/mcptt/rtp"
)

var addr = netip.MustParseAddr("192.0.2.1")

// TS 24.379 §6.2.1, TS 26.179 §4.1.3, TS 24.380 §14.2: the offer of a call: the speech in AMR-WB, i=speech, its b=AS
// of 23.85 kbit/s (TS 24.229 §6.1.1, TS 26.114 Tables K.5, K.6), ptime 20 and maxptime 240; the control channel with
// mc_implicit_request, mc_granted and mc_floor_ssrc.
func TestOffer(t *testing.T) {
	b, err := offer(addr, 7, 30000, 30002, controlchannel.Params{
		ImplicitRequest: true, Granted: true, FloorSSRC: 1234, HasFloorSSRC: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "v=0\r\no=- 7 1 IN IP4 192.0.2.1\r\ns=-\r\nc=IN IP4 192.0.2.1\r\nt=0 0\r\n" +
		"m=audio 30000 RTP/AVP 96\r\ni=speech\r\nb=AS:41\r\na=rtpmap:96 AMR-WB/16000/1\r\n" +
		"a=fmtp:96 mode-change-capability=2;max-red=0\r\na=ptime:20\r\na=maxptime:240\r\n" +
		"m=application 30002 udp MCPTT\r\na=fmtp:MCPTT mc_granted;mc_implicit_request;mc_floor_ssrc=1234\r\n"
	if string(b) != want {
		t.Errorf("offer\n%s\nwant\n%s", b, want)
	}

	b, err = offer(netip.MustParseAddr("2001:db8::1"), 7, 30000, 30002, controlchannel.Params{})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(b), "\r\nb=AS:49\r\n") {
		t.Errorf("IPv6 offer\n%s", b)
	}
}

// TS 24.379 §6.2.2, RFC 3264 §6, RFC 4867 §8.3.1, TS 24.380 §14.3: the answer to an offer of the server: the first
// AMR-WB payload type the source can send, its octet-align and mode-set kept, the unknown parameters and the
// server's mode-change-period removed, the client's mode-change-capability and max-red declared, in the direction
// reversed, the b=AS of its highest mode (TS 26.114 Table K.7); other streams refused; the control channel answered
// with mc_priority as offered (§14.3.3) and the client's mc_floor_ssrc.
func TestAnswerOffer(t *testing.T) {
	o := "v=0\r\no=- 1 1 IN IP4 192.0.2.100\r\ns=-\r\nc=IN IP4 192.0.2.100\r\nt=0 0\r\n" +
		"m=audio 20000 RTP/AVP 98 97 96\r\na=rtpmap:98 AMR-WB/16000/1\r\na=fmtp:98 octet-align=1;crc=1\r\n" +
		"a=rtpmap:97 AMR-WB/16000/1\r\na=fmtp:97 octet-align=1;mode-set=0,1;mode-change-period=2;max-red=40;x-vendor=1\r\n" +
		"a=rtpmap:96 AMR-WB/16000/1\r\na=recvonly\r\n" +
		"m=video 20010 RTP/AVP 100\r\na=rtpmap:100 H264/90000\r\n" +
		"m=application 21000 udp MCPTT\r\na=fmtp:MCPTT mc_priority=5;mc_floor_ssrc=99\r\n"

	b, n, err := answerOffer([]byte(o), addr, 7, 30000, 30002, controlchannel.Params{Priority: 255, FloorSSRC: 4, HasFloorSSRC: true})
	if err != nil {
		t.Fatal(err)
	}

	want := "v=0\r\no=- 7 1 IN IP4 192.0.2.1\r\ns=-\r\nc=IN IP4 192.0.2.1\r\nt=0 0\r\n" +
		"m=audio 30000 RTP/AVP 97\r\ni=speech\r\nb=AS:26\r\na=rtpmap:97 AMR-WB/16000/1\r\n" +
		"a=fmtp:97 octet-align=1;mode-set=0,1;mode-change-capability=2;max-red=0\r\n" +
		"a=ptime:20\r\na=maxptime:240\r\na=sendonly\r\n" +
		"m=video 0 RTP/AVP 100\r\na=rtpmap:100 H264/90000\r\n" +
		"m=application 30002 udp MCPTT\r\na=fmtp:MCPTT mc_priority=5;mc_floor_ssrc=4\r\n"
	if string(b) != want {
		t.Errorf("answer\n%s\nwant\n%s", b, want)
	}

	if f := n.format; f.pt != 97 || !f.octetAligned || f.mode != 0 {
		t.Errorf("format %+v", f)
	}

	if r := n.remote; r.audio.String() != "192.0.2.100:20000" || r.rtcp.Port() != 20001 || r.control.String() != "192.0.2.100:21000" {
		t.Errorf("remote %+v", r)
	}

	if !n.offered.HasFloorSSRC || n.offered.FloorSSRC != 99 {
		t.Errorf("offered %+v", n.offered)
	}

	// Without a control channel, or without AMR-WB to send, no answer.
	for _, bad := range []string{
		strings.Split(o, "m=application")[0],
		strings.Replace(o, "a=fmtp:97 octet-align=1;mode-set=0,1;mode-change-period=2;max-red=40;x-vendor=1\r\n"+
			"a=rtpmap:96 AMR-WB/16000/1\r\n", "a=fmtp:97 interleaving=4\r\n", 1),
		// RTP/SAVP refused [PM 9].
		strings.Replace(o, "RTP/AVP 98 97 96", "RTP/SAVP 98 97 96", 1),
	} {
		if _, _, err := answerOffer([]byte(bad), addr, 7, 30000, 30002, controlchannel.Params{}); err == nil {
			t.Errorf("answered\n%s", bad)
		}
	}
}

// TS 24.380 §14.4, RFC 4867 §8.3.1: the answer to the client's offer: mc_ssrc taken though not offered, parameters
// not offered discarded; an answer in octet-aligned mode, or without the control channel, refused.
func TestTakeAnswer(t *testing.T) {
	local := controlchannel.Params{ImplicitRequest: true, FloorSSRC: 4, HasFloorSSRC: true}
	a := "v=0\r\no=- 1 1 IN IP4 192.0.2.100\r\ns=-\r\nc=IN IP4 192.0.2.100\r\nt=0 0\r\n" +
		"m=audio 20000 RTP/AVP 96\r\na=rtpmap:96 AMR-WB/16000/1\r\na=fmtp:96 mode-set=8\r\n" +
		"m=application 21000 udp MCPTT\r\na=fmtp:MCPTT mc_queueing;mc_implicit_request;mc_ssrc=77;mc_floor_ssrc=5\r\n"

	n, err := takeAnswer([]byte(a), local)
	if err != nil {
		t.Fatal(err)
	}

	if c := n.control; !c.ImplicitRequest || c.Queueing || !c.HasSSRC || c.SSRC != 77 || c.FloorSSRC != 5 || n.format.mode != 8 {
		t.Errorf("negotiated %+v", n)
	}

	for _, bad := range []string{
		strings.Replace(a, "mode-set=8", "octet-align=1", 1),
		strings.Replace(a, "m=application 21000", "m=application 0", 1),
		strings.Replace(a, "m=audio 20000 RTP/AVP 96", "m=audio 20000 RTP/AVP 97", 1),
	} {
		if _, err := takeAnswer([]byte(bad), local); err == nil {
			t.Errorf("taken\n%s", bad)
		}
	}
}

// TS 22.179 R-6.15.3.2-016, -017: the losses of the speech heard, by where they fall: a burst whose marked first packet
// is missing lost its start; missing before a marked first, the end of the burst before; within, mid-burst.
func TestHeardTrack(t *testing.T) {
	var h Heard

	pkt := func(seq uint16, ts uint32, marker bool) {
		h.track(rtp.Packet{Sequence: seq, Timestamp: ts, Marker: marker}, 1)
	}

	// Burst 1: 10, 11, 13 (12 lost mid-burst).
	pkt(10, 0, true)
	pkt(11, 320, false)
	pkt(13, 960, false)
	// Burst 2 after a silence, its first two (14, 15) lost: 16 unmarked.
	pkt(16, 960+320*20, false)
	pkt(17, 960+320*21, false)
	// Burst 3 after a silence, the end of burst 2 lost (18): 19 marked.
	pkt(19, 960+320*50, true)

	if h.Bursts != 3 || h.MidLost != 1 || h.InitialLost != 2 || h.TrailingLost != 1 {
		t.Errorf("%+v", h)
	}
}
