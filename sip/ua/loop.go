package ua

import (
	"bytes"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// checkLoop enables the assertions that the state of an agent is used on its loop only. Tests enable it.
var checkLoop = false

// loop is the event loop of an agent (see docs/sip-ua-design.md): one goroutine owns the dialogs, sessions, outgoing
// INVITEs and timers of the agent, and runs the handlers of the application. Everything else posts functions to it:
// the transaction layer, timers, calls of the application from other goroutines. Posting never blocks.
type loop struct {
	mu   sync.Mutex
	q    []func()
	wake chan struct{}
	done chan struct{}
	gid  atomic.Uint64

	// The fields below belong to the loop.

	// events are the handlers due once the current job ends, in order: a handler runs after the state transitions
	// of the job, and may call the agent.
	events []func()
	exit   bool
}

func newLoop() *loop {
	l := &loop{wake: make(chan struct{}, 1), done: make(chan struct{})}
	go l.run()

	return l
}

func (l *loop) post(f func()) {
	l.mu.Lock()
	l.q = append(l.q, f)
	l.mu.Unlock()

	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *loop) run() {
	defer close(l.done)

	l.gid.Store(goroutineID())

	for range l.wake {
		for {
			l.mu.Lock()

			if len(l.q) == 0 {
				l.mu.Unlock()
				break
			}

			f := l.q[0]
			l.q[0] = nil
			l.q = l.q[1:]
			l.mu.Unlock()

			l.job(f)

			if l.exit {
				return
			}
		}
	}
}

// job runs f, then the handlers it made due.
func (l *loop) job(f func()) {
	f()

	for len(l.events) > 0 {
		e := l.events[0]
		l.events[0] = nil
		l.events = l.events[1:]
		e()
	}
}

// emit makes a handler due once the current job ends.
func (l *loop) emit(f func()) {
	l.check()
	l.events = append(l.events, f)
}

// stop ends the loop once the jobs posted before are done, and waits for it.
func (l *loop) stop() {
	l.post(func() { l.exit = true })
	<-l.done
}

// onLoop reports whether the caller runs on the loop: a handler of the application calling the agent.
func (l *loop) onLoop() bool {
	return l.gid.Load() == goroutineID()
}

func (l *loop) check() {
	if checkLoop && !l.onLoop() {
		panic("sip/ua: agent state used outside its loop")
	}
}

// call runs f on the loop and returns its result: directly on the loop, through the queue from elsewhere. It returns
// ErrClosed once the loop stopped.
func (l *loop) call(f func() error) error {
	if l.onLoop() {
		return f()
	}

	result := make(chan error, 1)

	l.post(func() { result <- f() })

	select {
	case err := <-result:
		return err
	case <-l.done:
		select {
		case err := <-result:
			return err
		default:
			return ErrClosed
		}
	}
}

// get runs f on the loop and returns its value; the zero value once the loop stopped.
func get[T any](l *loop, f func() T) T {
	var v T

	_ = l.call(func() error {
		v = f()
		return nil
	})

	return v
}

// goroutineID is the ID of the calling goroutine, from the header of its stack trace.
func goroutineID() uint64 {
	var buf [64]byte

	b := buf[:runtime.Stack(buf[:], false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	b = b[:max(bytes.IndexByte(b, ' '), 0)]

	n, _ := strconv.ParseUint(string(b), 10, 64)

	return n
}

// timer is a timer of the loop. Its function runs on the loop unless the timer was stopped first: stopping on the
// loop wins over an expiry already posted.
type timer struct {
	t       transaction.Timer
	stopped bool
}

func (a *Agent) after(d time.Duration, f func()) *timer {
	a.loop.check()

	tm := &timer{}

	tm.t = a.clock.AfterFunc(d, func() {
		a.loop.post(func() {
			if !tm.stopped {
				tm.stopped = true
				f()
			}
		})
	})

	return tm
}

func (tm *timer) stop() {
	if tm != nil && !tm.stopped {
		tm.stopped = true
		tm.t.Stop()
	}
}

// client is a client transaction handler posting to the loop.
type client struct {
	l        *loop
	response func(*sip.Response)
	err      func(error)
}

func (a *Agent) client(response func(*sip.Response), err func(error)) transaction.ClientHandler {
	return client{l: a.loop, response: response, err: err}
}

func (c client) HandleResponse(res *sip.Response) {
	c.l.post(func() { c.response(res) })
}

func (c client) HandleError(err error) {
	c.l.post(func() { c.err(err) })
}
