package b2bua_test

import (
	"testing"

	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/ua"
)

// forkedCall has bob answer the INVITE of the B2BUA in two early dialogs, each with its answer in a reliable 183; it
// returns bob's two requests and alice's early sessions answered with each.
func forkedCall(t *testing.T) (alice, bob *party, first, fork *ua.Request, a1, a2 *ua.Session) {
	t.Helper()

	alice, bob, b := newCall(t, b2bua.RelayConfig{})
	alice.invite(b.uri(), "offer-1", alice.handler())

	first = bob.next("invite").req

	fork, err := first.Fork(bob.handler())
	if err != nil {
		t.Fatal(err)
	}

	_ = first.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")})
	_ = fork.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-2")})

	answers := map[string]*ua.Session{}

	for range 2 {
		e := alice.next("answer")
		answers[string(e.sdp)] = e.s
	}

	alice.next("progress")
	alice.next("progress")

	a1, a2 = answers["answer-1"], answers["answer-2"]
	if a1 == nil || a2 == nil || a1 == a2 {
		t.Fatalf("alice's early dialogs %v", answers)
	}

	return alice, bob, first, fork, a1, a2
}

// RFC 3261 §13.2.2.4: each early dialog of bob is mirrored in one of alice's, with its own answer; the 2xx of one
// confirms its mirror, and ends the other one with a 199 (RFC 6228).
func TestBridgeMirrorsForks(t *testing.T) {
	alice, bob, _, fork, _, a2 := forkedCall(t)

	_ = fork.Accept()

	got := alice.nextAll("progress", "ended", "established")
	if got["established"].s != a2 {
		t.Error("the 2xx of the fork confirmed another dialog")
	}

	if res := got["progress"].res; res.StatusCode != 199 {
		t.Errorf("alice got a %d", res.StatusCode)
	}

	bob.nextAll("ended", "established")
}

// An early dialog bob ends alone ends its mirror; the other goes on.
func TestBridgeDroppedFork(t *testing.T) {
	alice, bob, first, fork, a1, a2 := forkedCall(t)

	_ = first.Session().Bye(ua.Change{}, nil)

	bob.next("ended")

	got := alice.nextAll("progress", "ended")
	if got["progress"].res.StatusCode != 199 || got["ended"].s != a1 {
		t.Fatalf("alice got a %d, ended %v", got["progress"].res.StatusCode, got["ended"].s == a1)
	}

	_ = fork.Accept()

	if e := alice.next("established"); e.s != a2 {
		t.Error("another dialog confirmed")
	}

	bob.next("established")
}
