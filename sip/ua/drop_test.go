package ua_test

import (
	"errors"
	"testing"

	"github.com/ellanetworks/ims/sip"
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
