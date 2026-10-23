package controlchannel

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip/sdp"
)

// TS 24.380 §4.3.3.1: the example media description of a control channel.
func TestChannelExample(t *testing.T) {
	s := parse(t, "v=0\r\nc=IN IP4 192.0.2.1\r\nm=audio 20030 RTP/AVP 96\r\n"+
		"m=application 20032 udp MCPTT\r\na=fmtp:MCPTT mc_queueing;mc_priority=5;mc_granted;mc_floor_ssrc=12345678\r\n")

	i := Channel(s)
	if i != 1 {
		t.Fatalf("channel %d", i)
	}

	p, err := MediaParams(s.Media[i])
	if err != nil {
		t.Fatal(err)
	}

	want := Params{Queueing: true, Priority: 5, Granted: true, FloorSSRC: 12345678, HasFloorSSRC: true}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("params %+v", p)
	}

	m := NewMedia(20032, p)
	if d, _ := m.Desc(); d.String() != "application 20032 udp MCPTT" || !IsChannel(m) {
		t.Errorf("NewMedia m=%s", d)
	}

	if fmtp := m.Attrs("fmtp"); len(fmtp) != 1 || fmtp[0] != "MCPTT mc_queueing;mc_priority=5;mc_granted;mc_floor_ssrc=12345678" {
		t.Errorf("NewMedia fmtp %q", fmtp)
	}

	if bare := NewMedia(1, Params{}); len(bare.Attrs("fmtp")) != 0 {
		t.Error("fmtp without parameters")
	}
}

// TS 24.379 Table A.1.3-1: a client's offer, the m= line's format in lower case.
func TestClientOfferExample(t *testing.T) {
	s := parse(t, "v=0\r\nc=IN IP6 5555::aaa:bbb:ccc:eee\r\nm=audio 3456 RTP/AVP 97\r\na=rtpmap:97 AMR\r\n"+
		"a=fmtp:97 mode-set=0,2,5,7; mode-change-period=2\r\na=maxptime:20\r\ni=speech\r\n"+
		"m=application 3457 udp mcptt\r\na=fmtp:MCPTT mc_queueing;mc_implicit_request\r\n")

	i := Channel(s)
	if i != 1 {
		t.Fatalf("channel %d", i)
	}

	p, err := MediaParams(s.Media[i])
	if err != nil || !reflect.DeepEqual(p, Params{Queueing: true, ImplicitRequest: true}) {
		t.Fatalf("params %+v, %v", p, err)
	}
}

// TS 24.380 §12.1.2.3: the ABNF's ":" separator and the examples' ";" alike; mc_priority 1 to 255; 32-bit SSRCs;
// unknown parameters kept.
func TestParseParams(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Params
		out  string
	}{
		{
			"mc_queueing:mc_priority=200:mc_implicit_request",
			Params{Queueing: true, Priority: 200, ImplicitRequest: true},
			"mc_queueing;mc_priority=200;mc_implicit_request",
		},
		{"mc_queueing;", Params{Queueing: true}, "mc_queueing"},
		{
			"mc_ssrc=4294967295;mc_no_floor_ctrl",
			Params{SSRC: 4294967295, HasSSRC: true, NoFloorControl: true},
			"mc_ssrc=4294967295;mc_no_floor_ctrl",
		},
		{"mc_floor_ssrc=0;mc_future=1;mc_granted", Params{
			Granted: true, HasFloorSSRC: true,
			Other: sdp.FormatParams{{Name: "mc_future", Value: "1", HasValue: true}},
		}, "mc_granted;mc_floor_ssrc=0;mc_future=1"},
		{"", Params{}, ""},
	} {
		p, err := ParseParams(c.in)
		if err != nil || !reflect.DeepEqual(p, c.want) {
			t.Errorf("%q: %+v, %v", c.in, p, err)
		}

		if s := p.String(); s != c.out {
			t.Errorf("%q: String %q, want %q", c.in, s, c.out)
		}
	}

	// A malformed value is left out, its parameter absent, the others kept.
	for _, in := range []string{"mc_priority=0", "mc_priority=256", "mc_priority=", "mc_ssrc=4294967296", "mc_floor_ssrc=x"} {
		p, err := ParseParams("mc_queueing;" + in)
		if err == nil || !reflect.DeepEqual(p, Params{Queueing: true}) {
			t.Errorf("%q: %+v, %v", in, p, err)
		}
	}

	// The first of a parameter given twice counts. Parameters with a ";" keep the ":" of their values; without, ":"
	// separates only parameters of the present document.
	for in, want := range map[string]Params{
		"mc_priority=3;mc_priority=9;x=a:mc_b;mc_granted": {
			Priority: 3, Granted: true, Other: sdp.FormatParams{{Name: "x", Value: "a:mc_b", HasValue: true}},
		},
		"x=a:b:mc_priority=3:mc_priority=9:mc_granted": {
			Priority: 3, Granted: true, Other: sdp.FormatParams{{Name: "x", Value: "a:b", HasValue: true}},
		},
	} {
		if p, err := ParseParams(in); err != nil || !reflect.DeepEqual(p, want) {
			t.Errorf("%q: %+v, %v", in, p, err)
		}
	}

	// A flag has no value (§12.1.2.3).
	for _, in := range []string{"mc_queueing=1", "mc_granted=", "mc_implicit_request=yes", "mc_no_floor_ctrl=0"} {
		if p, err := ParseParams(in); err == nil || !reflect.DeepEqual(p, Params{}) {
			t.Errorf("%q: %+v, %v", in, p, err)
		}
	}
}

// TS 24.380 §14.2: the offer of an initial INVITE has no mc_ssrc, and no floor control parameters with
// mc_no_floor_ctrl.
func TestOffer(t *testing.T) {
	local := Params{
		Queueing: true, Priority: 5, Granted: true, ImplicitRequest: true, SSRC: 1, HasSSRC: true,
		FloorSSRC: 2, HasFloorSSRC: true,
	}

	if got := Offer(local).String(); got != "mc_queueing;mc_priority=5;mc_granted;mc_implicit_request;mc_floor_ssrc=2" {
		t.Errorf("offer: %s", got)
	}

	local.NoFloorControl = true
	if got := Offer(local).String(); got != "mc_no_floor_ctrl;mc_floor_ssrc=2" {
		t.Errorf("without floor control: %s", got)
	}
}

// TS 24.380 §14.3: the answer has only parameters of the offer; the lower priority, the one offered of a client,
// none of a receive-only member (§14.3.3); mc_ssrc only with the implicit floor request accepted; mc_floor_ssrc only
// when offered; none of floor control for mc_no_floor_ctrl, which an answerer without it cannot answer.
func TestAnswer(t *testing.T) {
	local := Params{
		Queueing: true, Priority: 9, Granted: true, ImplicitRequest: true, SSRC: 7, HasSSRC: true,
		FloorSSRC: 99, HasFloorSSRC: true, Other: sdp.FormatParams{{Name: "mc_x"}, {Name: "mc_y"}},
	}

	for _, c := range []struct {
		name  string
		offer Params
		local Params
		want  string
	}{
		{
			"implicit request accepted",
			Params{Priority: 5, ImplicitRequest: true, HasFloorSSRC: true, Other: sdp.FormatParams{{Name: "MC_Y"}}},
			local, "mc_priority=5;mc_implicit_request;mc_ssrc=7;mc_floor_ssrc=99;mc_y",
		},
		{
			"implicit request declined",
			Params{ImplicitRequest: true, Granted: true},
			Params{Granted: true, SSRC: 7, HasSSRC: true},
			"",
		},
		{"lower priority granted", Params{Queueing: true, Priority: 200}, local, "mc_queueing;mc_priority=9"},
		{"offered priority returned", Params{Priority: 12}, Params{Priority: 255}, "mc_priority=12"},
		{"receive-only member", Params{Priority: 12, Queueing: true}, Params{Queueing: true}, "mc_queueing"},
		{
			"no floor control",
			Params{NoFloorControl: true, Queueing: true, ImplicitRequest: true, HasFloorSSRC: true},
			Params{NoFloorControl: true, Queueing: true, ImplicitRequest: true, SSRC: 7, HasSSRC: true, FloorSSRC: 3, HasFloorSSRC: true},
			"mc_no_floor_ctrl;mc_floor_ssrc=3",
		},
	} {
		got, err := Answer(c.offer, c.local)
		if err != nil || got.String() != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}

	if a, err := Answer(Params{NoFloorControl: true}, local); !errors.Is(err, ErrNoFloorControl) {
		t.Errorf("mc_no_floor_ctrl unsupported: %q, %v", a, err)
	}
}

// TS 24.380 §14.4: the offerer discards what it did not offer, but uses mc_ssrc whether offered or not; an
// mc_priority above the one offered is refused.
func TestAccept(t *testing.T) {
	offer := Params{Queueing: true, Priority: 5, ImplicitRequest: true, HasFloorSSRC: true}

	got, err := offer.Accept(Params{
		Queueing: true, Priority: 4, Granted: true, ImplicitRequest: true, SSRC: 7, HasSSRC: true,
		FloorSSRC: 9, HasFloorSSRC: true, Other: sdp.FormatParams{{Name: "mc_x"}},
	})
	if err != nil || got.String() != "mc_queueing;mc_priority=4;mc_implicit_request;mc_ssrc=7;mc_floor_ssrc=9" {
		t.Errorf("%s, %v", got, err)
	}

	// mc_ssrc without the implicit floor request offered (§14.4), and mc_implicit_request from a peer following
	// §14.3.7, not offered, discarded.
	got, err = Params{NoFloorControl: true}.Accept(Params{NoFloorControl: true, ImplicitRequest: true, SSRC: 7, HasSSRC: true})
	if err != nil || got.String() != "mc_ssrc=7;mc_no_floor_ctrl" {
		t.Errorf("%s, %v", got, err)
	}

	if got, err := offer.Accept(Params{Priority: 6}); !errors.Is(err, ErrPriority) || got.Priority != 0 {
		t.Errorf("%+v, %v", got, err)
	}
}

func TestIsChannel(t *testing.T) {
	s := parse(t, "v=0\r\nm=application 1 udp MCPTT x\r\nm=application 2 TCP MCPTT\r\nm=audio 3 udp MCPTT\r\n")
	if Channel(s) != -1 {
		t.Errorf("channel %d", Channel(s))
	}
}

func parse(t *testing.T, s string) *sdp.Session {
	t.Helper()

	sess, err := sdp.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}

	return sess
}

func FuzzFmtp(f *testing.F) {
	for _, s := range []string{
		"mc_queueing;mc_priority=5;mc_granted;mc_floor_ssrc=12345678", "mc_queueing:mc_implicit_request",
		"mc_ssrc=4294967295;mc_no_floor_ctrl;x=y", " ; ;mc_priority=1",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		// What parsed, malformed values left out, round-trips; but a lone parameter whose value holds ":mc_", which
		// the ":" separator of the ABNF makes ambiguous.
		p, _ := ParseParams(s)
		if out := p.String(); !strings.Contains(out, ";") && strings.Contains(strings.ToLower(out), ":mc_") {
			return
		}

		again, err := ParseParams(p.String())
		if err != nil || !reflect.DeepEqual(again, p) {
			t.Fatalf("%q: %+v does not round-trip: %+v, %v", s, p, again, err)
		}

		// An answer by the rules of §14.3 is one the offerer takes as is (§14.4).
		offer := Offer(p)

		answer, err := Answer(offer, p)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}

		if got, err := offer.Accept(answer); err != nil || !reflect.DeepEqual(got, answer) {
			t.Fatalf("%q: answer %+v accepted as %+v, %v", s, answer, got, err)
		}
	})
}
