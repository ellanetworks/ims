package b2bua_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

var errOverload = errors.New("no ports")

type overloaded struct {
	mu     sync.Mutex
	offers int
	after  int
}

func (m *overloaded) Offer(x *b2bua.Exchange) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.offers++
	if m.offers > m.after {
		return nil, &b2bua.Rejection{Reply: ua.Reply{Code: 503}, Err: errOverload}
	}

	return x.Offer, nil
}

func (m *overloaded) Answer(_ *b2bua.Exchange, _ *ua.Session, sdp []byte) ([]byte, error) {
	return sdp, nil
}

func (m *overloaded) End(*b2bua.Exchange, *ua.Session) {}

func (m *overloaded) Dialog(*ua.Session, ua.State) {}

func TestMediaRejectionCode(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{Media: &overloaded{}})
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	failedWith(t, alice, 503)
	b.callEnded(t)
	bob.none()
	settled(t, n, b.party, bob)
}

func TestNoFailoverOnMediaRejection(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	carol := newParty(t, n, "carol", nil)
	b := newSerial(t, n, b2bua.RelayConfig{Media: &overloaded{after: 1}}, bob.uri(), carol.uri())
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	_ = bob.next("invite").req.Reject(503)

	b.failure(t)
	failedWith(t, alice, 503)
	b.noFailure(t)
	carol.none()
	b.callEnded(t)
	settled(t, n, alice, bob, carol, b.party)
}

func TestFailoverBudget(t *testing.T) {
	for _, c := range []struct {
		name   string
		budget time.Duration
		next   bool
	}{
		{"within", 64*transaction.DefaultT1 + 10*time.Second, true},
		{"spent", 10 * time.Second, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			n := newNet(t)
			carol := newParty(t, n, "carol", nil)
			b := newSerial(t, n, b2bua.RelayConfig{FailoverBudget: c.budget}, deadUDP(n), carol.uri())
			alice := newParty(t, n, "alice", nil)

			alice.invite(b.uri(), "offer-1", alice.handler())

			n.Advance(64*transaction.DefaultT1 + 1)

			if !c.next {
				failedWith(t, alice, 408)
				b.noFailure(t)
				carol.none()
				b.callEnded(t)
				settled(t, n, alice, carol, b.party)

				return
			}

			b.failure(t)

			_ = carol.next("invite").req.Reject(486)

			b.failure(t)
			failedWith(t, alice, 486)
			b.callEnded(t)
			settled(t, n, alice, carol, b.party)
		})
	}
}
