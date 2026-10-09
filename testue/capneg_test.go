package testue

import (
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip/sdp"
)

func capNegOffer(t *testing.T, session string, video ...string) *sdp.Session {
	t.Helper()

	return sdpOf(t, []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.2\r\ns=-\r\nc=IN IP4 127.0.0.2\r\nt=0 0\r\n"+session+
		"m=audio 5000 RTP/AVP 116\r\na=rtpmap:116 AMR-WB/16000/1\r\n"+strings.Join(video, "\r\n")+"\r\n"))
}

const h264CBP = "a=rtpmap:99 H264/90000\r\na=fmtp:99 profile-level-id=42e01f;packetization-mode=1"

// RFC 5939 §3.5.2, §3.6.2, TS 26.114 §6.2.1a.3
func TestCapabilityNegotiation(t *testing.T) {
	for _, c := range []struct {
		name    string
		session string
		video   []string
		// proto and acfg of the answered video, "" for none; formats its payload types, "" when declined.
		proto, acfg, formats string
		// feedback are the answered rtcp-fb values; csup the answer's csup at the session or media level.
		feedback    string
		sessionCSup bool
		mediaCSup   bool
	}{
		{
			name:  "AVPF as a potential configuration",
			video: []string{"m=video 6000 RTP/AVP 99", h264CBP, "a=rtcp-fb:* nack", "a=tcap:1 RTP/AVPF", "a=pcfg:1 t=1"},
			proto: avpf, acfg: "1 t=1", formats: "99", feedback: "* nack",
		},
		{
			name: "lowest configuration number first",
			video: []string{
				"m=video 6000 RTP/AVP 99", h264CBP, "a=tcap:1 RTP/AVPF RTP/AVP",
				"a=pcfg:5 t=1", "a=pcfg:3 t=2",
			},
			proto: avp, acfg: "3 t=2", formats: "99",
		},
		{
			name: "transport alternatives in order, unsupported ones skipped",
			video: []string{
				"m=video 6000 RTP/AVP 99", h264CBP, "a=tcap:1 RTP/SAVPF RTP/AVPF",
				"a=pcfg:1 t=1|2",
			},
			proto: avpf, acfg: "1 t=2", formats: "99",
		},
		{
			name: "attribute capabilities: mandatory, then the known optional ones",
			video: []string{
				"m=video 6000 RTP/AVP 99", h264CBP, "a=tcap:1 RTP/AVPF",
				"a=acap:1 rtcp-fb:* nack pli", "a=acap:2 x-unknown:1", "a=acap:3 rtcp-fb:* ccm fir",
				"a=pcfg:1 t=1 a=1,[2,3]",
			},
			proto: avpf, acfg: "1 t=1 a=1,[3]", formats: "99", feedback: "* nack pli,* ccm fir",
		},
		{
			name: "unknown mandatory attribute capability",
			video: []string{
				"m=video 6000 RTP/AVP 99", h264CBP, "a=tcap:1 RTP/AVPF",
				"a=acap:1 x-unknown:1", "a=pcfg:1 t=1 a=1", "a=pcfg:2 t=1",
			},
			proto: avpf, acfg: "2 t=1", formats: "99",
		},
		{
			name: "attribute alternatives in order",
			video: []string{
				"m=video 6000 RTP/AVP 99", h264CBP, "a=tcap:1 RTP/AVPF",
				"a=acap:1 x-unknown:1", "a=acap:2 rtcp-fb:* nack", "a=pcfg:1 t=1 a=1|2",
			},
			proto: avpf, acfg: "1 t=1 a=2", formats: "99", feedback: "* nack",
		},
		{
			// RFC 5939 §3.13.1: only the formats of the m= line are offered; a capability remapping one deletes the
			// media attributes.
			name: "media attributes deleted, the format a capability",
			video: []string{
				"m=video 6000 RTP/AVP 99", "a=rtpmap:99 VP8/90000", "a=tcap:1 RTP/AVPF",
				"a=acap:1 rtpmap:99 H264/90000", "a=acap:2 fmtp:99 profile-level-id=42e01f;packetization-mode=1",
				"a=pcfg:1 t=1 a=-m:1,2",
			},
			proto: avpf, acfg: "1 t=1 a=-m:1,2", formats: "99",
		},
		{
			name: "a mandatory format the UE does not take",
			video: []string{
				"m=video 6000 RTP/AVP 99 98", h264CBP, "a=rtpmap:98 VP8/90000", "a=tcap:1 RTP/AVPF",
				"a=acap:1 rtpmap:98 VP8/90000", "a=pcfg:1 t=1 a=1", "a=pcfg:2 t=1",
			},
			proto: avpf, acfg: "2 t=1", formats: "99",
		},
		{
			name:  "a capability another media description has",
			video: []string{"m=video 6000 RTP/AVP 99", h264CBP, "a=pcfg:1 t=1"},
			proto: avp, formats: "99",
		},
		{
			name:    "a session-level transport capability",
			session: "a=tcap:1 RTP/AVPF\r\n",
			video:   []string{"m=video 6000 RTP/AVP 99", h264CBP, "a=pcfg:1 t=1"},
			proto:   avpf, acfg: "1 t=1", formats: "99",
		},
		{
			name:    "a session-level capability of a media-level attribute",
			session: "a=acap:1 rtcp-fb:* nack\r\n",
			video:   []string{"m=video 6000 RTP/AVP 99", h264CBP, "a=tcap:1 RTP/AVPF", "a=pcfg:1 t=1 a=1", "a=pcfg:2 t=1"},
			proto:   avpf, acfg: "2 t=1", formats: "99",
		},
		{
			name: "duplicate configuration numbers",
			video: []string{
				"m=video 6000 RTP/AVP 99", h264CBP, "a=tcap:1 RTP/AVPF RTP/AVP",
				"a=pcfg:1 t=1", "a=pcfg:1 t=2", "a=pcfg:2 t=1",
			},
			proto: avpf, acfg: "2 t=1", formats: "99",
		},
		{
			name: "extensions: unknown ignored, unknown mandatory not supported",
			video: []string{
				"m=video 6000 RTP/AVP 99", h264CBP, "a=tcap:1 RTP/AVPF RTP/AVP",
				"a=pcfg:1 t=1 +x=1", "a=pcfg:2 t=2 x=1",
			},
			proto: avp, acfg: "2 t=2", formats: "99",
		},
		{
			name:    "a session-level required extension the UE lacks",
			session: "a=creq:x-ext\r\n",
			video:   []string{"m=video 6000 RTP/AVP 99", h264CBP, "a=tcap:1 RTP/AVPF", "a=pcfg:1 t=1"},
			proto:   avp, formats: "99",
			sessionCSup: true,
		},
		{
			name:  "a media-level required extension the UE lacks",
			video: []string{"m=video 6000 RTP/AVP 99", h264CBP, "a=creq:cap-v0,x-ext", "a=tcap:1 RTP/AVPF", "a=pcfg:1 t=1"},
			proto: avp, formats: "99",
			mediaCSup: true,
		},
		{
			name:  "the base required",
			video: []string{"m=video 6000 RTP/AVP 99", h264CBP, "a=creq:cap-v0", "a=tcap:1 RTP/AVPF", "a=pcfg:1 t=1"},
			proto: avpf, acfg: "1 t=1", formats: "99",
		},
		{
			name:  "an unsupported transport",
			video: []string{"m=video 6000 RTP/SAVP 99", h264CBP},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			answer, err := newMedia(loopback, 40000, false, true).answer(capNegOffer(t, c.session, c.video...))
			if err != nil {
				t.Fatal(err)
			}

			video := answer.Media[1]
			desc, _ := video.Desc()

			if c.formats == "" {
				if video.Port() != 0 {
					t.Fatalf("answered video %+v, want it declined", desc)
				}

				return
			}

			if video.Port() == 0 || desc.Proto != c.proto || strings.Join(desc.Formats, " ") != c.formats {
				t.Fatalf("answered video %+v, want %s %s", desc, c.proto, c.formats)
			}

			if got, _ := video.Attr("acfg"); got != c.acfg {
				t.Errorf("acfg %q, want %q", got, c.acfg)
			}

			if got := strings.Join(video.Attrs("rtcp-fb"), ","); got != c.feedback {
				t.Errorf("rtcp-fb %q, want %q", got, c.feedback)
			}

			if got, _ := answer.Attr("csup"); (got == capNegBase) != c.sessionCSup {
				t.Errorf("session csup %q", got)
			}

			if got, _ := video.Attr("csup"); (got == capNegBase) != c.mediaCSup {
				t.Errorf("media csup %q", got)
			}

			for _, a := range capNegAttrs[1:] {
				if a != "acfg" && (answer.HasAttr(a) || video.HasAttr(a)) {
					t.Errorf("answer with a=%s", a)
				}
			}
		})
	}
}
