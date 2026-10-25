package floor

import (
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

// loop is a call of floor participants and a server, their messages delivered as on a network: queued, then
// delivered in order by flush.
type loop struct {
	t       *testing.T
	c       *fakeClock
	s       *Server
	out     *serverOut
	queue   []func()
	clients map[string]*client
}

type client struct {
	p   *Participant
	out *participantOut
	m   *Member
}

func newLoop(t *testing.T, ids ...string) *loop {
	t.Helper()

	s, out, c := newServer(t)
	l := &loop{t: t, c: c, s: s, out: out, clients: map[string]*client{}}

	out.to = func(m *Member, msg floorctl.Message) {
		l.queue = append(l.queue, func() { l.clients[m.cfg.ID].p.Receive(msg) })
	}

	for i, id := range ids {
		cl := &client{out: &participantOut{}}
		cl.p = newTestParticipant(t, c, cl.out, uint32(i+1))
		cl.out.to = func(msg floorctl.Message) { l.queue = append(l.queue, func() { cl.m.Receive(msg) }) }
		l.clients[id] = cl
	}

	return l
}

func (l *loop) flush() {
	for len(l.queue) > 0 {
		f := l.queue[0]
		l.queue = l.queue[1:]

		f()
	}
}

func (l *loop) advance(d time.Duration) { l.c.advance(d, l.flush) }

// start establishes the call: the originator first, without an implicit request, then the invited members; the
// floor idle.
func (l *loop) start(ids ...string) {
	for i, id := range ids {
		cl := l.clients[id]
		cl.m = l.s.Join(member(id, uint32(100+i)), false)

		if i == 0 {
			cl.p.Originate(Origination{})
		} else {
			cl.p.Join()
		}
	}

	l.s.Start(false)
	l.flush()
}

func (l *loop) state(id string) (participantState, memberState) {
	cl := l.clients[id]
	return cl.p.state, cl.m.state
}

// TS 24.380 Annex A.3.2: a floor request when the floor is idle, granted; media; released.
func TestAnnexA32(t *testing.T) {
	l := newLoop(t, "a", "b", "c")
	l.start("a", "b", "c")

	if l.s.state != gIdle {
		t.Fatalf("general %d", l.s.state)
	}

	l.out.take()

	// Steps 1–5.
	l.clients["a"].p.Press()
	l.flush()

	want(t, l.out.take(), "a:Granted", "b:Taken", "c:Taken")

	if p, m := l.state("a"); p != pHasPermission || m != uPermitted || l.s.state != gTaken {
		t.Fatalf("a %d, %d; general %d", p, m, l.s.state)
	}

	for _, id := range []string{"b", "c"} {
		if p, m := l.state(id); p != pNoPermission || m != uNotPermittedTaken {
			t.Fatalf("%s %d, %d", id, p, m)
		}
	}

	a := l.clients["a"]
	if !a.out.transmit || a.out.ssrc != 100 || !l.out.forward["a"] {
		t.Fatalf("a transmits %v with %d, forwarded %v", a.out.transmit, a.out.ssrc, l.out.forward["a"])
	}

	// Steps 6, 7.
	a.m.Media()

	// Steps 8–12.
	a.p.Unpress()
	l.flush()

	// Steps 9, 11: its last media still distributed, not revoked.
	if len(l.out.take()) != 0 || !l.out.forward["a"] {
		t.Fatal("the floor idle before the last media")
	}

	a.m.Media()
	l.advance(l.s.cfg.Drain)

	want(t, l.out.take(), "a:Idle", "b:Idle", "c:Idle")

	for _, id := range []string{"a", "b", "c"} {
		if p, m := l.state(id); p != pNoPermission || m != uNotPermittedIdle {
			t.Fatalf("%s %d, %d", id, p, m)
		}
	}

	if a.out.transmit || l.out.forward["a"] || l.s.state != gIdle {
		t.Fatalf("a transmits %v, forwarded %v; general %d", a.out.transmit, l.out.forward["a"], l.s.state)
	}

	if !slices.Equal(l.clients["b"].out.notices, []NoticeKind{NoticeIdle, NoticeTaken, NoticeIdle}) {
		t.Errorf("b notices %v", l.clients["b"].out.notices)
	}
}

// TS 24.380 Annex A.3.3: a floor request when the floor is taken, queueing not applied: denied.
func TestAnnexA33(t *testing.T) {
	l := newLoop(t, "a", "b", "c")
	l.start("a", "b", "c")
	l.clients["a"].p.Press()
	l.flush()
	l.out.take()

	b := l.clients["b"]
	b.p.Press()

	if b.p.state != pPendingRequest {
		t.Fatalf("b %d", b.p.state)
	}

	l.flush()

	sent := l.out.take()
	want(t, sent, "b:Deny")

	if sent[0].msg.RejectCause.Cause != floorctl.DenyAnotherHasPermission {
		t.Errorf("cause %d", sent[0].msg.RejectCause.Cause)
	}

	if p, m := l.state("b"); p != pNoPermission || m != uNotPermittedTaken || b.out.notices[len(b.out.notices)-1] != NoticeDenied {
		t.Errorf("b %d, %d, %v", p, m, b.out.notices)
	}
}

// TS 24.380 §6.3.4.4.4, §6.2.4.5.4, §6.3.4.5.4: the talker past T2 revoked; its client releases the floor, idle for
// every member.
func TestLoopRevoke(t *testing.T) {
	l := newLoop(t, "a", "b")
	l.start("a", "b")

	a := l.clients["a"]
	a.p.Press()
	l.flush()
	a.m.Media()
	l.out.take()

	for range 31 {
		l.advance(time.Second)
		a.m.Media()
	}

	want(t, l.out.take(), "a:Revoke", "a:Idle", "b:Idle")

	if p, m := l.state("a"); p != pNoPermission || m != uNotPermittedIdle || a.out.transmit || l.s.state != gIdle {
		t.Errorf("a %d, %d, transmit %v; general %d", p, m, a.out.transmit, l.s.state)
	}
}
