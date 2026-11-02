package ua_test

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

func TestHandlersOfTwoAgentsCallEachOther(t *testing.T) {
	const rounds = 200

	var (
		as, bs   atomic.Pointer[ua.Session]
		received atomic.Int32
	)

	finished := make(chan struct{})

	ping := func(other *atomic.Pointer[ua.Session]) ua.Handler {
		return ua.Handler{Request: func(_ context.Context, _ *ua.Session, r *ua.Request) {
			_ = r.Respond(ua.Reply{Code: 200})

			if n := received.Add(1); n == rounds {
				close(finished)
				return
			} else if n > rounds {
				return
			}

			_ = other.Load().Send(context.Background(), "INFO", ua.Change{}, nil)
		}}
	}

	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(_ context.Context, r *ua.Request) ua.Handler {
			bs.Store(r.Session())
			_ = r.Accept(sdpOf("answer-1")...)

			return ping(&as)
		}
	})

	h := ping(&bs)
	h.Established = func(_ context.Context, s *ua.Session, _ sip.Message) { as.Store(s) }

	if _, err := alice.a.Invite(context.Background(), inviteTo(alice, bob, "offer-1"), h); err != nil {
		t.Fatal(err)
	}

	netOf(t).Quiesce()

	if as.Load() == nil || bs.Load() == nil || bs.Load().State() != ua.Confirmed {
		t.Fatal("no session")
	}

	_ = as.Load().Send(context.Background(), "INFO", ua.Change{}, nil)
	_ = bs.Load().Send(context.Background(), "INFO", ua.Change{}, nil)

	netOf(t).Quiesce()

	select {
	case <-finished:
	default:
		t.Fatalf("deadlock after %d requests", received.Load())
	}
}

func TestHandlerPanicRecovered(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(_ context.Context, r *ua.Request) ua.Handler {
			_ = r.Accept(sdpOf("answer-1")...)
			return ua.Handler{Established: func(context.Context, *ua.Session, sip.Message) { panic("application bug") }}
		}
	})

	alice.invite(bob, "offer-1")

	as := alice.next("established").s
	alice.next("answer")

	_ = as.Bye(context.Background(), ua.Change{}, nil)

	alice.next("ended")
}

func TestStrandPostAndAfterFunc(t *testing.T) {
	clock := netOf(t).Clock()
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

func TestInviteOnStrand(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	carol := newPeer(t, "carol", nil, ua.SessionTimer{})

	strands := make(chan *ua.Strand, 2)

	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(_ context.Context, r *ua.Request) ua.Handler {
			strands <- r.Strand()
			return ua.Handler{}
		}
	})

	alice.invite(bob, "offer-1")

	st := <-strands

	o, err := bob.a.Invite(context.Background(), inviteTo(bob, carol, "offer-1"), ua.Handler{}, ua.On(st))
	if err != nil {
		t.Fatal(err)
	}

	if o.Strand() != st {
		t.Error("the outgoing INVITE is on another strand")
	}

	carol.next("invite")
}

func TestSharedLayer(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})

	d := &dispatcher{}
	n := netOf(t)
	layer := transaction.New(n.Config(transaction.Config{Handler: d}))
	addr := n.Addr()

	bob := &peer{t: t, name: "bob", addr: addr, events: make(chan event, 64), tr: sip.UDP}

	a, err := ua.New(ua.Config{
		Layer:   layer,
		Allow:   append(slices.Clone(ua.Methods), "INFO"),
		Prepare: bob.prepare,
		Contact: sip.Address{URI: sip.URI{Scheme: "sip", User: "bob", Host: addr.Addr().String(), Port: addr.Port()}},
		OnInvite: func(_ context.Context, r *ua.Request) ua.Handler {
			_ = r.Accept(sdpOf("answer-1")...)
			return bob.handler()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	bob.a = a
	d.a = a

	if err := layer.Serve(addr); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		_ = a.Shutdown(ctx)
		_ = layer.Close()
	})

	alice.invite(bob, "offer-1")

	as := alice.next("established").s
	alice.next("answer")
	bob.next("established")

	done, f := results()

	if err := as.Send(context.Background(), "INFO", ua.Change{}, f); err != nil {
		t.Fatal(err)
	}

	_ = bob.next("request").req.Respond(ua.Reply{Code: 200})

	if res := recv(t, done); res.code() != 200 {
		t.Fatalf("INFO %d", res.code())
	}

	uri, _ := sip.ParseURI(bob.uri())
	options := sip.NewRequest("OPTIONS", uri)
	options.Header.Add("From", "<"+alice.uri()+">")
	options.Header.Add("To", "<"+bob.uri()+">")

	if err := alice.a.Request(context.Background(), options, f); err != nil {
		t.Fatal(err)
	}

	if res := recv(t, done); res.code() != 404 {
		t.Errorf("OPTIONS %d, want the dispatcher's 404", res.code())
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_ = a.Shutdown(ctx)

	if err := layer.Serve(n.Addr()); err != nil {
		t.Errorf("layer closed with the agent: %v", err)
	}
}

type dispatcher struct {
	a *ua.Agent
}

func (d *dispatcher) HandleRequest(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	if req.Method == "INVITE" || d.a.Owns(req) {
		d.a.HandleRequest(ctx, tx, req)
		return
	}

	_ = tx.Respond(sip.NewResponse(req, 404, ""))
}

func (d *dispatcher) HandleCancel(ctx context.Context, tx *transaction.ServerTransaction, cancel *sip.Request) {
	d.a.HandleCancel(ctx, tx, cancel)
}

func (d *dispatcher) HandleAck(ctx context.Context, ack *sip.Request) { d.a.HandleAck(ctx, ack) }

func (d *dispatcher) HandleTransactionError(ctx context.Context, tx *transaction.ServerTransaction, err error) {
	d.a.HandleTransactionError(ctx, tx, err)
}

func inviteTo(from, to *peer, sdp string) *sip.Request {
	uri, _ := sip.ParseURI(to.uri())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+from.uri()+">")
	req.Header.Add("To", "<"+to.uri()+">")
	req.SetBodies(ua.SDP([]byte(sdp)))

	return req
}
