package b2bua_test

import (
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

// controller is a B2BUA inviting the members of a group for each INVITE, as a controlling function (TS 24.379
// §6.3.3): the INVITE is answered with the first member's 200, or, all failing, with the best of their failures.
type controller struct {
	*party

	members []*party
	ended   chan *b2bua.Call
}

func newController(t *testing.T, n *siptest.Net, members ...*party) *controller {
	c := &controller{members: members, ended: make(chan *b2bua.Call, 4)}

	c.party = newParty(t, n, "group", func(cfg *ua.Config) {
		cfg.Policy = ua.Policy{}
		cfg.OnInvite = c.invite
	})

	return c
}

func (c *controller) invite(r *ua.Request) ua.Handler {
	call := b2bua.NewCall(c.a, r.Strand(), b2bua.CallHandler{Ended: func(call *b2bua.Call) { c.ended <- call }})

	var (
		failures []*sip.Response
		pending  = len(c.members)
		answered bool
	)

	origin, _ := call.Incoming(r, b2bua.LegHandler{
		Ended: func(*b2bua.Leg, ua.End) { call.End(ua.Reply{}) },
	})

	member := b2bua.LegHandler{
		Established: func(l *b2bua.Leg, _ sip.Message) {
			if !answered {
				answered = true
				_ = r.Accept(sdpOf("group-answer")...)
			}
		},
		Failed: func(_ *b2bua.Leg, res *sip.Response, _ error) {
			failures = append(failures, res)
			pending--

			if pending == 0 && !answered {
				_ = r.Respond(b2bua.Best(failures))
			}
		},
	}

	for _, m := range c.members {
		uri, _ := sip.ParseURI(m.uri())
		out := sip.NewRequest("INVITE", uri)
		out.Header.Add("From", "<"+c.uri()+">")
		out.Header.Add("To", "<"+m.uri()+">")
		out.SetBodies(sdpOf("group-offer")...)

		if _, err := call.Dial(out, member); err != nil {
			pending--
		}
	}

	return origin.Handler()
}

// One leg to many: the first member to answer answers the caller; the others go on in the call, and its end ends
// them all.
func TestOneToMany(t *testing.T) {
	n := newNet(t)
	bob, carol := newParty(t, n, "bob", nil), newParty(t, n, "carol", nil)
	g := newController(t, n, bob, carol)
	alice := newParty(t, n, "alice", nil)

	alice.invite(g.uri(), "offer-1", alice.handler())

	rb, rc := bob.next("invite").req, carol.next("invite").req

	_ = rc.Accept(sdpOf("answer-c")...)

	if e := alice.next("established"); !hasSDP(e.msg, "group-answer") {
		t.Fatalf("alice's 200 %q", e.msg.Env().Body)
	}

	as := e(alice.next("answer"))
	carol.next("established")

	_ = rb.Accept(sdpOf("answer-b")...)

	bob.next("established")

	_ = as.Bye(ua.Change{}, nil)

	alice.next("ended")
	bob.next("ended")
	carol.next("ended")

	n.Quiesce()

	select {
	case <-g.ended:
	default:
		t.Fatal("the call did not end")
	}
}

func e(ev event) *ua.Session { return ev.s }

// RFC 3261 §16.7 step 6: all members failing, the caller gets the best of their failures, with all their Warnings.
func TestOneToManyFailing(t *testing.T) {
	n := newNet(t)
	bob, carol := newParty(t, n, "bob", nil), newParty(t, n, "carol", nil)
	g := newController(t, n, bob, carol)
	alice := newParty(t, n, "alice", nil)

	alice.invite(g.uri(), "offer-1", alice.handler())

	_ = bob.next("invite").req.Reject(486, sip.Field{Name: "Warning", Value: `399 bob "busy"`})
	_ = carol.next("invite").req.Reject(603, sip.Field{Name: "Warning", Value: `399 carol "declined"`})

	ev := alice.next("failed")
	if ev.res.StatusCode != 603 || len(ev.res.Header.Values("Warning")) != 2 {
		t.Fatalf("alice got %d, Warning %v", ev.res.StatusCode, ev.res.Header.Values("Warning"))
	}
}

// TS 24.379 §10.1.1.3.1.2: a long-lived leg serves a series of calls, attached to each in turn; a call ends as its
// last leg leaves it.
func TestLegAcrossCalls(t *testing.T) {
	n := newNet(t)
	bob, carol := newParty(t, n, "bob", nil), newParty(t, n, "carol", nil)

	var (
		pre   *b2bua.Leg
		ended = make(chan *b2bua.Call, 4)
	)

	server := newParty(t, n, "server", func(cfg *ua.Config) {
		cfg.Policy = ua.Policy{}
	})

	// The pre-established session: alice's INVITE answered by the server, its leg on a call of its own.
	server.mu.Lock()
	server.onInvite = func(r *ua.Request) ua.Handler {
		call := b2bua.NewCall(server.a, r.Strand(), b2bua.CallHandler{Ended: func(c *b2bua.Call) { ended <- c }})
		pre, _ = call.Incoming(r, b2bua.LegHandler{})
		_ = r.Accept(sdpOf("server-answer")...)

		return pre.Handler()
	}
	server.mu.Unlock()

	alice := newParty(t, n, "alice", nil)
	alice.invite(server.uri(), "offer-1", alice.handler())
	as := alice.next("established").s
	alice.next("answer")

	n.Quiesce()

	// Each call: dialled on the strand of the long-lived leg, which joins it.
	for _, to := range []*party{bob, carol} {
		done := make(chan struct{})

		pre.Call().Strand().Post(func() {
			defer close(done)

			call := b2bua.NewCall(server.a, pre.Call().Strand(), b2bua.CallHandler{Ended: func(c *b2bua.Call) { ended <- c }})
			if err := call.Attach(pre, b2bua.LegHandler{}); err != nil {
				t.Errorf("Attach: %v", err)
				return
			}

			uri, _ := sip.ParseURI(to.uri())
			out := sip.NewRequest("INVITE", uri)
			out.Header.Add("From", "<"+server.uri()+">")
			out.Header.Add("To", "<"+to.uri()+">")
			out.SetBodies(sdpOf("server-offer")...)

			if _, err := call.Dial(out, b2bua.LegHandler{}); err != nil {
				t.Errorf("Dial: %v", err)
			}
		})

		n.Quiesce()
		<-done

		// The former call, left by the long-lived leg, ends.
		select {
		case <-ended:
		default:
			t.Fatal("the former call did not end")
		}

		r := to.next("invite").req
		_ = r.Accept(sdpOf("answer")...)

		s := to.next("established").s
		_ = s.Bye(ua.Change{}, nil)

		to.next("ended")
	}

	if as.State() != ua.Confirmed {
		t.Fatalf("the pre-established session %s", as.State())
	}

	alice.none()
}

// RFC 3262 §5: an offer in a reliable 1xx cannot reach a caller without 100rel, who cannot answer in a PRACK: the
// offer is refused, and the caller gets a 488.
func TestBridgeOfferWithout100rel(t *testing.T) {
	n := newNet(t)

	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{})

	alice := n.Peer()
	req := siptest.NewRequest("INVITE", b.uri(), sip.UDP, alice.Addr())
	alice.Send(sip.UDP, b.addr, req)

	in := bob.next("invite").req
	_ = in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("offer-b")})

	for {
		res, _ := alice.RecvResponse()
		if res.StatusCode >= 200 {
			if res.StatusCode != 488 {
				t.Fatalf("alice got %d", res.StatusCode)
			}

			break
		}
	}
}

// The BYE of the callee ends the caller's session too, and each leg refreshes its own session timer.
func TestBridgeByeOfCallee(t *testing.T) {
	n := newNet(t)

	bob := newParty(t, n, "bob", func(c *ua.Config) { c.SessionTimer = ua.SessionTimer{Interval: 90 * time.Second} })
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{})
	alice := newParty(t, n, "alice", func(c *ua.Config) { c.SessionTimer = ua.SessionTimer{Interval: 1800 * time.Second} })

	alice.invite(b.uri(), "offer-1", alice.handler())
	_, bs := bridged(t, alice, bob)

	// Past bob's interval, his leg refreshed and alice's not involved.
	n.Advance(200 * time.Second)

	alice.none()

	if bs.State() != ua.Confirmed {
		t.Fatalf("bob's session %s", bs.State())
	}

	_ = bs.Bye(ua.Change{}, nil)

	alice.next("ended")
	bob.next("ended")
	b.callEnded(t)
}

func TestBest(t *testing.T) {
	res := func(code int, warning string) *sip.Response {
		r := sip.NewResponse(siptest.NewRequest("INVITE", "sip:x@192.0.2.1", sip.UDP, netAddr), code, "")
		if warning != "" {
			r.Header.Add("Warning", warning)
		}

		return r
	}

	for _, c := range []struct {
		in   []*sip.Response
		want int
	}{
		{nil, 408},
		{[]*sip.Response{res(486, ""), res(500, "")}, 486},
		{[]*sip.Response{res(500, ""), res(404, ""), res(603, "")}, 603},
		{[]*sip.Response{res(404, ""), res(415, "")}, 415},
		{[]*sip.Response{res(503, "")}, 500},
		{[]*sip.Response{res(302, ""), res(500, "")}, 480},
		{[]*sip.Response{res(407, "")}, 500},
	} {
		if got := b2bua.Best(c.in); got.Code != c.want {
			t.Errorf("Best of %d responses: %d, want %d", len(c.in), got.Code, c.want)
		}
	}

	got := b2bua.Best([]*sip.Response{res(486, `399 a "x"`), res(486, `399 a "x"`), res(480, `399 b "y"`)})
	if w := got.Header.Values("Warning"); len(w) != 2 {
		t.Errorf("Warnings %v", w)
	}
}
