package b2bua_test

import (
	"testing"

	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/ua"
)

// forkedCall has bob answer the INVITE of the B2BUA in two early dialogs, each with its answer in a reliable 183; it
// returns bob's two requests and alice's early sessions answered with each.
func forkedCall(t *testing.T) (alice, bob *party, first, fork *ua.Request, a1, a2 *ua.Session) {
	f := forkedCallOf(t)

	return f.alice, f.bob, f.first, f.fork, f.a1, f.a2
}

// forked is a call through b whose callee answered in two early dialogs.
type forked struct {
	alice, bob  *party
	b           *b2b
	first, fork *ua.Request
	a1, a2      *ua.Session
}

func forkedCallOf(t *testing.T) (f forked) {
	alice, bob, b, first, fork, a1, a2 := forkedParts(t)

	return forked{alice: alice, bob: bob, b: b, first: first, fork: fork, a1: a1, a2: a2}
}

func forkedParts(t *testing.T) (alice, bob *party, b *b2b, first, fork *ua.Request, a1, a2 *ua.Session) {
	t.Helper()

	alice, bob, b = newCall(t, b2bua.RelayConfig{})
	alice.invite(b.uri(), "offer-1", alice.handler())

	first = bob.next("invite").req

	fork, err := first.Fork(bob.handler())
	if err != nil {
		t.Fatal(err)
	}

	_ = first.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")})
	_ = fork.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-2")})

	// The answer of each early dialog, and its 183 once PRACKed: the dialogs' events interleave.
	answers := map[string]*ua.Session{}
	progress := 0

	alice.net.Quiesce()

	for range 4 {
		select {
		case e := <-alice.events:
			switch e.kind {
			case "answer":
				answers[string(e.sdp)] = e.s
			case "progress":
				progress++
			default:
				t.Fatalf("alice: %s event", e.kind)
			}
		default:
			t.Fatalf("alice: answers %v, %d 183s", answers, progress)
		}
	}

	a1, a2 = answers["answer-1"], answers["answer-2"]
	if a1 == nil || a2 == nil || a1 == a2 {
		t.Fatalf("alice's early dialogs %v", answers)
	}

	return alice, bob, b, first, fork, a1, a2
}

// RFC 3261 §13.2.2.4: each early dialog of bob is mirrored in one of alice's, with its own answer; the 2xx of one
// confirms its mirror, and ends the other one with a 199 (RFC 6228).
func TestBridgeMirrorsForks(t *testing.T) {
	f := forkedCallOf(t)
	alice, bob, fork, a2 := f.alice, f.bob, f.fork, f.a2

	_ = fork.Accept()

	got := alice.nextAll("progress", "ended", "established")
	if got["established"].s != a2 {
		t.Error("the 2xx of the fork confirmed another dialog")
	}

	if res := got["progress"].res; res.StatusCode != 199 {
		t.Errorf("alice got a %d", res.StatusCode)
	}

	bob.nextAll("ended", "established")

	_ = a2.Bye(ua.Change{}, nil)

	alice.next("ended")
	bob.next("ended")
	settled(t, f.b.net, alice, bob, f.b.party)
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
