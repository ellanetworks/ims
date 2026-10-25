package floor

import (
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
)

// fakeClock is a clock of fake time: timers fire by advance, in order.
type fakeClock struct {
	now    time.Duration
	seq    int
	timers []*fakeTimer
}

type fakeTimer struct {
	c    *fakeClock
	when time.Duration
	seq  int
	f    func()
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.seq++
	t := &fakeTimer{c: c, when: c.now + d, seq: c.seq, f: f}
	c.timers = append(c.timers, t)

	return t
}

func (c *fakeClock) Now() time.Time { return time.Unix(0, 0).Add(c.now) }

func (t *fakeTimer) Stop() bool {
	i := slices.Index(t.c.timers, t)
	if i < 0 {
		return false
	}

	t.c.timers = slices.Delete(t.c.timers, i, i+1)

	return true
}

// advance moves the time by d, firing the timers due, then running after each.
func (c *fakeClock) advance(d time.Duration, after func()) {
	end := c.now + d

	for {
		slices.SortFunc(c.timers, func(a, b *fakeTimer) int {
			if a.when != b.when {
				return int(a.when - b.when)
			}

			return a.seq - b.seq
		})

		if len(c.timers) == 0 || c.timers[0].when > end {
			break
		}

		t := c.timers[0]
		c.timers = c.timers[1:]
		c.now = t.when
		t.f()

		if after != nil {
			after()
		}
	}

	c.now = end
}

// sent is a message the server sent a member.
type sent struct {
	to  string
	msg floorctl.Message
}

// serverOut records what the server does; to, when set, delivers its messages.
type serverOut struct {
	sent         []sent
	forward      map[string]bool
	inactive     int
	disconnected []string
	onIdle       func()
	to           func(m *Member, msg floorctl.Message)
}

func (o *serverOut) Send(m *Member, msg floorctl.Message) {
	o.sent = append(o.sent, sent{m.cfg.ID, msg})
	if o.to != nil {
		o.to(m, msg)
	}
}

func (o *serverOut) Forward(m *Member, on bool) {
	if o.forward == nil {
		o.forward = map[string]bool{}
	}

	o.forward[m.cfg.ID] = on
}

func (o *serverOut) Disconnect(m *Member) { o.disconnected = append(o.disconnected, m.cfg.ID) }

func (o *serverOut) Inactive() {
	o.inactive++
	if o.onIdle != nil {
		o.onIdle()
	}
}

// take returns the messages sent since the last take, as "to:type" with the fields tests check.
func (o *serverOut) take() []sent {
	s := o.sent
	o.sent = nil

	return s
}

func newServer(t *testing.T) (*Server, *serverOut, *fakeClock) {
	t.Helper()

	c := &fakeClock{}
	out := &serverOut{}

	s, err := NewServer(ServerConfig{Clock: c, Out: out, Timers: withT7(4 * time.Second), SSRC: 0x5e5e})
	if err != nil {
		t.Fatal(err)
	}

	return s, out, c
}

func member(id string, ssrc uint32) MemberConfig {
	return MemberConfig{ID: id, AudioSSRC: ssrc, FloorSSRC: ssrc + 1000, HasFloorSSRC: true}
}

// types are the "to:message" of ss.
func types(ss []sent) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.to+":"+name(s.msg.Type))
	}

	return out
}

func name(t floorctl.Type) string {
	switch t {
	case floorctl.Request:
		return "Request"
	case floorctl.Granted:
		return "Granted"
	case floorctl.Taken:
		return "Taken"
	case floorctl.Deny:
		return "Deny"
	case floorctl.Release:
		return "Release"
	case floorctl.Idle:
		return "Idle"
	case floorctl.Revoke:
		return "Revoke"
	case floorctl.Ack:
		return "Ack"
	}

	return "?"
}

// only are the messages of ss to id.
func only(ss []sent, id string) []sent {
	var out []sent

	for _, s := range ss {
		if s.to == id {
			out = append(out, s)
		}
	}

	return out
}

func want(t *testing.T, got []sent, w ...string) {
	t.Helper()

	if g := types(got); !slices.Equal(g, w) {
		t.Fatalf("sent %v, want %v", g, w)
	}
}

// participantOut records what a participant does.
type participantOut struct {
	sent     []floorctl.Message
	notices  []NoticeKind
	transmit bool
	ssrc     uint32
	to       func(msg floorctl.Message)
}

func (o *participantOut) Send(msg floorctl.Message) {
	o.sent = append(o.sent, msg)
	if o.to != nil {
		o.to(msg)
	}
}

func (o *participantOut) Transmit(on bool, ssrc uint32) { o.transmit, o.ssrc = on, ssrc }

func (o *participantOut) Notify(n Notice) { o.notices = append(o.notices, n.Kind) }

// withT7 are the default timers, with T7.
func withT7(t7 time.Duration) ServerTimers {
	t := DefaultServerTimers
	t.T7 = t7

	return t
}
