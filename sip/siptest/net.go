package siptest

import (
	"context"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

type Net struct {
	t       testing.TB
	clock   *Clock
	sched   *Scheduler
	network *transaction.Network
	peers   *transaction.Layer

	mu   sync.Mutex
	next uint32
}

func NewNet(t testing.TB) *Net {
	n := &Net{t: t, clock: NewClock(), sched: &Scheduler{}, network: transaction.NewClosedNetwork()}
	n.sched.cond.L = &n.sched.mu
	n.clock.settle = n.Quiesce

	n.peers = transaction.New(n.Config(transaction.Config{Handler: noHandler{}}))

	t.Cleanup(func() {
		_ = n.peers.Close()
	})

	return n
}

func (n *Net) Clock() *Clock {
	return n.clock
}

func (n *Net) Scheduler() *Scheduler {
	return n.sched
}

func (n *Net) Config(cfg transaction.Config) transaction.Config {
	cfg.Clock = n.clock
	cfg.Scheduler = n.sched
	cfg.Network = n.network

	return cfg
}

func (n *Net) Addr() netip.AddrPort {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.next++
	if n.next >= 1<<24-1 {
		panic("siptest: no address left on the net")
	}

	h := n.next

	return netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, byte(h >> 16), byte(h >> 8), byte(h)}), sip.DefaultPort)
}

func (n *Net) Serve(l *transaction.Layer) netip.AddrPort {
	n.t.Helper()

	addr := n.Addr()
	if err := l.Serve(addr); err != nil {
		n.t.Fatalf("siptest: serve %s: %v", addr, err)
	}

	return addr
}

func (n *Net) Peer() *Peer {
	n.t.Helper()

	p := NewPeer(n.t, n.peers, n.Addr())
	p.net = n

	return p
}

func (n *Net) Quiesce() {
	n.t.Helper()

	if !n.sched.wait(Timeout) {
		n.t.Fatalf("siptest: %d tasks still running after %s:\n%s", n.sched.Running(), Timeout, stacks(true))
	}
}

func (n *Net) Await(f func(), limit time.Duration) {
	n.t.Helper()

	const idle = 5 * time.Millisecond

	done := make(chan struct{})

	go func() {
		defer close(done)

		f()
	}()

	start := n.clock.Elapsed()

	for {
		n.Quiesce()

		select {
		case <-done:
			return
		case <-n.sched.started():
			continue
		case <-time.After(idle):
		}

		d, ok := n.clock.Next()
		if !ok || n.clock.Elapsed()+d-start > limit {
			break
		}

		n.clock.Advance(d)
	}

	select {
	case <-done:
	case <-time.After(Timeout):
		n.t.Fatalf("siptest: a call still waiting after %s of timers and %s:\n%s", limit, Timeout, stacks(false))
	}
}

func (n *Net) Advance(d time.Duration) {
	n.clock.Advance(d)
}

func stacks(tasks bool) string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]

	var out []string

	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if !tasks || strings.Contains(g, "siptest.(*Scheduler).Go") {
			out = append(out, g)
		}
	}

	return strings.Join(out, "\n\n")
}

type Scheduler struct {
	mu    sync.Mutex
	cond  sync.Cond
	tasks int

	start chan struct{}
}

func (s *Scheduler) Go(f func()) {
	s.mu.Lock()
	s.tasks++

	if s.start != nil {
		close(s.start)
		s.start = nil
	}

	s.mu.Unlock()

	go func() {
		defer s.done()

		f()
	}()
}

func (s *Scheduler) started() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.start == nil {
		s.start = make(chan struct{})
	}

	return s.start
}

func (s *Scheduler) Running() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.tasks
}

func (s *Scheduler) done() {
	s.mu.Lock()
	s.tasks--

	if s.tasks == 0 {
		s.cond.Broadcast()
	}

	s.mu.Unlock()
}

func (s *Scheduler) wait(d time.Duration) bool {
	expired := false

	t := time.AfterFunc(d, func() {
		s.mu.Lock()
		expired = true

		s.cond.Broadcast()
		s.mu.Unlock()
	})
	defer t.Stop()

	s.mu.Lock()
	defer s.mu.Unlock()

	for s.tasks > 0 && !expired {
		s.cond.Wait()
	}

	return s.tasks == 0
}

type noHandler struct{}

func (noHandler) HandleRequest(context.Context, *transaction.ServerTransaction, *sip.Request) {}

func (noHandler) HandleCancel(context.Context, *transaction.ServerTransaction, *sip.Request) {}

func (noHandler) HandleAck(context.Context, *sip.Request) {}

func (noHandler) HandleTransactionError(context.Context, *transaction.ServerTransaction, error) {}
