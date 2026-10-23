package media_test

import (
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/floorctl"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/mcptt/rtp"
	"github.com/ellanetworks/ims/sip/siptest"
)

// strand serializes the floor machines' inputs, as a call's strand does.
type strand struct {
	sync.Mutex
	c *siptest.Clock
}

func (s *strand) run(f func()) {
	s.Lock()
	defer s.Unlock()

	f()
}

func (s *strand) AfterFunc(d time.Duration, f func()) floor.Timer {
	return s.c.AfterFunc(d, func() { s.run(f) })
}

func (s *strand) Now() time.Time { return s.c.Now() }

// floorOut is the floor control server's output on the media of the call.
type floorOut struct {
	t    *testing.T
	call *media.Call
	legs map[*floor.Member]*media.Leg
}

func (o *floorOut) Send(m *floor.Member, msg floorctl.Message) {
	if err := o.legs[m].SendFloor(msg); err != nil {
		o.t.Error(err)
	}
}

func (o *floorOut) Forward(m *floor.Member, on bool) { o.call.Forward(o.legs[m], on) }

func (o *floorOut) Inactive() {}

func (o *floorOut) Disconnect(*floor.Member) {}

// TS 24.380 §6.3.4.4.5, §6.3.4.4.3, §6.3.5.4.6: the floor control server on the media plane. A Floor Request over the
// control channel granted, the talker's media distributed, its end detected by T1; media without permission revoked.
func TestFloorOnMedia(t *testing.T) {
	e := newEnv(t)
	st := &strand{c: e.n.Clock()}
	call := e.r.NewCall()
	out := &floorOut{t: t, call: call, legs: map[*floor.Member]*media.Leg{}}

	timers := floor.DefaultServerTimers
	timers.T7 = 4 * time.Second

	s, err := floor.NewServer(floor.ServerConfig{Clock: st, Out: out, Timers: timers})
	if err != nil {
		t.Fatal(err)
	}

	var clients []*client

	for i, id := range []string{"sip:a@x", "sip:b@x", "sip:c@x"} {
		var m *floor.Member

		cl := e.client(call, i+1, media.LegConfig{SharedControl: true, PayloadType: 96})
		cl.leg = rewire(e, call, cl, func(msg floorctl.Message) { st.run(func() { m.Receive(msg) }) },
			func() { st.run(m.Media) })

		ssrc, _ := cl.leg.FloorSSRC()

		st.run(func() {
			m = s.Join(floor.MemberConfig{ID: id, AudioSSRC: cl.leg.AudioSSRC(), FloorSSRC: ssrc + 1, HasFloorSSRC: true}, false)
			out.legs[m] = cl.leg
		})

		clients = append(clients, cl)
	}

	st.run(func() { s.Start(false) })
	e.n.Quiesce()

	a, b := clients[0], clients[1]
	for _, cl := range clients {
		cl.control.Take()
	}

	floorMsg := func(cl *client, msg floorctl.Message) {
		ssrc, _ := cl.leg.FloorSSRC()
		msg.SSRC = ssrc

		app, err := msg.App()
		if err != nil {
			t.Fatal(err)
		}

		buf, _ := rtp.AppendRTCP(nil, app)
		_ = cl.control.WriteTo(buf, cl.leg.Control())
	}

	floorMsg(a, floorctl.Message{Type: floorctl.Request})
	e.n.Quiesce()

	if got := received(t, a); len(got) != 1 || got[0].Type != floorctl.Granted || *got[0].GrantedSSRC != a.leg.AudioSSRC() {
		t.Fatalf("a received %+v", got)
	}

	if got := received(t, b); len(got) != 1 || got[0].Type != floorctl.Taken || got[0].GrantedPartyID != "sip:a@x" {
		t.Fatalf("b received %+v", got)
	}

	// Media of a, 50 packets a second for 2 seconds, to b and c; then silence: T1 ends the grant.
	for i := range 100 {
		_ = a.audio.WriteTo(packet(t, a.leg.AudioSSRC(), uint16(i), 1), a.leg.Audio())

		e.n.Advance(20 * time.Millisecond)
	}

	if n := len(b.audio.Take()); n != 100 {
		t.Fatalf("b received %d packets", n)
	}

	e.n.Advance(4 * time.Second)

	if got := received(t, b); len(got) != 1 || got[0].Type != floorctl.Idle {
		t.Fatalf("b received %+v after the silence", got)
	}

	// Media of b without permission: revoked, not distributed.
	floorMsg(a, floorctl.Message{Type: floorctl.Request})
	e.n.Quiesce()
	received(t, b)
	clients[2].audio.Take()

	_ = b.audio.WriteTo(packet(t, b.leg.AudioSSRC(), 1, 1), b.leg.Audio())

	e.n.Quiesce()

	got := received(t, b)
	if len(got) != 1 || got[0].Type != floorctl.Revoke || got[0].RejectCause.Cause != floorctl.RevokeNoPermission {
		t.Errorf("b received %+v", got)
	}

	if n := len(clients[2].audio.Take()) + len(a.audio.Take()); n != 0 {
		t.Errorf("media without permission distributed: %d", n)
	}
}

// rewire replaces the leg of cl with one reporting to the floor machines.
func rewire(e *env, call *media.Call, cl *client, onFloor func(floorctl.Message), onMedia func()) *media.Leg {
	e.t.Helper()

	cl.leg.Close()

	l, err := call.AddLeg(media.LegConfig{SharedControl: true, PayloadType: 96, OnFloor: onFloor, OnMedia: onMedia})
	if err != nil {
		e.t.Fatal(err)
	}

	l.SetRemote(media.Remote{Audio: cl.audio.LocalAddr(), Control: cl.control.LocalAddr()})

	return l
}

// received are the floor control messages cl's control socket received since the last call.
func received(t *testing.T, cl *client) []floorctl.Message {
	t.Helper()

	var ms []floorctl.Message

	for _, d := range cl.control.Take() {
		ps, err := rtp.ParseRTCP(d.B)
		if err != nil {
			t.Fatal(err)
		}

		for _, p := range ps {
			m, err := floorctl.Decode(p.(*rtp.App))
			if err != nil {
				t.Fatal(err)
			}

			ms = append(ms, m)
		}
	}

	return ms
}
