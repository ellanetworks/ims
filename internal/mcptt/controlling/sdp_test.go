package controlling

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/mcptt/controlchannel"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/mcptt/media/mediatest"
	"github.com/ellanetworks/ims/sip/sdp"
)

type goScheduler struct{}

func (goScheduler) Go(f func()) { go f() }

// leg is a leg of a relay on addr.
func leg(t *testing.T, addr string) *media.Leg {
	t.Helper()

	r, err := media.New(media.Config{
		Network: mediatest.New(goScheduler{}), Addr: netip.MustParseAddr(addr), MinPort: 20000, MaxPort: 20099,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = r.Close() })

	l, err := r.NewCall().AddLeg(media.LegConfig{SharedControl: true})
	if err != nil {
		t.Fatal(err)
	}

	return l
}

func parse(t *testing.T, audio string) *sdp.Session {
	t.Helper()

	s, err := sdp.Parse([]byte("v=0\r\no=- 1 1 IN IP4 192.0.2.10\r\ns=-\r\nc=IN IP4 192.0.2.10\r\nt=0 0\r\n" + audio +
		"m=application 4002 udp MCPTT\r\na=fmtp:MCPTT mc_implicit_request;mc_floor_ssrc=7\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	return s
}

// originator is a call's speech: AMR-WB of payload type 96, bandwidth-efficient, of a mode-set, recvonly, without
// b=AS.
func originator(t *testing.T) speech {
	t.Helper()

	sp, _, _, err := chooseSpeech(parse(t, "m=audio 4000 RTP/AVPF 96\r\na=rtpmap:96 AMR-WB/16000/1\r\n"+
		"a=fmtp:96 mode-set=0,1,2;mode-change-capability=2\r\na=ptime:20\r\na=recvonly\r\n"), []string{"AMR-WB", "AMR"})
	if err != nil {
		t.Fatal(err)
	}

	return sp
}

// RFC 3264 §6.1, RFC 4867 §8.3.1: a joiner answered from its own offer: its payload type, profile and direction; its
// mode-set returned as offered; refused for another codec, configuration or mode-set.
func TestJoiningAnswer(t *testing.T) {
	sp := originator(t)
	l := leg(t, "192.0.2.1")

	for _, c := range []struct {
		name, audio string
		want        []string
	}{
		{
			"payload type", "m=audio 4000 RTP/AVP 118\r\na=rtpmap:118 AMR-WB/16000\r\n",
			[]string{
				"m=audio 20000 RTP/AVP 118\r\n", "a=rtpmap:118 AMR-WB/16000/1\r\n",
				"a=fmtp:118 mode-set=0,1,2;mode-change-capability=2\r\n", "b=AS:30\r\n", "a=ptime:20\r\n",
			},
		},
		{
			"profile, direction of the session level, mode-set", "a=recvonly\r\nm=audio 4000 RTP/AVPF 97 96\r\n" +
				"a=rtpmap:97 AMR-WB/16000\r\na=fmtp:97 octet-align=1\r\na=rtpmap:96 AMR-WB/16000\r\na=fmtp:96 mode-set=2,1,0\r\n",
			[]string{"m=audio 20000 RTP/AVPF 96\r\n", "a=fmtp:96 mode-set=2,1,0;mode-change-capability=2\r\n", "a=sendonly\r\n"},
		},
		{"octet-aligned", "m=audio 4000 RTP/AVP 96\r\na=rtpmap:96 AMR-WB/16000\r\na=fmtp:96 octet-align=1\r\n", nil},
		{"AMR", "m=audio 4000 RTP/AVP 96\r\na=rtpmap:96 AMR/8000\r\n", nil},
		{"another mode-set", "m=audio 4000 RTP/AVP 96\r\na=rtpmap:96 AMR-WB/16000\r\na=fmtp:96 mode-set=0,1\r\n", nil},
		{"RTP/SAVP", "m=audio 4000 RTP/SAVP 96\r\na=rtpmap:96 AMR-WB/16000\r\n", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			offer := parse(t, c.audio)

			st, ai, ci, err := sp.joining(offer)
			if c.want == nil {
				if err == nil {
					t.Fatalf("accepted: %+v", st)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			b, err := sp.answer(offer, st, ai, ci, l, 1, false, controlchannel.Params{})
			if err != nil {
				t.Fatal(err)
			}

			for _, w := range c.want {
				if !strings.Contains(string(b), w) {
					t.Errorf("answer without %q:\n%s", w, b)
				}
			}

			if c.name == "payload type" && strings.Contains(string(b), "a=send") {
				t.Errorf("answer of a direction:\n%s", b)
			}
		})
	}
}

// TS 24.379 §6.3.3.1.1, TS 24.229 §6.1.1: the offer to an invited member: the call's payload type, RTP/AVP,
// sendrecv whatever the originator's direction, the b=AS of TS 26.114 Table K.6 for the server's IPv6 address
// without one in the originator's offer.
func TestOfferToMember(t *testing.T) {
	sp := originator(t)

	b, err := sp.offer(leg(t, "2001:db8::1"), 1, controlchannel.Params{FloorSSRC: 5, HasFloorSSRC: true, Priority: 3})
	if err != nil {
		t.Fatal(err)
	}

	for _, w := range []string{
		"m=audio 20000 RTP/AVP 96\r\ni=speech\r\nb=AS:38\r\na=rtpmap:96 AMR-WB/16000/1\r\n",
		"a=fmtp:MCPTT mc_priority=3;mc_floor_ssrc=5\r\n",
	} {
		if !strings.Contains(string(b), w) {
			t.Errorf("offer without %q:\n%s", w, b)
		}
	}

	if strings.Contains(string(b), "only") {
		t.Errorf("offer of the originator's direction:\n%s", b)
	}
}

// RFC 3264 §5.1, §6.1: the relay's direction of a peer's stream, the reverse of the peer's, of its media or session
// level.
func TestRemoteDirection(t *testing.T) {
	for audio, want := range map[string]media.Direction{
		"m=audio 4000 RTP/AVP 96\r\n":               media.SendRecv,
		"m=audio 4000 RTP/AVP 96\r\na=sendonly\r\n": media.RecvOnly,
		"a=recvonly\r\nm=audio 4000 RTP/AVP 96\r\n": media.SendOnly,
		"m=audio 4000 RTP/AVP 96\r\na=inactive\r\n": media.Inactive,
	} {
		rem, err := remoteOf(parse(t, audio), 0, 1)
		if err != nil || rem.Direction != want {
			t.Errorf("%q: %v, %v", audio, rem.Direction, err)
		}
	}
}
