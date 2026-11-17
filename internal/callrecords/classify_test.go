package callrecords

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip/proxy"
)

func TestOutcome(t *testing.T) {
	tests := []struct {
		status  int
		by      db.CallParty
		alerted bool
		want    db.CallOutcome
	}{
		{200, db.PartyCaller, false, db.OutcomeAnswered},
		{202, db.PartyNetwork, false, db.OutcomeAnswered},
		{487, db.PartyCaller, true, db.OutcomeCancelled},
		{487, db.PartyCallee, true, db.OutcomeFailed},
		{487, db.PartyNetwork, false, db.OutcomeFailed},
		{486, db.PartyCallee, false, db.OutcomeBusy},
		{600, db.PartyCallee, true, db.OutcomeBusy},
		{603, db.PartyCallee, true, db.OutcomeRejected},
		{408, db.PartyCallee, true, db.OutcomeNoAnswer},
		{408, db.PartyNetwork, true, db.OutcomeNoAnswer},
		{480, db.PartyCallee, true, db.OutcomeUnavailable},
		{480, db.PartyNetwork, true, db.OutcomeUnavailable},
		{408, db.PartyCallee, false, db.OutcomeFailed},
		{480, db.PartyNetwork, false, db.OutcomeUnavailable},
		{403, db.PartyNetwork, false, db.OutcomeFailed},
		{404, db.PartyNetwork, false, db.OutcomeFailed},
		{380, db.PartyNetwork, false, db.OutcomeFailed},
		{500, db.PartyNetwork, false, db.OutcomeFailed},
		{503, db.PartyCallee, true, db.OutcomeFailed},
		{488, db.PartyCallee, false, db.OutcomeFailed},
	}

	for _, tt := range tests {
		if got := Outcome(tt.status, tt.by, tt.alerted); got != tt.want {
			t.Errorf("Outcome(%d, %s, alerted %t) = %s, want %s", tt.status, tt.by, tt.alerted, got, tt.want)
		}
	}
}

func TestEndedBy(t *testing.T) {
	tests := []struct {
		cause proxy.EndCause
		by    proxy.Side
		want  db.CallParty
	}{
		{proxy.EndBye, proxy.Caller, db.PartyCaller},
		{proxy.EndBye, proxy.Callee, db.PartyCallee},
		{proxy.EndFailed, proxy.Caller, db.PartyCaller},
		{proxy.EndFailed, proxy.Callee, db.PartyCallee},
		{proxy.EndFailed, 0, db.PartyNetwork},
		{proxy.EndReleased, 0, db.PartyNetwork},
		{proxy.EndLost, proxy.Callee, db.PartyNetwork},
		{proxy.EndLost, 0, db.PartyNetwork},
		{proxy.EndNoAck, 0, db.PartyNetwork},
		{proxy.EndExpired, 0, db.PartyNetwork},
		{proxy.EndDiscarded, 0, db.PartyNetwork},
	}

	for _, tt := range tests {
		if got := EndedBy(tt.cause, tt.by); got != tt.want {
			t.Errorf("EndedBy(%s, %s) = %s, want %s", tt.cause, tt.by, got, tt.want)
		}
	}
}

func TestDuration(t *testing.T) {
	start := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	answered := db.CallRecord{Outcome: db.OutcomeAnswered, DeliveryStartAt: start, DeliveryEndAt: start.Add(90 * time.Second)}
	if d, ok := Duration(answered); !ok || d != 90*time.Second {
		t.Fatalf("Duration of an ended call = %v, %t; want 1m30s", d, ok)
	}

	inCall := answered
	inCall.DeliveryEndAt = time.Time{}

	if _, ok := Duration(inCall); ok {
		t.Fatal("a call in progress has a duration")
	}

	if _, ok := Duration(db.CallRecord{Outcome: db.OutcomeBusy, DeliveryStartAt: start}); ok {
		t.Fatal("an unanswered call has a duration")
	}
}

func TestNegotiatedMedia(t *testing.T) {
	answer := strings.Join([]string{
		"v=0", "o=- 1 1 IN IP4 10.0.0.2", "s=-", "c=IN IP4 10.0.0.2", "t=0 0",
		"m=audio 5000 RTP/AVP 116", "m=video 0 RTP/AVP 99", "m=audio 5002 RTP/AVP 116", "m=text 5004 RTP/AVP 98",
	}, "\r\n") + "\r\n"

	if got, _ := Negotiated(nil, []byte(answer)); !slices.Equal(got, []string{"audio", "text"}) {
		t.Fatalf("Negotiated media = %v, want audio and text, not the rejected video", got)
	}

	if got, _ := Negotiated(nil, []byte("not SDP")); got != nil {
		t.Fatalf("Negotiated media of no SDP = %v", got)
	}

	bundled := strings.Join([]string{
		"v=0", "o=- 1 1 IN IP4 10.0.0.2", "s=-", "c=IN IP4 10.0.0.2", "t=0 0", "a=group:BUNDLE a v",
		"m=audio 5000 RTP/AVP 116", "a=mid:a", "m=video 0 RTP/AVP 99", "a=mid:v", "a=bundle-only",
	}, "\r\n") + "\r\n"

	if got, _ := Negotiated(nil, []byte(bundled)); !slices.Equal(got, []string{"audio", "video"}) {
		t.Fatalf("Negotiated media = %v, want audio and the bundle-only video", got)
	}
}

func sdpOf(media ...string) []byte {
	return []byte(strings.Join(append([]string{
		"v=0", "o=- 1 1 IN IP4 10.0.0.2", "s=-", "c=IN IP4 10.0.0.2", "t=0 0",
	}, media...), "\r\n") + "\r\n")
}

func TestNegotiatedCodecs(t *testing.T) {
	volteOffer := sdpOf(
		"m=audio 5000 RTP/AVP 116 107 97 111 110",
		"a=rtpmap:116 AMR-WB/16000/1", "a=fmtp:116 mode-change-capability=2;max-red=0",
		"a=rtpmap:107 AMR-WB/16000/1", "a=fmtp:107 octet-align=1",
		"a=rtpmap:97 AMR/8000/1", "a=rtpmap:111 telephone-event/16000", "a=rtpmap:110 telephone-event/8000",
	)
	amrWB := db.Codec{Media: "audio", Encoding: "AMR-WB", ClockRate: 16000, Channels: 1}
	amr := db.Codec{Media: "audio", Encoding: "AMR", ClockRate: 8000, Channels: 1}

	tests := []struct {
		name          string
		offer, answer []byte
		want          []db.Codec
	}{
		{
			name:  "one codec with telephone-event",
			offer: volteOffer,
			answer: sdpOf(
				"m=audio 6000 RTP/AVP 116 111", "a=rtpmap:116 AMR-WB/16000/1", "a=rtpmap:111 telephone-event/16000",
			),
			want: []db.Codec{amrWB},
		},
		{
			name:   "a dynamic payload type numbered differently from the offer",
			offer:  volteOffer,
			answer: sdpOf("m=audio 6000 RTP/AVP 96", "a=rtpmap:96 amr-wb/16000"),
			want:   []db.Codec{{Media: "audio", Encoding: "amr-wb", ClockRate: 16000, Channels: 1}},
		},
		{
			name:   "several codecs, in the order of the answer, once each",
			offer:  volteOffer,
			answer: sdpOf("m=audio 6000 RTP/AVP 97 116 107", "a=rtpmap:97 AMR/8000", "a=rtpmap:116 AMR-WB/16000", "a=rtpmap:107 AMR-WB/16000/1"),
			want:   []db.Codec{amr, amrWB},
		},
		{
			name: "EVS",
			offer: sdpOf("m=audio 5000 RTP/AVP 96 116", "a=rtpmap:96 EVS/16000", "a=fmtp:96 br=5.9-24.4;bw=nb-swb",
				"a=rtpmap:116 AMR-WB/16000"),
			answer: sdpOf("m=audio 6000 RTP/AVP 96", "a=rtpmap:96 EVS/16000", "a=fmtp:96 br=13.2;bw=swb"),
			want:   []db.Codec{{Media: "audio", Encoding: "EVS", ClockRate: 16000, Channels: 1}},
		},
		{
			name:   "static payload types without rtpmap, comfort noise left out",
			offer:  sdpOf("m=audio 5000 RTP/AVP 8 0 13 101", "a=rtpmap:101 telephone-event/8000"),
			answer: sdpOf("m=audio 6000 RTP/AVP 8 13 101", "a=rtpmap:101 telephone-event/8000"),
			want:   []db.Codec{{Media: "audio", Encoding: "PCMA", ClockRate: 8000, Channels: 1}},
		},
		{
			name:   "a format of the answer not in the offer",
			offer:  sdpOf("m=audio 5000 RTP/AVP 0"),
			answer: sdpOf("m=audio 6000 RTP/AVP 0 8"),
			want:   []db.Codec{{Media: "audio", Encoding: "PCMU", ClockRate: 8000, Channels: 1}},
		},
		{
			name:   "a different number of channels",
			offer:  sdpOf("m=audio 5000 RTP/AVP 96", "a=rtpmap:96 opus/48000/2"),
			answer: sdpOf("m=audio 6000 RTP/AVP 96", "a=rtpmap:96 opus/48000/1"),
		},
		{
			name:   "a dynamic payload type without rtpmap",
			offer:  volteOffer,
			answer: sdpOf("m=audio 6000 RTP/AVP 116"),
		},
		{
			name: "a rejected stream and a video stream",
			offer: sdpOf("m=audio 5000 RTP/AVP 116", "a=rtpmap:116 AMR-WB/16000", "m=video 5002 RTP/AVP 99",
				"a=rtpmap:99 H264/90000", "m=video 5004 RTP/AVP 100", "a=rtpmap:100 H265/90000"),
			answer: sdpOf("m=audio 0 RTP/AVP 116", "a=rtpmap:116 AMR-WB/16000", "m=video 6002 RTP/AVP 99",
				"a=rtpmap:99 H264/90000"),
			want: []db.Codec{{Media: "video", Encoding: "H264", ClockRate: 90000}},
		},
		{
			name:   "on hold",
			offer:  sdpOf("m=audio 5000 RTP/AVP 116", "a=rtpmap:116 AMR-WB/16000", "a=sendonly"),
			answer: sdpOf("m=audio 6000 RTP/AVP 116", "a=rtpmap:116 AMR-WB/16000", "a=recvonly"),
			want:   []db.Codec{amrWB},
		},
		{
			name:   "not RTP",
			offer:  sdpOf("m=message 5000 TCP/MSRP *", "m=image 5002 udptl t38"),
			answer: sdpOf("m=message 6000 TCP/MSRP *", "m=image 6002 udptl t38"),
		},
		{
			name:   "no media",
			offer:  sdpOf(),
			answer: sdpOf(),
		},
		{
			name:   "more streams in the answer than in the offer",
			offer:  sdpOf("m=audio 5000 RTP/AVP 0"),
			answer: sdpOf("m=audio 0 RTP/AVP 0", "m=audio 6000 RTP/AVP 0"),
		},
		{
			name:   "AMR-WB in a different payload format configuration",
			offer:  sdpOf("m=audio 5000 RTP/AVP 107", "a=rtpmap:107 AMR-WB/16000", "a=fmtp:107 octet-align=1"),
			answer: sdpOf("m=audio 6000 RTP/AVP 107", "a=rtpmap:107 AMR-WB/16000"),
		},
		{
			name: "AMR-WB in the octet-aligned configuration",
			offer: sdpOf("m=audio 5000 RTP/AVP 116 107", "a=rtpmap:116 AMR-WB/16000", "a=rtpmap:107 AMR-WB/16000",
				"a=fmtp:107 octet-align=1; mode-change-capability=2"),
			answer: sdpOf("m=audio 6000 RTP/AVP 107", "a=rtpmap:107 AMR-WB/16000", "a=fmtp:107 octet-align=1"),
			want:   []db.Codec{amrWB},
		},
		{
			name: "retransmission, redundancy and FEC left out",
			offer: sdpOf("m=video 5000 RTP/AVPF 99 97 98 100 101", "a=rtpmap:99 H264/90000", "a=rtpmap:97 rtx/90000",
				"a=fmtp:97 apt=99", "a=rtpmap:98 red/90000", "a=rtpmap:100 ulpfec/90000", "a=rtpmap:101 flexfec/90000"),
			answer: sdpOf("m=video 6000 RTP/AVPF 99 97 98 100 101", "a=rtpmap:99 H264/90000", "a=rtpmap:97 rtx/90000",
				"a=fmtp:97 apt=99", "a=rtpmap:98 red/90000", "a=rtpmap:100 ulpfec/90000", "a=rtpmap:101 flexfec/90000"),
			want: []db.Codec{{Media: "video", Encoding: "H264", ClockRate: 90000}},
		},
		{
			name: "a bundle-only stream",
			offer: sdpOf("a=group:BUNDLE a v", "m=audio 5000 RTP/AVP 116", "a=mid:a", "a=rtpmap:116 AMR-WB/16000",
				"m=video 0 RTP/AVP 99", "a=mid:v", "a=bundle-only", "a=rtpmap:99 H264/90000"),
			answer: sdpOf("a=group:BUNDLE a v", "m=audio 6000 RTP/AVP 116", "a=mid:a", "a=rtpmap:116 AMR-WB/16000",
				"m=video 0 RTP/AVP 99", "a=mid:v", "a=bundle-only", "a=rtpmap:99 H264/90000"),
			want: []db.Codec{amrWB, {Media: "video", Encoding: "H264", ClockRate: 90000}},
		},
		{
			name:   "no SDP answer",
			offer:  volteOffer,
			answer: []byte("not SDP"),
		},
		{
			name:   "no SDP offer",
			answer: sdpOf("m=audio 6000 RTP/AVP 0"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, got := Negotiated(tt.offer, tt.answer); !slices.Equal(got, tt.want) {
				t.Fatalf("Negotiated codecs = %+v, want %+v", got, tt.want)
			}
		})
	}
}
