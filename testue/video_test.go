package testue

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
)

func videoPair(t *testing.T) (*UE, *UE) {
	t.Helper()

	return pairWith(t, Config{Video: true}, Config{Video: true})
}

// wantStreams checks the kinds of the call's streams and which are active, and that each active one has endpoints.
func wantStreams(t *testing.T, c *Call, want ...string) {
	t.Helper()

	got := c.Streams()

	var kinds []string

	for _, s := range got {
		k := s.Kind
		if !s.Active {
			k += ":0"
		} else if s.Local.Port == 0 || s.Remote.Port == 0 {
			t.Errorf("stream %d %+v, want both endpoints", s.Index, s)
		}

		kinds = append(kinds, k)
	}

	if strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Fatalf("streams %v, want %v", kinds, want)
	}
}

// Both ends see the same media on each stream: one's local endpoint is the other's remote one.
func wantMirrored(t *testing.T, a, b *Call) {
	t.Helper()

	as, bs := a.Streams(), b.Streams()
	for i := range as {
		if as[i].Active && (as[i].Local != bs[i].Remote || as[i].Remote != bs[i].Local) {
			t.Errorf("stream %d: %+v and %+v", i, as[i], bs[i])
		}
	}
}

func wantAttrs(t *testing.T, m *sdp.Media, attrs ...string) {
	t.Helper()

	for _, a := range attrs {
		name, value, _ := strings.Cut(a, ":")
		if !slices.Contains(m.Attrs(name), value) {
			t.Errorf("video without a=%s: %v", a, m.Attrs(name))
		}
	}
}

func videoMedia(t *testing.T, s *sdp.Session) *sdp.Media {
	t.Helper()

	for _, m := range s.Media {
		if m.Type() == sdp.Video {
			return m
		}
	}

	t.Fatalf("no video in\n%s", s)

	return nil
}

// IR.94 §2.2.1, §2.2.2, §3.3, NG.114 §3.3.2.1
func TestVideoCall(t *testing.T) {
	for _, preconditions := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "preconditions"}[preconditions], func(t *testing.T) {
			ctx := testContext(t)
			a, b := videoPair(t)

			ac, bc := connect(t, ctx, a, b, CallOptions{Video: true, Preconditions: preconditions})

			wantStreams(t, ac, sdp.Audio, sdp.Video)
			wantStreams(t, bc, sdp.Audio, sdp.Video)
			wantMirrored(t, ac, bc)

			if ac.PreconditionsMet() != true || bc.PreconditionsMet() != true {
				t.Error("preconditions not met")
			}

			audio, _ := ac.Stream(sdp.Audio)
			video, _ := ac.Stream(sdp.Video)

			if video.Local.Port != audio.Local.Port+2 {
				t.Errorf("video on %d, audio on %d: want video on the next RTP/RTCP pair", video.Local.Port, audio.Local.Port)
			}

			invite := bc.Invite()
			if ac := invite.Header.Get("Accept-Contact"); !strings.HasSuffix(ac, ";video") {
				t.Errorf("INVITE Accept-Contact = %q, want video", ac)
			}

			if c := invite.Header.Get("Contact"); !strings.Contains(c, ";video") {
				t.Errorf("INVITE Contact = %q, want video", c)
			}

			offer := videoMedia(t, sdpOf(t, invite.Body))

			desc, _ := offer.Desc()
			if desc.Proto != avpf || strings.Join(desc.Formats, " ") != "112 113 114" {
				t.Errorf("offered video %+v, want AVPF with H.265, then H.264 CHP and CBP", desc)
			}

			wantAttrs(t, offer, "rtcp-fb:* nack", "rtcp-fb:* nack pli", "rtcp-fb:* ccm fir", "rtcp-fb:* ccm tmmbr", "extmap:7 "+cvoURN)

			if as, ok := offer.Bandwidth("AS"); !ok || as != 560 {
				t.Errorf("offered video b=AS %d, want 560", as)
			}

			answer := videoMedia(t, bc.LocalSDP())
			if codecs, _ := answer.Codecs(); len(codecs) != 1 || codecs[0].Encoding != h265 {
				t.Errorf("answered video codecs %+v, want H.265", codecs)
			}
		})
	}
}

// IR.94 §2.2.2: a voice only UE declines video with port 0, and the call goes on as voice.
func TestVideoDeclined(t *testing.T) {
	ctx := testContext(t)
	a, b := pairWith(t, Config{Video: true}, Config{})

	ac, bc := connect(t, ctx, a, b, CallOptions{Video: true})

	wantStreams(t, ac, sdp.Audio, sdp.Video+":0")
	wantStreams(t, bc, sdp.Audio, sdp.Video+":0")

	if c := bc.Invite().Header.Get("Contact"); !strings.Contains(c, ";video") {
		t.Errorf("caller Contact = %q, want video", c)
	}

	// Later offers keep the declined stream at port 0 (RFC 3264 §8.2).
	if err := ac.Hold(ctx); err != nil {
		t.Fatal(err)
	}

	if s := ac.LocalSDP(); len(s.Media) != 2 || s.Media[1].Port() != 0 {
		t.Fatalf("offer after the decline:\n%s", s)
	}
}

// A voice only UE makes no video call and adds no video.
func TestVideoNotEnabled(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	if _, err := a.Invite("sip:+15550002@"+domain+";user=phone", CallOptions{Video: true}); !errors.Is(err, ErrCallState) {
		t.Fatalf("video Invite = %v, want ErrCallState", err)
	}

	ac, _ := connect(t, ctx, a, b, CallOptions{})

	if err := ac.AddVideo(ctx); !errors.Is(err, ErrCallState) {
		t.Fatalf("AddVideo = %v, want ErrCallState", err)
	}

	wantStreams(t, ac, sdp.Audio)
}

// IR.94 §2.2.2: video added to a voice call and removed again, by re-INVITE.
func TestAddAndRemoveVideo(t *testing.T) {
	for _, preconditions := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "preconditions"}[preconditions], func(t *testing.T) {
			ctx := testContext(t)
			a, b := videoPair(t)

			ac, bc := connect(t, ctx, a, b, CallOptions{Preconditions: preconditions})
			wantStreams(t, ac, sdp.Audio)

			if err := ac.AddVideo(ctx); err != nil {
				t.Fatalf("AddVideo: %v", err)
			}

			wantStreams(t, ac, sdp.Audio, sdp.Video)
			wantStreams(t, bc, sdp.Audio, sdp.Video)
			wantMirrored(t, ac, bc)

			// RFC 3312 §5: the stream added has its resources reported in an UPDATE once the re-INVITE completes.
			eventually(t, "preconditions met", func() bool { return ac.PreconditionsMet() && bc.PreconditionsMet() })

			if err := ac.AddVideo(ctx); !errors.Is(err, ErrCallState) {
				t.Errorf("second AddVideo = %v, want ErrCallState", err)
			}

			if err := bc.RemoveVideo(ctx); err != nil {
				t.Fatalf("RemoveVideo: %v", err)
			}

			wantStreams(t, ac, sdp.Audio, sdp.Video+":0")
			wantStreams(t, bc, sdp.Audio, sdp.Video+":0")

			if err := bc.RemoveVideo(ctx); !errors.Is(err, ErrNoStream) {
				t.Errorf("second RemoveVideo = %v, want ErrNoStream", err)
			}

			// RFC 3264 §8.3: video added again takes the m-line of the removed one.
			if err := ac.AddVideo(ctx); err != nil {
				t.Fatalf("AddVideo again: %v", err)
			}

			wantStreams(t, ac, sdp.Audio, sdp.Video)
			wantStreams(t, bc, sdp.Audio, sdp.Video)

			if ac.State() != CallConfirmed || bc.State() != CallConfirmed {
				t.Fatalf("states %s and %s", ac.State(), bc.State())
			}
		})
	}
}

// IR.94 §2.3.2: hold and resume cover every stream of the call in one offer.
func TestHoldVideoCall(t *testing.T) {
	ctx := testContext(t)
	a, b := videoPair(t)

	ac, bc := connect(t, ctx, a, b, CallOptions{Video: true})

	directions := func(c *Call) string {
		var out []string
		for _, s := range c.Streams() {
			out = append(out, string(s.Direction))
		}

		return strings.Join(out, " ")
	}

	if err := ac.Hold(ctx); err != nil {
		t.Fatal(err)
	}

	if got := directions(ac); got != "sendonly sendonly" {
		t.Errorf("holding: %s", got)
	}

	if got := directions(bc); got != "recvonly recvonly" {
		t.Errorf("held: %s", got)
	}

	if err := ac.Resume(ctx); err != nil {
		t.Fatal(err)
	}

	if got := directions(ac); got != "sendrecv sendrecv" {
		t.Errorf("resumed: %s", got)
	}
}

// TS 24.229 §6.1.1, IR.94 §2.4.1: a phone that loses its video bearer removes the video, and the call goes on.
func TestVideoBearerLost(t *testing.T) {
	ctx := testContext(t)
	a, b := videoPair(t)

	ac, bc := connect(t, ctx, a, b, CallOptions{Video: true, Preconditions: true})

	if err := bc.BearerLost(ctx, sdp.Video); err != nil {
		t.Fatalf("BearerLost: %v", err)
	}

	wantStreams(t, ac, sdp.Audio, sdp.Video+":0")
	wantStreams(t, bc, sdp.Audio, sdp.Video+":0")

	if methods := methods(ac); !strings.Contains(strings.Join(methods, ","), "INVITE") {
		t.Errorf("caller got %v, want the re-INVITE", methods)
	}

	// The video is already gone: nothing more to lose.
	if err := bc.BearerLost(ctx, sdp.Video); err != nil {
		t.Fatalf("second BearerLost: %v", err)
	}

	if ac.State() != CallConfirmed || bc.State() != CallConfirmed {
		t.Fatalf("states %s and %s", ac.State(), bc.State())
	}
}

// IR.94 §2.4.1, §3.3.2 Note 1: a video bearer refused during setup has the video removed in an UPDATE.
func TestVideoBearerRefusedDuringSetup(t *testing.T) {
	ctx := testContext(t)
	a, b := videoPair(t)

	ac, err := a.Invite("sip:+15550002@"+domain+";user=phone", CallOptions{Video: true, Preconditions: true})
	if err != nil {
		t.Fatal(err)
	}

	bc := incoming(t, b)

	if err := bc.Ring(ctx); err != nil {
		t.Fatalf("Ring: %v", err)
	}

	if err := ac.BearerLost(ctx, sdp.Video); err != nil {
		t.Fatalf("BearerLost: %v", err)
	}

	wantStreams(t, ac, sdp.Audio, sdp.Video+":0")

	done := make(chan error, 1)

	go func() { done <- bc.Answer(ctx) }()

	if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	if err := <-done; err != nil {
		t.Fatalf("Answer: %v", err)
	}

	wantStreams(t, bc, sdp.Audio, sdp.Video+":0")
}

func wantMediaLossReason(t *testing.T, c *Call) {
	t.Helper()

	for _, r := range c.Reasons() {
		if cause, _ := r.Cause(); r.Is(sip.ReasonReleaseCause) && cause == sip.ReleaseMediaBearerLoss {
			return
		}
	}

	t.Errorf("reasons %v, want RELEASE_CAUSE %d", c.Reasons(), sip.ReleaseMediaBearerLoss)
}

// TS 24.229 §6.1.1, §5.1.5: losing the voice bearer ends the call, with RELEASE_CAUSE 3.
func TestVoiceBearerLost(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		ctx := testContext(t)
		a, b := videoPair(t)

		ac, bc := connect(t, ctx, a, b, CallOptions{Video: true})

		if err := ac.BearerLost(ctx, sdp.Audio); err != nil {
			t.Fatalf("BearerLost: %v", err)
		}

		ended(t, ac, MediaLost)
		ended(t, bc, RemoteBye)
		wantMediaLossReason(t, bc)
	})

	// TS 24.229 §5.1.3.1: the INVITE is cancelled.
	t.Run("calling", func(t *testing.T) {
		ctx := testContext(t)
		a, b := pair(t)

		ac, err := a.Invite("sip:+15550002@"+domain+";user=phone", CallOptions{Preconditions: true})
		if err != nil {
			t.Fatal(err)
		}

		bc := incoming(t, b)

		if err := bc.Ring(ctx); err != nil {
			t.Fatalf("Ring: %v", err)
		}

		if err := ac.BearerLost(ctx, sdp.Audio); err != nil {
			t.Fatalf("BearerLost: %v", err)
		}

		ended(t, bc, Cancelled)
		wantMediaLossReason(t, bc)
	})

	// RFC 3312 §11: the callee rejects the INVITE with 580 (Precondition Failure).
	t.Run("called", func(t *testing.T) {
		ctx := testContext(t)
		a, b := pair(t)

		ac, err := a.Invite("sip:+15550002@"+domain+";user=phone", CallOptions{Preconditions: true})
		if err != nil {
			t.Fatal(err)
		}

		bc := incoming(t, b)

		if err := bc.BearerLost(ctx, sdp.Audio); err != nil {
			t.Fatalf("BearerLost: %v", err)
		}

		if res, err := ac.Wait(ctx); res == nil || res.StatusCode != 580 {
			t.Fatalf("Wait = %v, %v, want 580", res, err)
		}

		wantMediaLossReason(t, ac)
	})
}

func pixelVideoOffer(t *testing.T) *sdp.Session {
	t.Helper()

	b, err := os.ReadFile("../sip/internal/corpus/testdata/ella/live/5g/pixel-10a/call_to_crosscall-core-z5_video_callee_bye/001-INVITE.sip")
	if err != nil {
		t.Fatal(err)
	}

	_, body, ok := bytes.Cut(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte("\n\n"))
	if !ok {
		t.Fatal("INVITE without a body")
	}

	return sdpOf(t, bytes.ReplaceAll(body, []byte("\n"), []byte("\r\n")))
}

// A Pixel 10a's video offer: AVP with AVPF through SDP capability negotiation (RFC 5939, IR.94 §3.3.2), H.265 first.
func TestAnswerPixelVideoOffer(t *testing.T) {
	m := newMedia(loopback, 40000, true, true)

	answer, err := m.answer(pixelVideoOffer(t))
	if err != nil {
		t.Fatal(err)
	}

	video := videoMedia(t, answer)

	desc, _ := video.Desc()
	if desc.Proto != avpf || strings.Join(desc.Formats, " ") != "115" || desc.Port != 40002 {
		t.Fatalf("answered video %+v, want H.265 over AVPF on 40002", desc)
	}

	if fmtp, _ := video.Fmtp("115"); fmtp != "profile-id=1;level-id=93" {
		t.Errorf("answered fmtp %q", fmtp)
	}

	wantAttrs(t, video, "acfg:1 t=1", "extmap:7 "+cvoURN, "rtcp-fb:* ccm fir")

	// Voice only, the same offer has its video declined.
	answer, err = newMedia(loopback, 40000, true, false).answer(pixelVideoOffer(t))
	if err != nil {
		t.Fatal(err)
	}

	if videoMedia(t, answer).Port() != 0 {
		t.Fatalf("voice only answer:\n%s", answer)
	}
}

// IR.94 §2.2.1: a video capable UE registers with the video feature tag.
func TestVideoContact(t *testing.T) {
	a, _ := pairWith(t, Config{Video: true}, Config{})

	if c := a.contact(5060); !strings.Contains(c, ";audio;video") {
		t.Fatalf("Contact %q, want audio and video", c)
	}
}

// RFC 8285 §6: the answer keeps the offer's extmap ID for video orientation, and has none when the offer has none.
func TestVideoOrientationID(t *testing.T) {
	for _, c := range []struct{ extmap, want string }{
		{"a=extmap:3/sendrecv urn:3gpp:video-orientation\r\n", "3"},
		{"a=extmap:4 urn:ietf:params:rtp-hdrext:toffset\r\na=extmap:5\r\n", ""},
		{"", ""},
	} {
		offer := sdpOf(t, []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.2\r\ns=-\r\nc=IN IP4 127.0.0.2\r\nt=0 0\r\n"+
			"m=video 6000 RTP/AVPF 99\r\na=rtpmap:99 H264/90000\r\na=fmtp:99 packetization-mode=1\r\n"+c.extmap))

		if got := cvo(offer.Media[0]); got != c.want {
			t.Errorf("cvo(%q) = %q, want %q", c.extmap, got, c.want)
		}
	}
}
