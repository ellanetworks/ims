package b2bua_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/ua"
)

type exchanges struct {
	mu  sync.Mutex
	got [][2]string
}

func (e *exchanges) negotiated(x *b2bua.Exchange, answer []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.got = append(e.got, [2]string{string(x.Offer), string(answer)})
}

func (e *exchanges) all() [][2]string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return slices.Clone(e.got)
}

// RFC 3264 section 6
// RFC 3262 section 5
func TestRelayNegotiated(t *testing.T) {
	var e exchanges

	alice, bob, b, _, fork, _, a2 := forkedPartsWith(t, b2bua.RelayConfig{Negotiated: e.negotiated})

	_ = fork.Accept()

	alice.nextAll("progress", "ended", "established")
	bob.nextAll("ended", "established")

	if got, want := e.all(), [][2]string{{"offer-1", "answer-2"}}; !slices.Equal(got, want) {
		t.Fatalf("after the 2xx of the fork: %v, want %v", got, want)
	}

	if err := a2.Reinvite(context.Background(), ua.Change{Bodies: sdpOf("offer-2")}, nil); err != nil {
		t.Fatal(err)
	}

	_ = bob.next("request").req.Respond(ua.Reply{Code: 200, Bodies: sdpOf("answer-3")})

	alice.next("answer")
	b.net.Quiesce()

	if got, want := e.all(), [][2]string{{"offer-1", "answer-2"}, {"offer-2", "answer-3"}}; !slices.Equal(got, want) {
		t.Fatalf("after the re-INVITE: %v, want %v", got, want)
	}
}

// RFC 3264 section 6
func TestRelayNegotiatedRejected(t *testing.T) {
	var e exchanges

	alice, bob, b := newCall(t, b2bua.RelayConfig{Negotiated: e.negotiated})
	alice.invite(b.uri(), "offer-1", alice.handler())
	as, _ := bridged(t, alice, bob)

	if err := as.Reinvite(context.Background(), ua.Change{Bodies: sdpOf("offer-2")}, nil); err != nil {
		t.Fatal(err)
	}

	_ = bob.next("request").req.Reject(488)

	b.net.Quiesce()

	if got, want := e.all(), [][2]string{{"offer-1", "answer-1"}}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
