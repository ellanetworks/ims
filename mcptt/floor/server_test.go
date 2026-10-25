package floor

import (
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

// started is a server of members a, b and c, the floor idle, what it sent at start taken.
func started(t *testing.T) (*Server, *serverOut, *fakeClock, []*Member) {
	t.Helper()

	s, out, c := newServer(t)
	ms := []*Member{s.Join(member("a", 100), false), s.Join(member("b", 200), false), s.Join(member("c", 300), false)}
	s.Start(false)
	out.take()

	return s, out, c, ms
}

// granted is started with the floor granted to a.
func granted(t *testing.T) (*Server, *serverOut, *fakeClock, []*Member) {
	t.Helper()

	s, out, c, ms := started(t)
	ms[0].Receive(floorctl.Message{Type: floorctl.Request})
	out.take()

	return s, out, c, ms
}

// §6.3.4.2.2: the originator's implicit request granted in its 200 by mc_granted, or as a Floor Request; without
// one, or denied for want of another participant (§6.3.4.3.3), the floor idle.
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

			// §6.3.5.2.2 3b i, NOTE 4: the implicit request of the negotiated maximum priority, with mc_granted or not.
			if len(sent) > 0 && sent[0].msg.Type == floorctl.Granted && *sent[0].msg.Priority != 3 {
				t.Errorf("granted priority %d", *sent[0].msg.Priority)
			}
		})
	}
}

// §6.3.5.2.2: a member joining a started call is told the floor's state; its implicit request granted when idle.
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

// §6.3.4.4.2: the Floor Granted carries T2, the granted priority, the Audio SSRC; the Floor Taken the granted
// party, the next sequence number, the permission to request, the Audio SSRC; each in the SSRC the member assigned
// (§8.1.2, §14.3.8), the server's own otherwise.
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

// §6.3.4.3.3: a request in an idle floor denied of a receive-only member (cause #5).
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

// §6.3.5.4.4: in a taken floor, a receive-only member is denied with cause #5.
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

// §6.3.4.3.4: T7 repeats the Floor Idle, C7 in all, each of the next sequence number.
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

// §6.3.4.3.5: T4 tells the call; restarted unless the call releases.
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

// §6.3.4.4.3: T1, without media, ends the grant; §6.3.4.4.5: media restarts it.
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

// §6.3.4.4.4, §6.3.4.5.2: T2, from the first media, revokes with cause #2; §6.3.5.6.3: T8 repeats the Floor Revoke;
// §6.3.4.5.3: media forwarded in the grace period; §6.3.4.5.5: T3 ends it.
func TestT2T3(t *testing.T) {
	s, out, c, ms := granted(t)
	a := ms[0]

	// Media every second, T1 never expiring.
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

// §6.3.5.6.3 NOTE, Table 11.1.3-1: in the grace period, Floor Revokes are resent until T3 ends it, however many; the
// talker not disconnected.
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

// §6.3.5.6.5, §6.3.4.5.4: a Floor Release in the grace period ends it.
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

// §6.3.4.4.8: a Floor Request of the granted member is granted again, with the time left to talk.
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

// §6.3.5.4.6: media without permission revoked with cause #3, not forwarded; §6.3.5.7.3: T8 repeats it;
// §6.3.5.7.4: a Floor Release answered with the floor's state.
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

// §6.3.5.3.8: media after a Floor Release revoked; §6.3.5.3.7, §6.3.5.4.5: a Floor Release without permission
// answered with the floor's state.
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

// §6.3.5.5.3: a Floor Release asking for an acknowledgement gets a Floor Ack of the controlling function.
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

// §6.3.4.4.11: the granted member leaving releases the floor; §6.3.3: no more messages to it.
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

// §6.3.4.6.2, §6.3.5.8.2: the call released, no messages, media or timers.
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

// §6.3.2.2: an implicit request of the originator alone granted when the first invited member joins.
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

// Annex A.3.2 steps 9 to 12: the media of a member that released the floor still distributed for the drain, its
// late media not revoked; a request in the drain granted again.
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

// §6.3.5.7.3 NOTE: a member that keeps sending media after the Floor Revokes is given up on, the call asked to
// disconnect it; one that stopped is not, the Floor Revoke resent (§6.3.5.7.3 step 1).
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

	// Media after the second; it stops after the third. (a's grant ends meanwhile: T1.)
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

// §6.3.4.4.2 3a iii: the Floor Taken carries the location of the granted member's request, "not provided" of an
// implicit request.
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
