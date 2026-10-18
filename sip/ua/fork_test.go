package ua_test

import (
	"testing"

	"github.com/ellanetworks/ims/sip/ua"
)

// forked has bob answer alice's INVITE in two early dialogs, the first and a fork, each with a 180; it returns bob's
// requests and alice's two early sessions, in that order.
func forked(t *testing.T, alice, bob *peer) (first, fork *ua.Request, a1, a2 *ua.Session) {
	t.Helper()

	alice.invite(bob, "offer-1")

	first = bob.next("invite").req

	fork, err := first.Fork(bob.handler())
	if err != nil {
		t.Fatal(err)
	}

	_ = first.Progress(ua.Reply{Code: 180})
	_ = fork.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-f")})

	e := alice.nextAll("progress", "answer")
	a1 = e["progress"].s

	// The reliable 183, reported once PRACKed.
	a2 = alice.next("progress").s

	if a1 == a2 || a1.ID().RemoteTag == a2.ID().RemoteTag {
		t.Fatal("one early dialog for two To tags")
	}

	return first, fork, a1, a2
}

// RFC 3261 §12.1.1, RFC 6228: a fork of an INVITE is an early dialog of its own; its 2xx confirms it, and ends the
// other one with a 199.
func TestForkAnswered(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	first, fork, _, a2 := forked(t, alice, bob)

	if err := fork.Accept(); err != nil {
		t.Fatal(err)
	}

	// The 199 of the other dialog, reported, then its end.
	got := alice.nextAll("progress", "ended", "established")
	if got["progress"].res.StatusCode != 199 {
		t.Errorf("alice got a %d", got["progress"].res.StatusCode)
	}

	if got["established"].s != a2 {
		t.Error("the 2xx confirmed the other early dialog")
	}

	if e := got["ended"].end; e.Cause != ua.EndNotChosen || e.Response == nil || e.Response.StatusCode != 199 {
		t.Errorf("alice's other early dialog ended %+v", e)
	}

	ends := bob.nextAll("ended", "established")
	if ends["ended"].s != first.Session() || ends["ended"].end.Cause != ua.EndNotChosen {
		t.Errorf("bob's first dialog ended %+v", ends["ended"].end)
	}

	if ends["established"].s != fork.Session() {
		t.Error("bob's fork not confirmed")
	}

	if err := first.Respond(ua.Reply{Code: 200}); err == nil {
		t.Error("a second final response")
	}
}

// RFC 3261 §9.2: a CANCEL ends every early dialog of the INVITE, answered once with a 487.
func TestForkCancelled(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	o := alice.invite(bob, "offer-1")

	first := bob.next("invite").req
	fork, _ := first.Fork(bob.handler())
	_ = first.Progress(ua.Reply{Code: 180})
	_ = fork.Progress(ua.Reply{Code: 180})

	alice.nextAll("progress", "progress")

	_ = o.Cancel(ua.Change{})

	for range 2 {
		if e := bob.next("ended"); e.end.Cause != ua.EndCancelled {
			t.Errorf("bob ended %+v", e.end)
		}
	}

	if e := alice.next("failed"); e.res == nil || e.res.StatusCode != 487 {
		t.Fatalf("alice's INVITE %+v", e.res)
	}
}

// RFC 3261 §15: a BYE in one early dialog ends it alone; the INVITE goes on in the other, then fails with a 487
// once the last one ends.
func TestForkEarlyBye(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	first, fork, a1, _ := forked(t, alice, bob)

	if err := a1.Bye(ua.Change{}, nil); err != nil {
		t.Fatal(err)
	}

	if e := bob.next("ended"); e.s != first.Session() || e.end.Cause != ua.EndBye {
		t.Fatalf("bob ended %+v", e.end)
	}

	alice.next("ended")
	alice.none()

	if err := fork.Accept(); err != nil {
		t.Fatalf("the fork after the BYE of the other: %v", err)
	}

	alice.next("established")
	bob.next("established")
}

// A failure of one member answers the INVITE: every early dialog ends with it.
func TestForkRejected(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	first, fork, _, _ := forked(t, alice, bob)

	if err := fork.Reject(486); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if e := bob.next("ended"); e.end.Cause != ua.EndRejected {
			t.Errorf("bob ended %+v", e.end)
		}
	}

	if err := first.Progress(ua.Reply{Code: 180}); err == nil {
		t.Error("a 1xx after the final response")
	}

	got := alice.nextAll("failed", "ended", "ended")
	if res := got["failed"].res; res == nil || res.StatusCode != 486 {
		t.Fatalf("alice's INVITE %+v", res)
	}
}

// RFC 6228: Bye on one early dialog of a forked INVITE ends it alone, with a 199; the other goes on.
func TestForkDropped(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	first, fork, a1, _ := forked(t, alice, bob)

	if err := first.Session().Bye(ua.Change{}, nil); err != nil {
		t.Fatal(err)
	}

	bob.next("ended")

	got := alice.nextAll("progress", "ended")
	if got["progress"].res.StatusCode != 199 || got["ended"].s != a1 {
		t.Fatalf("alice got %d, ended %v", got["progress"].res.StatusCode, got["ended"].s == a1)
	}

	_ = fork.Accept()

	alice.next("established")
	bob.next("established")
}
