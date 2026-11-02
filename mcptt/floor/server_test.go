package floor

import (
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

func started(t *testing.T) (*Server, *serverOut, *fakeClock, []*Member) {
	t.Helper()

	s, out, c := newServer(t)
	ms := []*Member{s.Join(member("a", 100), false), s.Join(member("b", 200), false), s.Join(member("c", 300), false)}
	s.Start(false)
	out.take()

	return s, out, c, ms
}

func granted(t *testing.T) (*Server, *serverOut, *fakeClock, []*Member) {
	t.Helper()

	s, out, c, ms := started(t)
	ms[0].Receive(floorctl.Message{Type: floorctl.Request})
	out.take()

	return s, out, c, ms
}

// TS 24.380 clause 6.3.4.2.2
func TestStart(t *testing.T) {
	for _, c := range []struct {
		name              string
		members           int
		implicit, granted bool
		want              []string
	}{
		{"mc_granted", 2, true, true, []string{"a:Granted", "b:Taken"}},
		{"implicit request", 2, true, false, []string{"a:Granted", "b:Taken"}},
		{"implicit request alone, until another joins", 1, true, false, nil},
		{"no implicit request", 2, false, false, []string{"a:Idle", "b:Idle"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, out, _ := newServer(t)
			a := member("a", 100)
			a.Priority = 3
			s.Join(a, c.implicit)

			if c.members == 2 {
				s.Join(member("b", 200), false)
			}

			if len(out.take()) != 0 {
				t.Fatal("sent before start")
			}

			s.Start(c.granted)

			sent := out.take()
			want(t, sent, c.want...)

			if len(sent) > 0 && sent[0].msg.Type == floorctl.Granted && *sent[0].msg.Priority != 3 {
				t.Errorf("granted priority %d", *sent[0].msg.Priority)
			}
		})
	}
}

// TS 24.380 clause 6.3.5.2.2
func TestJoin(t *testing.T) {
	s, out, _, ms := started(t)

	s.Join(member("d", 400), false)
	want(t, out.take(), "d:Idle")

	e := s.Join(member("e", 500), true)

	sent := out.take()
	want(t, sent, "e:Granted", "a:Taken", "b:Taken", "c:Taken", "d:Taken")

	if sent[1].msg.GrantedPartyID != "e" || *sent[1].msg.GrantedSSRC != 500 || e.state != uPermitted {
		t.Errorf("Floor Taken %+v; e %d", sent[1].msg, e.state)
	}

	s.Join(member("f", 600), true)
	want(t, out.take(), "f:Taken")

	if ms[0].state != uNotPermittedTaken {
		t.Errorf("a %d", ms[0].state)
	}
}

// TS 24.380 clause 6.3.4.4.2
func TestGrant(t *testing.T) {
	s, out, _ := newServer(t)
	a := member("a", 100)
	a.Priority = 5
	s.Join(a, false)
	s.Join(MemberConfig{ID: "b", AudioSSRC: 200}, false)
	s.Start(false)

	idle := out.take()

	s.members[0].Receive(floorctl.Message{Type: floorctl.Request, Priority: ptr[uint8](9)})

	sent := out.take()
	want(t, sent, "a:Granted", "b:Taken")

	g, tk := sent[0].msg, sent[1].msg
	if g.SSRC != 1100 || *g.Duration != 30 || *g.Priority != 5 || *g.GrantedSSRC != 100 {
		t.Errorf("Floor Granted %+v", g)
	}

	if tk.SSRC != 0x5e5e || tk.GrantedPartyID != "a" || !*tk.Permission || *tk.GrantedSSRC != 100 ||
		*tk.SequenceNumber != *idle[1].msg.SequenceNumber+1 {
		t.Errorf("Floor Taken %+v", tk)
	}
}

// TS 24.380 clause 6.3.4.3.3
func TestRequestReceiveOnly(t *testing.T) {
	s, out, _ := newServer(t)
	s.Join(member("a", 100), false)

	r := member("r", 200)
	r.ReceiveOnly = true
	m := s.Join(r, false)
	s.Start(false)
	out.take()

	m.Receive(floorctl.Message{Type: floorctl.Request})

	sent := out.take()
	want(t, sent, "r:Deny")

	if sent[0].msg.RejectCause.Cause != floorctl.DenyReceiveOnly {
		t.Errorf("cause %d", sent[0].msg.RejectCause.Cause)
	}
}

// TS 24.380 clause 6.3.5.4.4
func TestRequestTakenReceiveOnly(t *testing.T) {
	s, out, _, _ := granted(t)

	r := member("r", 400)
	r.ReceiveOnly = true
	m := s.Join(r, false)

	out.take()

	m.Receive(floorctl.Message{Type: floorctl.Request})

	if sent := out.take(); sent[0].msg.RejectCause.Cause != floorctl.DenyReceiveOnly {
		t.Errorf("%+v", sent)
	}
}

// TS 24.380 clause 6.3.4.3.4
func TestT7(t *testing.T) {
	s, out, c, _ := started(t)
	s.cfg.Timers.C7 = 3

	c.advance(10*s.cfg.Timers.T7, nil)

	sent := out.take()
	want(t, sent, "a:Idle", "b:Idle", "c:Idle", "a:Idle", "b:Idle", "c:Idle")

	if *sent[3].msg.SequenceNumber != *sent[0].msg.SequenceNumber+1 {
		t.Errorf("sequence numbers %d, %d", *sent[0].msg.SequenceNumber, *sent[3].msg.SequenceNumber)
	}
}

// TS 24.380 clause 6.3.4.3.5
func TestT4(t *testing.T) {
	s, out, c, _ := started(t)
	s.cfg.Timers.C7 = 1

	c.advance(s.cfg.Timers.T4, nil)
	c.advance(s.cfg.Timers.T4, nil)

	if out.inactive != 2 {
		t.Fatalf("inactive %d", out.inactive)
	}

	out.onIdle = s.Release
	c.advance(s.cfg.Timers.T4, nil)
	c.advance(2*s.cfg.Timers.T4, nil)

	if out.inactive != 3 || s.state != gReleasing || len(c.timers) != 0 {
		t.Errorf("inactive %d, state %d, %d timers", out.inactive, s.state, len(c.timers))
	}
}

// TS 24.380 clause 6.3.4.4.3
// TS 24.380 clause 6.3.4.4.5
func TestT1(t *testing.T) {
	s, out, c, ms := granted(t)

	c.advance(s.cfg.Timers.T1-time.Millisecond, nil)
	ms[0].Media()
	c.advance(s.cfg.Timers.T1-time.Millisecond, nil)

	if s.state != gTaken || len(out.take()) != 0 {
		t.Fatalf("state %d", s.state)
	}

	c.advance(time.Millisecond, nil)
	want(t, out.take(), "a:Idle", "b:Idle", "c:Idle")

	if out.forward["a"] || s.granted != nil {
		t.Error("a still forwarded")
	}
}

// TS 24.380 clause 6.3.4.4.4
// TS 24.380 clause 6.3.4.5.2
// TS 24.380 clause 6.3.5.6.3
// TS 24.380 clause 6.3.4.5.3
// TS 24.380 clause 6.3.4.5.5
func TestT2T3(t *testing.T) {
	s, out, c, ms := granted(t)
	a := ms[0]

	tick := func(n int) {
		for range n {
			c.advance(time.Second, nil)
			a.Media()
		}
	}

	a.Media()
	tick(30)

	sent := out.take()
	want(t, sent, "a:Revoke")

	if sent[0].msg.RejectCause.Cause != floorctl.RevokeMediaBurstTooLong || a.state != uPendingRevoke ||
		s.state != gPendingRevoke || !out.forward["a"] {
		t.Fatalf("revoke %+v, a %d, general %d", sent[0].msg, a.state, s.state)
	}

	tick(2)
	want(t, out.take(), "a:Revoke", "a:Revoke")

	tick(1)
	want(t, out.take(), "a:Idle", "b:Idle", "c:Idle")

	if a.state != uNotPermittedIdle || s.state != gIdle || out.forward["a"] || len(out.disconnected) != 0 {
		t.Errorf("a %d, general %d, disconnected %v", a.state, s.state, out.disconnected)
	}
}

// TS 24.380 clause 6.3.5.6.3
// TS 24.380 clause 11.1.3
func TestGraceLongerThanRevokes(t *testing.T) {
	s, out, c, ms := granted(t)
	s.cfg.Timers.T2, s.cfg.Timers.T3 = time.Second, 5*time.Second
	a := ms[0]

	a.Media()

	for range 6 {
		c.advance(time.Second, nil)
		a.Media()
	}

	want(t, out.take(), "a:Revoke", "a:Revoke", "a:Revoke", "a:Revoke", "a:Revoke", "a:Idle", "b:Idle", "c:Idle")

	if len(out.disconnected) != 0 || a.state != uNotPermittedIdle {
		t.Errorf("disconnected %v, a %d", out.disconnected, a.state)
	}
}

// TS 24.380 clause 6.3.5.6.5
// TS 24.380 clause 6.3.4.5.4
func TestReleasePendingRevoke(t *testing.T) {
	s, out, c, ms := granted(t)
	s.cfg.Timers.T2 = time.Second

	ms[0].Media()
	c.advance(time.Second, nil)
	out.take()

	ms[0].Receive(floorctl.Message{Type: floorctl.Release})
	want(t, out.take(), "a:Idle", "b:Idle", "c:Idle")

	c.advance(time.Minute, nil)

	for _, s := range out.take() {
		if s.msg.Type == floorctl.Revoke {
			t.Fatal("Floor Revoke after the release")
		}
	}
}

// TS 24.380 clause 6.3.4.4.8
func TestRequestGranted(t *testing.T) {
	_, out, c, ms := granted(t)
	ms[0].Media()

	for range 10 {
		c.advance(time.Second, nil)
		ms[0].Media()
	}

	ms[0].Receive(floorctl.Message{Type: floorctl.Request})

	sent := out.take()
	want(t, sent, "a:Granted")

	if *sent[0].msg.Duration != 20 {
		t.Errorf("duration %d", *sent[0].msg.Duration)
	}
}

// TS 24.380 clause 6.3.5.4.6
// TS 24.380 clause 6.3.5.7.3
// TS 24.380 clause 6.3.5.7.4
func TestMediaWithoutPermission(t *testing.T) {
	s, out, c, ms := granted(t)
	b := ms[1]

	b.Media()

	sent := out.take()
	want(t, sent, "b:Revoke")

	if sent[0].msg.RejectCause.Cause != floorctl.RevokeNoPermission || b.state != uNotPermittedSendsMedia || out.forward["b"] {
		t.Fatalf("%+v, b %d", sent[0].msg, b.state)
	}

	c.advance(s.cfg.Timers.T8, nil)
	want(t, out.take(), "b:Revoke")

	b.Receive(floorctl.Message{Type: floorctl.Release, AckRequired: true})
	want(t, out.take(), "b:Ack", "b:Taken")

	if b.state != uNotPermittedTaken || len(c.timers) != 1 {
		t.Errorf("b %d, %d timers", b.state, len(c.timers))
	}
}

// TS 24.380 clause 6.3.5.3.8
// TS 24.380 clause 6.3.5.3.7
// TS 24.380 clause 6.3.5.4.5
func TestMediaAfterRelease(t *testing.T) {
	s, out, c, ms := granted(t)
	a, b := ms[0], ms[1]

	b.Receive(floorctl.Message{Type: floorctl.Release})
	want(t, out.take(), "b:Taken")

	a.Receive(floorctl.Message{Type: floorctl.Release})
	c.advance(s.cfg.Drain, nil)
	out.take()

	ms[2].Receive(floorctl.Message{Type: floorctl.Release})
	want(t, out.take(), "c:Idle")

	ms[2].Media()

	if len(out.take()) != 0 {
		t.Fatal("media of a member that did not talk revoked")
	}

	a.Media()
	want(t, out.take(), "a:Revoke")
}

// TS 24.380 clause 6.3.5.5.3
func TestReleaseAck(t *testing.T) {
	s, out, c, ms := granted(t)
	ms[0].Receive(floorctl.Message{Type: floorctl.Release, AckRequired: true})
	c.advance(s.cfg.Drain, nil)

	sent := out.take()
	want(t, sent, "a:Ack", "a:Idle", "b:Idle", "c:Idle")

	if *sent[0].msg.Source != floorctl.SourceControlling || *sent[0].msg.MessageType != floorctl.Release || sent[0].msg.SSRC != 1100 {
		t.Errorf("Floor Ack %+v", sent[0].msg)
	}
}

// TS 24.380 clause 6.3.4.4.11
// TS 24.380 clause 6.3.3
func TestLeave(t *testing.T) {
	s, out, _, ms := granted(t)
	ms[0].Leave()
	want(t, out.take(), "b:Idle", "c:Idle")

	if out.forward["a"] || s.state != gIdle {
		t.Errorf("forwarded %v, general %d", out.forward["a"], s.state)
	}

	ms[0].Receive(floorctl.Message{Type: floorctl.Request})
	ms[0].Media()

	if len(out.take()) != 0 {
		t.Error("a left, answered")
	}
}

// TS 24.380 clause 6.3.4.6.2
// TS 24.380 clause 6.3.5.8.2
func TestRelease(t *testing.T) {
	s, out, c, ms := granted(t)
	s.Release()

	ms[1].Receive(floorctl.Message{Type: floorctl.Request})
	ms[0].Media()
	c.advance(time.Hour, nil)

	if len(out.take()) != 0 || out.forward["a"] || len(c.timers) != 0 {
		t.Errorf("forwarded %v, %d timers", out.forward["a"], len(c.timers))
	}

	if m := s.Join(member("d", 400), true); m.state != uReleasing || len(out.take()) != 0 {
		t.Error("joined a released call")
	}
}

// TS 24.380 clause 6.3.2.2
func TestStartPending(t *testing.T) {
	s, out, _ := newServer(t)
	s.Join(member("a", 100), true)
	s.Start(false)

	if len(out.take()) != 0 {
		t.Fatal("answered alone")
	}

	s.Join(member("b", 200), false)
	want(t, out.take(), "a:Granted", "b:Taken")
}

// TS 24.380 clause A.3.2
func TestDrain(t *testing.T) {
	s, out, c, ms := granted(t)
	a := ms[0]

	a.Receive(floorctl.Message{Type: floorctl.Release})
	c.advance(s.cfg.Drain/2, nil)
	a.Media()

	if len(out.take()) != 0 || !out.forward["a"] {
		t.Fatal("media of the drain revoked, or not distributed")
	}

	ms[1].Receive(floorctl.Message{Type: floorctl.Request})
	want(t, out.take(), "b:Deny")

	a.Receive(floorctl.Message{Type: floorctl.Request})
	want(t, out.take(), "a:Granted", "b:Taken", "c:Taken")

	c.advance(time.Second, nil)

	if s.state != gTaken || s.granted != a {
		t.Errorf("state %d", s.state)
	}
}

// TS 24.380 clause 6.3.5.7.3
func TestRevokesGiveUp(t *testing.T) {
	s, out, c, ms := granted(t)
	b := ms[1]
	t8 := s.cfg.Timers.T8

	b.Media()

	for range 2 {
		c.advance(t8/2, nil)
		b.Media()
		c.advance(t8/2, nil)
	}

	want(t, out.take(), "b:Revoke", "b:Revoke", "b:Revoke")
	c.advance(5*t8, nil)
	want(t, only(out.take(), "b"), "b:Revoke", "b:Revoke", "b:Revoke", "b:Revoke", "b:Revoke")

	if len(out.disconnected) != 0 {
		t.Fatalf("disconnected %v", out.disconnected)
	}

	for range 3 {
		c.advance(t8/2, nil)
		b.Media()
		c.advance(t8/2, nil)
	}

	want(t, only(out.take(), "b"), "b:Revoke", "b:Revoke")

	if len(out.disconnected) != 1 || out.disconnected[0] != "b" {
		t.Errorf("disconnected %v", out.disconnected)
	}
}

// TS 24.380 clause 6.3.4.4.2
func TestTakenLocation(t *testing.T) {
	_, out, _, ms := started(t)
	loc := floorctl.Field{ID: floorctl.FieldLocation, Value: []byte{3, 0x13, 0x00, 0x14}}
	ms[0].Receive(floorctl.Message{Type: floorctl.Request, Other: []floorctl.Field{loc}})

	sent := out.take()
	if tk := sent[1].msg; len(tk.Other) != 1 || tk.Other[0].ID != floorctl.FieldLocation || string(tk.Other[0].Value) != string(loc.Value) {
		t.Errorf("Floor Taken %+v", tk)
	}

	if _, err := sent[1].msg.App(); err != nil {
		t.Error(err)
	}
}
