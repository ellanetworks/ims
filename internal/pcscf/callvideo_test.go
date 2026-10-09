package pcscf

import (
	"testing"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/internal/policy"
	"github.com/ellanetworks/ims/sip"
)

func bearerRAR(t *testing.T, s *ipsecScene, session string, action rx.SpecificAction, components ...uint32) {
	t.Helper()

	r := rx.ReAuthRequest{SpecificActions: []rx.SpecificAction{action}}
	for _, n := range components {
		r.Flows = append(r.Flows, rx.Flows{MediaComponentNumber: n})
	}

	if !s.p.rxReAuth(session, r) {
		t.Fatal("ReAuth reported an unknown session")
	}
}

func wantBYE(t *testing.T, s *ipsecScene) {
	t.Helper()

	bye, _ := s.scscf.RecvRequest()
	if bye.Method != "BYE" {
		t.Fatalf("S-CSCF got %s, want the BYE", bye.Method)
	}

	wantReason503(t, bye)
}

// GSMA IR.94 §2.4.1, NG.114 §4.6.2: the call goes on as voice when only its video bearer is lost.
func TestCallVideoLostContinuesAsVoice(t *testing.T) {
	for _, action := range []rx.SpecificAction{
		rx.ActionIndicationOfReleaseOfBearer, rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfFailedResourcesAllocation,
	} {
		t.Run(action.String(), func(t *testing.T) {
			clk, opt := mediaLossClock()
			s, u, pcrf, _ := newRxIPsecScene(t, opt)
			e := s.establishVideo(t, u, pcrf, true)

			bearerRAR(t, s, e.session, action, 2)

			clk.Advance(lossTimeout)
			s.scscf.RecvNone(quiet)
			u.us.RecvNone(quiet)
		})
	}
}

// TS 24.229 §6.1.1: the UE removes the lost video, and the call goes on as voice.
func TestCallVideoLostThenRemoved(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishVideo(t, u, pcrf, true)

	bearerRAR(t, s, e.session, rx.ActionIndicationOfReleaseOfBearer, 2)

	update, err := e.ue.NewRequest("UPDATE")
	if err != nil {
		t.Fatal(err)
	}

	update.Header.Add("Contact", ueContact(u))
	update.SetBody("application/sdp", sdpBody(ueAddr.String(), "4000", "m=video 0 RTP/AVP 99", "a=rtpmap:99 H264/90000"))
	s.ueSend(u, update)

	fwd, ff := s.scscf.RecvRequest()
	if fwd.Method != "UPDATE" {
		t.Fatalf("S-CSCF got %s, want the UPDATE", fwd.Method)
	}

	ok := sip.NewResponse(fwd, 200, "")
	ok.SetBody("application/sdp", sdpBody("192.0.2.9", "5000", "m=video 0 RTP/AVP 99", "a=rtpmap:99 H264/90000"))
	s.scscf.Send(ff.Transport, ff.Remote, ok)

	_, aar := pcrf.aar()
	for _, c := range aar.MediaComponents {
		if c.Number != 2 {
			continue
		}

		if c.FlowStatus == nil || *c.FlowStatus != rx.FlowStatusRemoved {
			t.Fatalf("video Flow-Status %v, want REMOVED", c.FlowStatus)
		}
	}

	wantStatus(t, first(u.us.RecvResponse()), 200)

	clk.Advance(lossTimeout)
	s.scscf.RecvNone(quiet)
}

// NG.114 §4.6.2: losing the voice bearer ends the call, video or not.
func TestCallVideoCallAudioLost(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishVideo(t, u, pcrf, true)

	bearerRAR(t, s, e.session, rx.ActionIndicationOfReleaseOfBearer, 1)

	clk.Advance(lossTimeout)
	wantBYE(t, s)
	u.us.RecvNone(quiet)
}

// Video lost, then voice lost within the operator time: the call ends.
func TestCallVideoThenAudioLost(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishVideo(t, u, pcrf, true)

	bearerRAR(t, s, e.session, rx.ActionIndicationOfReleaseOfBearer, 2)
	bearerRAR(t, s, e.session, rx.ActionIndicationOfReleaseOfBearer, 1)

	clk.Advance(lossTimeout)
	wantBYE(t, s)
}

// Video lost and the call kept, then voice lost on its own: the call ends.
func TestCallVideoKeptThenAudioLost(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishVideo(t, u, pcrf, true)

	bearerRAR(t, s, e.session, rx.ActionIndicationOfReleaseOfBearer, 2)
	clk.Advance(lossTimeout)
	s.scscf.RecvNone(quiet)

	bearerRAR(t, s, e.session, rx.ActionIndicationOfReleaseOfBearer, 1)
	clk.Advance(lossTimeout)
	wantBYE(t, s)
}

// An indication naming no flow covers every media of the call.
func TestCallVideoCallAllLost(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishVideo(t, u, pcrf, true)

	bearerRAR(t, s, e.session, rx.ActionIndicationOfReleaseOfBearer)

	clk.Advance(lossTimeout)
	wantBYE(t, s)
}

// A refused video bearer during setup neither cancels nor fails the call.
func TestCallVideoRefusedDuringSetup(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)
	e := s.establishVideo(t, u, pcrf, false)

	bearerRAR(t, s, e.session, rx.ActionIndicationOfFailedResourcesAllocation, 2)

	clk.Advance(lossTimeout)
	s.scscf.RecvNone(quiet)
	u.us.RecvNone(quiet)
}

// A call left with only video has no voice to go on with: losing the video ends it.
func TestCallVideoOnlyLost(t *testing.T) {
	clk, opt := mediaLossClock()
	s, u, pcrf, _ := newRxIPsecScene(t, opt)

	video := []string{"m=video 4002 RTP/AVP 99", "a=rtpmap:99 H264/90000"}
	answer := []string{"m=video 5002 RTP/AVP 99", "a=rtpmap:99 H264/90000"}
	e := s.establish(t, u, pcrf, true, sdpBody(ueAddr.String(), "0", video...), sdpBody("192.0.2.9", "0", answer...))

	bearerRAR(t, s, e.session, rx.ActionIndicationOfReleaseOfBearer, 2)

	clk.Advance(lossTimeout)
	wantBYE(t, s)
}

func TestLossReleases(t *testing.T) {
	av := map[uint32]policy.MediaType{1: policy.MediaAudio, 2: policy.MediaVideo}

	for _, c := range []struct {
		name           string
		active         map[uint32]policy.MediaType
		lost           []uint32
		release, video bool
	}{
		{"video", av, []uint32{2}, false, true},
		{"audio", av, []uint32{1}, true, false},
		{"both", av, []uint32{2, 1}, true, false},
		{"unnamed", av, nil, true, false},
		{"unnamed, nothing active", map[uint32]policy.MediaType{}, nil, false, false},
		{"removed video", map[uint32]policy.MediaType{1: policy.MediaAudio}, []uint32{2}, false, false},
		{"video only", map[uint32]policy.MediaType{2: policy.MediaVideo}, []uint32{2}, true, false},
		{"one of two videos", map[uint32]policy.MediaType{1: policy.MediaVideo, 2: policy.MediaVideo}, []uint32{2}, true, false},
		{"text", map[uint32]policy.MediaType{1: policy.MediaAudio, 3: policy.MediaText}, []uint32{3}, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			release, video := lossReleases(c.active, c.lost)
			if release != c.release || video != c.video {
				t.Errorf("lossReleases = %v, %v, want %v, %v", release, video, c.release, c.video)
			}
		})
	}
}
