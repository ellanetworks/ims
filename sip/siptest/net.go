package siptest

import (
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// Net is a network in memory for tests of agents and proxies: the layers on it carry their messages in memory, run
// their timers on one fake clock, and start their tasks, the strands of their agents included, on a scheduler that
// knows when none is left. A test acts, then Quiesce or Advance, then checks: what the stack does in reaction is done
// by then, without sleeps.
//
// Addresses are handed out by Addr. A message to an address not on the net is lost over UDP and refused over TCP.
type Net struct {
	t       testing.TB
	clock   *Clock
	sched   *Scheduler
	network *transaction.Network
	peers   *transaction.Layer

	mu   sync.Mutex
	next uint32
}

// NewNet makes a net, closed when the test ends.
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

// Clock is the clock of the net. Its Advance runs the timers due in order, each once the tasks the previous started
// are done.
func (n *Net) Clock() *Clock {
	return n.clock
}

// Scheduler is the scheduler of the net.
func (n *Net) Scheduler() *Scheduler {
	return n.sched
}

// Config is cfg with the clock, the scheduler and the network of the net.
func (n *Net) Config(cfg transaction.Config) transaction.Config {
	cfg.Clock = n.clock
	cfg.Scheduler = n.sched
	cfg.Network = n.network

	return cfg
}

// Addr is a new address on the net: a host of 10.0.0.0/8, port 5060.
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

// Serve serves a new address of the net on l, and returns it.
func (n *Net) Serve(l *transaction.Layer) netip.AddrPort {
	n.t.Helper()

	addr := n.Addr()
	if err := l.Serve(addr); err != nil {
		n.t.Fatalf("siptest: serve %s: %v", addr, err)
	}

	return addr
}

// Peer is a peer on a new address of the net, that sends and receives messages itself.
func (n *Net) Peer() *Peer {
	n.t.Helper()

	p := NewPeer(n.t, n.peers, n.Addr())
	p.net = n

	return p
}

// Quiesce waits until no task of the net is left: every message sent so far delivered and handled, every handler
// made due run. Timers wait for Advance. It fails the test if tasks still run after Timeout, a task blocked.
// Called from the test, not from a handler.
func (n *Net) Quiesce() {
	n.t.Helper()

	if !n.sched.wait(Timeout) {
		n.t.Fatalf("siptest: %d tasks still running after %s:\n%s", n.sched.Running(), Timeout, stacks(true))
	}
}

// Await runs f, a call that waits for the stack, such as closing an agent or a layer, and returns once it did: while
// f waits, the clock advances from timer to timer, as far as limit, the timeouts f waits for expiring in turn. f runs on
// a goroutine of its own, not a task: the clock advances once f started no task for idle, a few milliseconds. Await
// is for shutting down; a test checks behaviour with Advance.
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

// Advance advances the clock of the net by d: see Clock.
func (n *Net) Advance(d time.Duration) {
	n.clock.Advance(d)
}

// stacks are the stacks of the goroutines, only those of the tasks of schedulers if tasks: to show which blocked.
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

// Scheduler is a transaction.Scheduler that counts its tasks, each on a goroutine of its own.
type Scheduler struct {
	mu    sync.Mutex
	cond  sync.Cond
	tasks int

	// start, while not nil, closes as the next task starts.
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

// started closes as the next task starts.
func (s *Scheduler) started() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.start == nil {
		s.start = make(chan struct{})
	}

	return s.start
}

// Running is the number of tasks not done.
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

// wait waits until no task is left, for d at most.
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

// noHandler is the handler of the layer of the peers of a net, which take their messages themselves.
type noHandler struct{}

func (noHandler) HandleRequest(*transaction.ServerTransaction, *sip.Request) {}

func (noHandler) HandleCancel(*transaction.ServerTransaction, *sip.Request) {}

func (noHandler) HandleAck(*sip.Request) {}

func (noHandler) HandleTransactionError(*transaction.ServerTransaction, error) {}
