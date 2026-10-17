package ua_test

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// The handlers of two agents call each other's sessions without deadlocking: a call never waits for a handler, as
// a B2BUA bridging two agents does.
func TestHandlersOfTwoAgentsCallEachOther(t *testing.T) {
	const rounds = 200

	var (
		as, bs   atomic.Pointer[ua.Session]
		received atomic.Int32
	)

	finished := make(chan struct{})

	ping := func(other *atomic.Pointer[ua.Session]) ua.Handler {
		return ua.Handler{Request: func(_ *ua.Session, r *ua.Request) {
			_ = r.Respond(ua.Reply{Code: 200})

			if n := received.Add(1); n == rounds {
				close(finished)
				return
			} else if n > rounds {
				return
			}

			_ = other.Load().Send("INFO", ua.Change{}, nil)
		}}
	}

	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(r *ua.Request) ua.Handler {
			bs.Store(r.Session())
			_ = r.Accept(sdpOf("answer-1")...)

			return ping(&as)
		}
	})

	h := ping(&bs)
	h.Established = func(s *ua.Session, _ sip.Message) { as.Store(s) }

	if _, err := alice.a.Invite(inviteTo(alice, bob, "offer-1"), h); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(wait)
	for as.Load() == nil || bs.Load() == nil || bs.Load().State() != ua.Confirmed {
		if time.Now().After(deadline) {
			t.Fatal("no session")
		}

		time.Sleep(time.Millisecond)
	}

	// Both sides send at once, each from its handlers.
	_ = as.Load().Send("INFO", ua.Change{}, nil)
	_ = bs.Load().Send("INFO", ua.Change{}, nil)

	select {
	case <-finished:
	case <-time.After(3 * wait):
		t.Fatalf("deadlock after %d requests", received.Load())
	}
}

// A handler that panics does not stop its strand: the next events still come.
func TestHandlerPanicRecovered(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(r *ua.Request) ua.Handler {
			_ = r.Accept(sdpOf("answer-1")...)
			return ua.Handler{Established: func(*ua.Session, sip.Message) { panic("application bug") }}
		}
	})

	alice.invite(bob, "offer-1")

	as := alice.next("established").s
	alice.next("answer")

	_ = as.Bye()

	alice.next("ended")
}

// Post and AfterFunc run on the strand, in order with the handlers, on the clock of the agent.
func TestStrandPostAndAfterFunc(t *testing.T) {
	clock := siptest.NewClock()
	alice := newPeer(t, "alice", clock, ua.SessionTimer{})
	st := alice.a.NewStrand()

	got := make(chan int, 4)

	for i := range 3 {
		st.Post(func() { got <- i })
	}

	tm := st.AfterFunc(10*time.Second, func() { got <- 9 })
	stopped := st.AfterFunc(5*time.Second, func() { got <- -1 })

	if !stopped.Stop() {
		t.Error("Stop of a pending timer reported false")
	}

	for i := range 3 {
		if v := <-got; v != i {
			t.Fatalf("job %d ran as %d", i, v)
		}
	}

	advance(clock, 11*time.Second, alice)

	if v := <-got; v != 9 {
		t.Fatalf("timer ran %d", v)
	}

	if tm.Stop() {
		t.Error("Stop of a fired timer reported true")
	}
}

// The legs of a call on one strand: the outgoing INVITE of a B2BUA on the strand of the incoming one.
func TestInviteOnStrand(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	carol := newPeer(t, "carol", nil, ua.SessionTimer{})

	strands := make(chan *ua.Strand, 2)

	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(r *ua.Request) ua.Handler {
			strands <- r.Strand()
			return ua.Handler{}
		}
	})

	alice.invite(bob, "offer-1")

	st := <-strands

	o, err := bob.a.Invite(inviteTo(bob, carol, "offer-1"), ua.Handler{}, ua.On(st))
	if err != nil {
		t.Fatal(err)
	}

	if o.Strand() != st {
		t.Error("the outgoing INVITE is on another strand")
	}

	carol.next("invite")
}

// An agent on a shared transaction layer: the dispatcher gives it the requests it owns.
func TestSharedLayer(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})

	d := &dispatcher{}
	layer := transaction.New(transaction.Config{Handler: d})
	addr := freePort(t)

	bob := &peer{t: t, name: "bob", addr: addr, events: make(chan event, 64), tr: sip.UDP}

	a, err := ua.New(ua.Config{
		Layer:   layer,
		Prepare: bob.prepare,
		Contact: sip.Address{URI: sip.URI{Scheme: "sip", User: "bob", Host: addr.Addr().String(), Port: addr.Port()}},
		OnInvite: func(r *ua.Request) ua.Handler {
			_ = r.Accept(sdpOf("answer-1")...)
			return bob.handler()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	bob.a = a
	d.a = a

	// The layer listens once the dispatcher has the agent.
	if _, err := layer.Listen(t.Context(), addr); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		_ = a.Close(ctx)
		_ = layer.Close()
	})

	alice.invite(bob, "offer-1")

	as := alice.next("established").s
	alice.next("answer")
	bob.next("established")

	done, f := results()

	if err := as.Send("INFO", ua.Change{}, f); err != nil {
		t.Fatal(err)
	}

	_ = bob.next("request").req.Respond(ua.Reply{Code: 200})

	if res := recv(t, done); res.code() != 200 {
		t.Fatalf("INFO %d", res.code())
	}

	// A request outside the agent's dialogs goes to the dispatcher's other users.
	uri, _ := sip.ParseURI(bob.uri())
	options := sip.NewRequest("OPTIONS", uri)
	options.Header.Add("From", "<"+alice.uri()+">")
	options.Header.Add("To", "<"+bob.uri()+">")

	if err := alice.a.Request(options, f); err != nil {
		t.Fatal(err)
	}

	if res := recv(t, done); res.code() != 404 {
		t.Errorf("OPTIONS %d, want the dispatcher's 404", res.code())
	}

	// Closing the agent leaves the layer to its owner.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_ = a.Close(ctx)

	if _, err := layer.Listen(t.Context(), netip.AddrPortFrom(loopback, 0)); err != nil {
		t.Errorf("layer closed with the agent: %v", err)
	}
}

// dispatcher shares a layer between an agent, for INVITEs and its dialogs, and a stand-in for other users.
type dispatcher struct {
	a *ua.Agent
}

func (d *dispatcher) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	if req.Method == "INVITE" || d.a.Owns(req) {
		d.a.HandleRequest(tx, req)
		return
	}

	_ = tx.Respond(sip.NewResponse(req, 404, ""))
}

func (d *dispatcher) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	d.a.HandleCancel(tx, cancel)
}

func (d *dispatcher) HandleAck(ack *sip.Request) { d.a.HandleAck(ack) }

func (d *dispatcher) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	d.a.HandleTransactionError(tx, err)
}

func inviteTo(from, to *peer, sdp string) *sip.Request {
	uri, _ := sip.ParseURI(to.uri())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+from.uri()+">")
	req.Header.Add("To", "<"+to.uri()+">")
	req.SetBodies(ua.SDP([]byte(sdp)))

	return req
}
