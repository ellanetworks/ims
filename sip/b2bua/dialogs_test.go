package b2bua_test

import (
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/ua"
)

type dialogs struct {
	mu        sync.Mutex
	offers    int
	answered  map[string]*ua.Session
	confirmed []*ua.Session
	ended     []*ua.Session
}

func (m *dialogs) Offer(x *b2bua.Exchange) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.offers++

	return []byte(string(x.Offer) + "@" + strconv.Itoa(m.offers)), nil
}

func (m *dialogs) Answer(_ *b2bua.Exchange, d *ua.Session, sdp []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.answered == nil {
		m.answered = map[string]*ua.Session{}
	}

	m.answered[string(sdp)] = d

	return sdp, nil
}

func (m *dialogs) End(*b2bua.Exchange, *ua.Session) {}

func (m *dialogs) Dialog(d *ua.Session, state ua.State) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch state {
	case ua.Confirmed:
		m.confirmed = append(m.confirmed, d)
	case ua.Terminated:
		m.ended = append(m.ended, d)
	}
}

// RFC 3263 section 4.3
func TestFailoverOffersAgain(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	carol := newParty(t, n, "carol", nil)
	m := &dialogs{}
	b := newSerial(t, n, b2bua.RelayConfig{Media: m}, bob.uri(), carol.uri())
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	in := bob.next("invite").req
	if !hasSDP(in.Message(), "offer-1@1") {
		t.Fatalf("bob's INVITE offer %q", in.Message().Body)
	}

	_ = in.Reject(503)

	b.failure(t)

	if in := carol.next("invite").req; !hasSDP(in.Message(), "offer-1@2") {
		t.Fatalf("carol's INVITE offer %q, want the offer made again for her", in.Message().Body)
	}
}

// RFC 3261 section 13.2.2.4
// RFC 3264 section 8
func TestMediaDialogOfForks(t *testing.T) {
	m := &dialogs{}
	alice, bob, b := newCall(t, b2bua.RelayConfig{Media: m})

	alice.invite(b.uri(), "offer-1", alice.handler())

	first := bob.next("invite").req

	fork, err := first.Fork(bob.handler())
	if err != nil {
		t.Fatal(err)
	}

	_ = first.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")})
	_ = fork.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-2")})

	alice.net.Quiesce()

	_ = first.Accept()

	alice.net.Quiesce()

	m.mu.Lock()
	defer m.mu.Unlock()

	d1, d2 := m.answered["answer-1"], m.answered["answer-2"]

	switch {
	case d1 == nil || d2 == nil || d1 == d2:
		t.Fatalf("answers of the forks in dialogs %v", m.answered)
	case fmt.Sprint(m.confirmed) != fmt.Sprint([]*ua.Session{d1}):
		t.Errorf("confirmed dialogs %v, want the first fork's %v", m.confirmed, d1)
	}
}
