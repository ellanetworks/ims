package testue

import (
	"bytes"
	"errors"
	"fmt"
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

			eventually(t, "preconditions met", func() bool { return ac.PreconditionsMet() && bc.PreconditionsMet() })

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

			wantAttrs(t, offer, "rtcp-fb:* trr-int 5000", "rtcp-fb:* nack", "rtcp-fb:* nack pli", "rtcp-fb:* ccm fir",
				"rtcp-fb:* ccm tmmbr", "extmap:7 "+cvoURN)

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

		methods(bc)

		if err := ac.BearerLost(ctx, sdp.Audio); err != nil {
			t.Fatalf("BearerLost: %v", err)
		}

		ended(t, ac, MediaLost)
		ended(t, bc, Cancelled)
		wantMediaLossReason(t, bc)

		// RFC 3312 §8, as the Crosscall Core-Z5 does (call_precondition_failure_580/014-CANCEL).
		for e := range bc.Events() {
			if e.Request != nil && e.Request.Method == "CANCEL" {
				wantPreconditionFailure(t, e.Request.Body)
				break
			}
		}
	})

	// RFC 3312 §8: the callee rejects the INVITE with 580 (Precondition Failure), describing the failure, or 488
	// without preconditions, with the Reason, as the Crosscall Core-Z5 does (call_precondition_failure_580/011-580).
	for _, c := range []struct {
		preconditions bool
		code          int
	}{{true, 580}, {false, 488}} {
		t.Run(fmt.Sprintf("called %d", c.code), func(t *testing.T) {
			ctx := testContext(t)
			a, b := pair(t)

			ac, err := a.Invite("sip:+15550002@"+domain+";user=phone", CallOptions{Preconditions: c.preconditions})
			if err != nil {
				t.Fatal(err)
			}

			bc := incoming(t, b)

			if err := bc.BearerLost(ctx, sdp.Audio); err != nil {
				t.Fatalf("BearerLost: %v", err)
			}

			res, err := ac.Wait(ctx)
			if res == nil || res.StatusCode != c.code {
				t.Fatalf("Wait = %v, %v, want %d", res, err, c.code)
			}

			if r := res.Header.Get("Reason"); !strings.Contains(r, "RELEASE_CAUSE") || !strings.Contains(r, "cause=3") {
				t.Errorf("Reason %q, want RELEASE_CAUSE;cause=3", r)
			}

			if c.preconditions {
				wantPreconditionFailure(t, res.Body)
			} else if len(res.Body) != 0 {
				t.Errorf("488 with a body:\n%s", res.Body)
			}

			ended(t, bc, MediaLost)
			ended(t, ac, Rejected)
		})
	}
}

// wantPreconditionFailure checks the SDP of a precondition failure (RFC 3312 §8): every m-line at port 0, the local QoS
// desired failing.
func wantPreconditionFailure(t *testing.T, body []byte) {
	t.Helper()

	s := sdpOf(t, body)
	if len(s.Media) == 0 {
		t.Fatalf("failure SDP without m-lines:\n%s", body)
	}

	for _, m := range s.Media {
		if m.Port() != 0 || !slices.Contains(m.Attrs("des"), "qos failure local sendrecv") {
			t.Errorf("failure SDP:\n%s\nwant every m-line at port 0 with des:qos failure local", body)
		}
	}
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

// RFC 8285 §6: the answer keeps the offer's extmap ID for video orientation, mirroring its direction, and has none
// when the offer has none.
func TestVideoOrientationID(t *testing.T) {
	for _, c := range []struct{ extmap, want string }{
		{"a=extmap:3 urn:3gpp:video-orientation\r\n", "3"},
		{"a=extmap:3/sendrecv urn:3gpp:video-orientation\r\n", "3/sendrecv"},
		// RFC 8285 §6, TS 26.114 §6.2.3.3: CVO answered only in the directions offered.
		{"a=extmap:5/sendonly urn:3gpp:video-orientation\r\n", "5/recvonly"},
		{"a=extmap:6/recvonly urn:3gpp:video-orientation\r\n", "6/sendonly"},
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

// peerResponse is the response of the scripted peer to req, in its dialog.
func peerResponse(t *testing.T, p *peer, req *sip.Request, code int, tag string, body *sdp.Session) *sip.Response {
	t.Helper()

	res := sip.NewResponse(req, code, "")
	setToTag(t, &res.Header, tag)
	res.Header.Set("Contact", "<sip:peer@"+p.s.Addr().String()+">")

	if body != nil {
		res.SetBody(sdp.ContentType, body.Bytes())
	}

	return res
}

// TS 24.229 §6.1.4.2, RFC 3312 §5, RFC 3262 §5: a phone answers a re-INVITE adding video with preconditions in a
// reliable 183, and holds its 200, without SDP, until the UPDATE reports the resources.
func TestAddVideoAnsweredInReliable183(t *testing.T) {
	ctx := testContext(t)
	p := newPeer(t, Config{Video: true})
	pm := newMedia(loopback, 50000, true, true)
	tag := sip.NewTag()

	c, err := p.u.Invite("sip:+15550002@"+domain+";user=phone", CallOptions{Preconditions: true})
	if err != nil {
		t.Fatal(err)
	}

	invite := p.request("INVITE")

	answer, err := pm.answer(sdpOf(t, invite.Body))
	if err != nil {
		t.Fatal(err)
	}

	p.send(peerResponse(t, p, invite, 200, tag, answer))

	if res, err := c.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	p.request("ACK")

	added := make(chan error, 1)

	go func() { added <- c.AddVideo(ctx) }()

	reinvite := p.request("INVITE")

	answer, err = pm.answer(sdpOf(t, reinvite.Body))
	if err != nil {
		t.Fatal(err)
	}

	progress := peerResponse(t, p, reinvite, 183, tag, answer)
	progress.Header.Set("Require", "100rel, precondition")
	progress.Header.Set("RSeq", "1")
	p.send(progress)

	prack := p.request("PRACK")
	p.send(peerResponse(t, p, prack, 200, tag, nil))

	update := p.request("UPDATE")

	offer := sdpOf(t, update.Body)
	assertQoS(t, "UPDATE", videoMedia(t, offer), "curr:qos local sendrecv")

	answer, err = pm.answer(offer)
	if err != nil {
		t.Fatal(err)
	}

	p.send(peerResponse(t, p, update, 200, tag, answer))
	p.send(peerResponse(t, p, reinvite, 200, tag, nil))
	p.request("ACK")

	if err := <-added; err != nil {
		t.Fatalf("AddVideo: %v", err)
	}

	if _, ok := c.Stream(sdp.Video); !ok || !c.PreconditionsMet() {
		t.Fatalf("streams %+v, preconditions met %v: want the video with its resources", c.Streams(), c.PreconditionsMet())
	}
}

// RFC 3261 §14.1: a re-INVITE refused after a reliable 183 answered it leaves the session as it was.
func TestAddVideoRefusedAfterReliable183(t *testing.T) {
	ctx := testContext(t)
	p := newPeer(t, Config{Video: true})
	pm := newMedia(loopback, 50000, false, true)
	tag := sip.NewTag()

	c, err := p.u.Invite("sip:+15550002@"+domain+";user=phone", CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	invite := p.request("INVITE")

	answer, err := pm.answer(sdpOf(t, invite.Body))
	if err != nil {
		t.Fatal(err)
	}

	p.send(peerResponse(t, p, invite, 200, tag, answer))

	if res, err := c.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	p.request("ACK")

	added := make(chan error, 1)

	go func() { added <- c.AddVideo(ctx) }()

	reinvite := p.request("INVITE")

	answer, err = pm.answer(sdpOf(t, reinvite.Body))
	if err != nil {
		t.Fatal(err)
	}

	progress := peerResponse(t, p, reinvite, 183, tag, answer)
	progress.Header.Set("Require", "100rel")
	progress.Header.Set("RSeq", "1")
	p.send(progress)

	prack := p.request("PRACK")
	p.send(peerResponse(t, p, prack, 200, tag, nil))
	p.send(peerResponse(t, p, reinvite, 488, tag, nil))
	p.request("ACK")

	var rerr *ResponseError
	if err := <-added; !errors.As(err, &rerr) || rerr.Response.StatusCode != 488 {
		t.Fatalf("AddVideo = %v, want the 488", err)
	}

	if s := c.Streams(); len(s) != 1 || s[0].Kind != sdp.Audio || !s[0].Active {
		t.Fatalf("streams %+v, want the audio only", s)
	}

	if remote := c.RemoteSDP(); len(remote.Media) != 1 {
		t.Fatalf("remote SDP:\n%s\nwant the answer of the call", remote)
	}
}

// RFC 3261 §9.1, TS 24.229 §5.1.5: a 2xx crossing the CANCEL of a call whose voice bearer was lost has the call
// ended by a BYE with the same RELEASE_CAUSE.
func TestVoiceBearerLostCrossing2xx(t *testing.T) {
	ctx := testContext(t)
	p := newPeer(t, Config{})
	pm := newMedia(loopback, 50000, false, false)
	tag := sip.NewTag()

	c, err := p.u.Invite("sip:+15550002@"+domain+";user=phone", CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	invite := p.request("INVITE")
	p.send(peerResponse(t, p, invite, 180, tag, nil))
	eventually(t, "the early dialog", func() bool { return c.State() == CallEarly })

	lost := make(chan error, 1)

	go func() { lost <- c.BearerLost(ctx, sdp.Audio) }()

	cancel := p.request("CANCEL")
	if !strings.Contains(cancel.Header.Get("Reason"), "RELEASE_CAUSE") {
		t.Errorf("CANCEL Reason %q, want RELEASE_CAUSE", cancel.Header.Get("Reason"))
	}

	answer, err := pm.answer(sdpOf(t, invite.Body))
	if err != nil {
		t.Fatal(err)
	}

	p.send(peerResponse(t, p, invite, 200, tag, answer))
	p.send(sip.NewResponse(cancel, 200, ""))
	p.request("ACK")

	bye := p.request("BYE")
	if !strings.Contains(bye.Header.Get("Reason"), "RELEASE_CAUSE") || !strings.Contains(bye.Header.Get("Reason"), "cause=3") {
		t.Errorf("BYE Reason %q, want RELEASE_CAUSE;cause=3", bye.Header.Get("Reason"))
	}

	p.send(sip.NewResponse(bye, 200, ""))

	if err := <-lost; err != nil {
		t.Fatalf("BearerLost: %v", err)
	}

	ended(t, c, MediaLost)
}

// RFC 3261 §14.2, RFC 3264 §8: an offer the UE cannot accept leaves the session as it was.
func TestRefusedOfferKeepsTheStreams(t *testing.T) {
	m := newMedia(loopback, 40000, false, true)

	const head = "v=0\r\no=- 1 1 IN IP4 127.0.0.2\r\ns=-\r\nc=IN IP4 127.0.0.2\r\nt=0 0\r\n"

	if _, err := m.answer(sdpOf(t, []byte(head+
		"m=audio 5000 RTP/AVP 116\r\na=rtpmap:116 AMR-WB/16000/1\r\n"+
		"m=video 5002 RTP/AVPF 99\r\na=rtpmap:99 H264/90000\r\na=fmtp:99 profile-level-id=42e01f;packetization-mode=1\r\n"+
		"a=extmap:7 urn:3gpp:video-orientation\r\n"))); err != nil {
		t.Fatal(err)
	}

	// Video over AVP without CVO, and audio the UE has no codec for.
	if _, err := m.answer(sdpOf(t, []byte(head+
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"+
		"m=video 5002 RTP/AVP 98\r\na=rtpmap:98 H265/90000\r\n"))); !errors.Is(err, ErrNoCodec) {
		t.Fatalf("answer = %v, want ErrNoCodec", err)
	}

	next, err := m.offer()
	if err != nil {
		t.Fatal(err)
	}

	video := videoMedia(t, next)

	if desc, _ := video.Desc(); desc.Proto != avpf || strings.Join(desc.Formats, " ") != "99" {
		t.Fatalf("video after the refused offer %+v, want H.264 over AVPF", desc)
	}

	wantAttrs(t, video, "extmap:7 "+cvoURN)

	if codecs, _ := audio(t, next).Codecs(); len(codecs) == 0 || codecs[0].Encoding != "AMR-WB" {
		t.Fatalf("audio after the refused offer %+v", codecs)
	}
}

// RFC 3264 §6: an answer has as many m-lines as the offer.
func TestShortAnswerRefused(t *testing.T) {
	m := newMedia(loopback, 40000, false, true)

	if err := m.addVideo(); err != nil {
		t.Fatal(err)
	}

	if _, err := m.offer(); err != nil {
		t.Fatal(err)
	}

	answer := sdpOf(t, []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.2\r\ns=-\r\nc=IN IP4 127.0.0.2\r\nt=0 0\r\n"+
		"m=audio 5000 RTP/AVP 116\r\na=rtpmap:116 AMR-WB/16000/1\r\n"))

	if err := m.answered(answer); err == nil {
		t.Fatal("answered a one m-line answer to a two m-line offer")
	}

	if m.remote != nil || len(m.active()) != 2 {
		t.Fatalf("remote %v, %d active streams: want the session left as offered", m.remote, len(m.active()))
	}
}

func videoOffer(t *testing.T, lines ...string) *sdp.Session {
	t.Helper()

	return sdpOf(t, []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.2\r\ns=-\r\nc=IN IP4 127.0.0.2\r\nt=0 0\r\n"+
		"m=audio 5000 RTP/AVP 116\r\na=rtpmap:116 AMR-WB/16000/1\r\n"+strings.Join(lines, "\r\n")+"\r\n"))
}

// RFC 4585 §4.2, TS 26.114 §6.2.3.2: the answer has the feedback the offer has and the UE takes, for the chosen
// payload type or all of them, with the offered values.
func TestAnswerFeedback(t *testing.T) {
	answer, err := newMedia(loopback, 40000, false, true).answer(videoOffer(t,
		"m=video 5002 RTP/AVPF 99 100",
		"a=rtpmap:99 H264/90000", "a=fmtp:99 profile-level-id=42e01f;packetization-mode=1",
		"a=rtpmap:100 H264/90000", "a=fmtp:100 profile-level-id=640c1f;packetization-mode=1",
		"a=rtcp-fb:* trr-int 1000", "a=rtcp-fb:99 nack", "a=rtcp-fb:100 ccm fir", "a=rtcp-fb:* goog-remb",
		"a=rtcp-fb:*  nack   pli"))
	if err != nil {
		t.Fatal(err)
	}

	if got := videoMedia(t, answer).Attrs("rtcp-fb"); strings.Join(got, ",") != "* trr-int 1000,* nack,* nack pli" {
		t.Fatalf("answered rtcp-fb %q", got)
	}

	// Over AVP, there is no feedback (RFC 4585 §4).
	answer, err = newMedia(loopback, 40000, false, true).answer(videoOffer(t,
		"m=video 5002 RTP/AVP 99", "a=rtpmap:99 H264/90000", "a=fmtp:99 profile-level-id=42e01f;packetization-mode=1",
		"a=rtcp-fb:* nack"))
	if err != nil {
		t.Fatal(err)
	}

	if got := videoMedia(t, answer).Attrs("rtcp-fb"); len(got) != 0 {
		t.Fatalf("rtcp-fb %q over AVP", got)
	}
}

// IR.94 §3.3.1, NG.114 §3.3.2.1, RFC 6184 §8.1, RFC 7798 §7.2.2, ITU-T H.264 A.2.1.1, A.2.4.2: the UE takes H.265
// Main and H.264 Constrained Baseline or Constrained High, and answers H.265 with the offer's symmetric parameters.
func TestChooseVideo(t *testing.T) {
	const h265Symmetric = "profile-space=0;tier-flag=0;profile-id=1;level-id=120;" +
		"profile-compatibility-indicator=60000000;interop-constraints=B00000000000"

	for _, c := range []struct {
		name, rtpmap, fmtp, want string
	}{
		{"H.265 Main", "H265/90000", "profile-id=1;level-id=93;sprop-vps=QAE", "profile-id=1;level-id=93"},
		{"H.265 defaults", "H265/90000", "", ""},
		{"H.265 symmetric", "H265/90000", h265Symmetric, h265Symmetric},
		{"H.265 Main 10", "H265/90000", "profile-id=2", "-"},
		{"H.264 CBP", "H264/90000", "profile-level-id=42e01f;packetization-mode=1", "profile-level-id=42e01f;packetization-mode=1"},
		{"H.264 CBP of a Pixel", "H264/90000", "profile-level-id=42C00C;packetization-mode=1", "profile-level-id=42C00C;packetization-mode=1"},
		{"H.264 CHP", "H264/90000", "profile-level-id=640c1f;packetization-mode=1", "profile-level-id=640c1f;packetization-mode=1"},
		{"H.264 Baseline", "H264/90000", "profile-level-id=42001f;packetization-mode=1", "-"},
		{"H.264 High", "H264/90000", "profile-level-id=64001f;packetization-mode=1", "-"},
		{"H.264 without profile-level-id", "H264/90000", "packetization-mode=1", "-"},
		{"H.264 single NAL", "H264/90000", "profile-level-id=42e01f", "-"},
		{"VP8", "VP8/90000", "", "-"},
	} {
		t.Run(c.name, func(t *testing.T) {
			lines := []string{"m=video 5002 RTP/AVPF 99", "a=rtpmap:99 " + c.rtpmap}
			if c.fmtp != "" {
				lines = append(lines, "a=fmtp:99 "+c.fmtp)
			}

			got := chooseVideo(videoOffer(t, lines...).Media[1])

			switch {
			case c.want == "-" && got != nil:
				t.Fatalf("took %+v", got)
			case c.want != "-" && (len(got) != 1 || got[0].fmtp != c.want):
				t.Fatalf("got %+v, want fmtp %q", got, c.want)
			}
		})
	}
}

// TS 24.229 §5.1.3.1, §5.1.5: the user ending a call says so, as phones do.
func TestUserEndsCallReason(t *testing.T) {
	wantUserEnds := func(t *testing.T, c *Call) {
		t.Helper()

		for _, r := range c.Reasons() {
			if cause, _ := r.Cause(); r.Is(sip.ReasonReleaseCause) && cause == sip.ReleaseUserEndsCall {
				return
			}
		}

		t.Errorf("reasons %v, want RELEASE_CAUSE %d", c.Reasons(), sip.ReleaseUserEndsCall)
	}

	t.Run("BYE", func(t *testing.T) {
		ctx := testContext(t)
		a, b := pair(t)

		ac, bc := connect(t, ctx, a, b, CallOptions{})

		if err := ac.Bye(ctx); err != nil {
			t.Fatal(err)
		}

		ended(t, bc, RemoteBye)
		wantUserEnds(t, bc)
	})

	t.Run("CANCEL", func(t *testing.T) {
		ctx := testContext(t)
		a, b := pair(t)

		ac, err := a.Invite("sip:+15550002@"+domain+";user=phone", CallOptions{})
		if err != nil {
			t.Fatal(err)
		}

		bc := incoming(t, b)

		if err := bc.Ring(ctx); err != nil {
			t.Fatal(err)
		}

		if err := ac.Cancel(ctx); err != nil {
			t.Fatal(err)
		}

		ended(t, bc, Cancelled)
		wantUserEnds(t, bc)
	})
}
