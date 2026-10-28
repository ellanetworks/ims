package ua_test

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

func allFlows(sip.Flow) bool { return true }

// TS 24.229 §5.1.1.4.1: Drop ends a confirmed session at once, without a BYE.
func TestDropConfirmed(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	established(t, alice, bob)

	bob.a.Drop(allFlows)

	if e := bob.next("ended"); e.end.Cause != ua.EndDropped {
		t.Fatalf("bob ended %+v", e.end)
	}

	alice.none()
}

// Drop ends an INVITE of the peer not answered yet without a final response.
func TestDropIncoming(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	if err := r.Progress(ua.Reply{Code: 180}); err != nil {
		t.Fatal(err)
	}

	alice.next("progress")

	bob.a.Drop(allFlows)

	if e := bob.next("ended"); e.end.Cause != ua.EndDropped {
		t.Fatalf("bob ended %+v", e.end)
	}

	if err := r.Accept(sdpOf("answer-1")...); err == nil {
		t.Fatal("a dropped INVITE accepted")
	}

	alice.none()
}

// Drop ends an INVITE of the agent not answered yet without a CANCEL.
func TestDropOutgoing(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	if err := r.Progress(ua.Reply{Code: 180}); err != nil {
		t.Fatal(err)
	}

	alice.next("progress")

	alice.a.Drop(allFlows)

	got := alice.nextAll("ended", "failed")
	if e := got["ended"]; e.end.Cause != ua.EndDropped {
		t.Errorf("alice ended %+v", e.end)
	}

	if f := got["failed"]; !errors.Is(f.err, transaction.ErrDropped) {
		t.Errorf("alice failed %v", f.err)
	}

	bob.none()
}

// An INVITE that reached the agent but not its strand when Drop ran is processed no further: no session, no
// application event, no response.
func TestDropArrivingInvite(t *testing.T) {
	var busy atomic.Pointer[ua.Strand]

	arrived := make(chan struct{}, 1)

	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.StrandFor = func(*sip.Request) *ua.Strand {
			arrived <- struct{}{}
			return busy.Load()
		}
	})

	st := bob.a.NewStrand()
	busy.Store(st)

	release := make(chan struct{})

	st.Post(func() { <-release })

	alice := newRaw(t)

	invite := siptest.NewRequest("INVITE", "sip:bob@"+bob.addr.String(), sip.UDP, alice.sock.Addr())
	invite.SetBodies(sdpOf("offer")...)
	alice.sock.Send(sip.UDP, bob.addr, invite)

	<-arrived
	bob.a.Drop(allFlows)
	close(release)

	bob.none()

	// The 100 of the transaction, if sent before Drop: nothing it queued is sent after.
	for {
		r, ok := alice.sock.RecvWithin(50 * time.Millisecond)
		if !ok {
			return
		}

		if res, isRes := r.Msg.(*sip.Response); !isRes || res.StatusCode != 100 {
			t.Fatalf("got %q, want the 100 at most", r.Msg.StartLine())
		}
	}
}

// Drop waits for no strand: a handler may call it, its own session ending once it returns.
func TestDropFromAHandler(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})

	var bobAgent atomic.Pointer[ua.Agent]

	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(r *ua.Request) ua.Handler {
			bobAgent.Load().Drop(allFlows)
			return ua.Handler{}
		}
	})
	bobAgent.Store(bob.a)

	alice.invite(bob, "offer-1")

	netOf(t).Quiesce()
	alice.none()
}
