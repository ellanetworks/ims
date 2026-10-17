package ua

import (
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// checkLoop enables the assertions that the state of a usage is used with its strand held. Tests enable it.
var checkLoop = false

// A Strand serialises the work of the dialogs on it: their sessions and subscriptions, the handlers of the
// application, and its own jobs (Post, AfterFunc). An Agent puts each dialog set on a strand of its own unless the
// application chooses one: the legs of a call bridged by a B2BUA, for one, share a strand, which orders their events
// and lets their handlers act on both legs without locks of the application.
//
// The state of the usages on a strand changes under its lock: by the methods of the application, which return once
// done and never wait for a handler, and by the inputs of the transaction layer and the timers, which the strand
// runs as jobs. Handlers and the application's jobs run in order on the strand's worker goroutine, outside the lock:
// they may call the agent, and should not block, since the strand waits for them.
type Strand struct {
	a  *Agent
	mu sync.Mutex

	qmu     sync.Mutex
	items   []item
	running bool
}

// item is a job of the strand: an input of the stack, run under the strand's lock, or an event, run outside it.
type item struct {
	f      func()
	locked bool
}

// NewStrand makes a strand, for the dialogs the application wants serialised together.
func (a *Agent) NewStrand() *Strand {
	return &Strand{a: a}
}

// Post runs f on the strand, after the events and jobs already due, outside its lock.
func (st *Strand) Post(f func()) {
	st.enqueue(item{f: f})
}

// AfterFunc runs f on the strand once d passed on the clock of the agent, unless stopped first.
func (st *Strand) AfterFunc(d time.Duration, f func()) *Timer {
	tm := &Timer{}

	tm.t = st.a.clock.AfterFunc(d, func() {
		st.enqueue(item{f: func() {
			if tm.fire() {
				f()
			}
		}})
	})

	return tm
}

// Timer is a timer of AfterFunc.
type Timer struct {
	mu      sync.Mutex
	t       transaction.Timer
	stopped bool
}

// Stop prevents the function of the timer from running, and reports whether it did.
func (tm *Timer) Stop() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.stopped {
		return false
	}

	tm.stopped = true
	tm.t.Stop()

	return true
}

func (tm *Timer) fire() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.stopped {
		return false
	}

	tm.stopped = true

	return true
}

// post runs f on the strand under its lock: an input of the transaction layer or of a timer.
func (st *Strand) post(f func()) {
	st.enqueue(item{f: f, locked: true})
}

// emit makes a handler due: it runs on the strand once the jobs before it ran, outside the lock. Called with the
// strand held.
func (st *Strand) emit(f func()) {
	st.check()
	st.enqueue(item{f: f})
}

func (st *Strand) enqueue(it item) {
	st.qmu.Lock()
	st.items = append(st.items, it)

	start := !st.running
	st.running = true
	st.qmu.Unlock()

	if start {
		st.a.working(1)

		go st.run()
	}
}

// run is the worker of the strand, while it has work.
func (st *Strand) run() {
	defer st.a.working(-1)

	for {
		st.qmu.Lock()

		if len(st.items) == 0 {
			st.running = false
			st.qmu.Unlock()

			return
		}

		it := st.items[0]
		st.items[0] = item{}
		st.items = st.items[1:]
		st.qmu.Unlock()

		if it.locked {
			st.mu.Lock()
			st.safely(it.f)
			st.mu.Unlock()
		} else {
			st.safely(it.f)
		}
	}
}

// safely runs f, recovering a panic: one handler failing does not stop the strand, nor the agent.
func (st *Strand) safely(f func()) {
	defer func() {
		if v := recover(); v != nil {
			st.a.log.Error("panic on a strand", slog.Any("panic", v), slog.String("stack", string(debug.Stack())))
		}
	}()

	f()
}

// do runs f with the strand held: a method of the application.
func (st *Strand) do(f func() error) error {
	st.mu.Lock()
	defer st.mu.Unlock()

	return f()
}

func (st *Strand) check() {
	if checkLoop && st.mu.TryLock() {
		st.mu.Unlock()
		panic("sip/ua: usage state used without its strand")
	}
}

// get runs f with the strand held, and returns its value.
func get[T any](st *Strand, f func() T) T {
	st.mu.Lock()
	defer st.mu.Unlock()

	return f()
}

// timer is a timer of the stack on a strand. Its function runs under the strand's lock unless the timer was stopped
// first, with the strand held: stopping wins over an expiry already posted.
type timer struct {
	t       transaction.Timer
	stopped bool
}

func (st *Strand) after(d time.Duration, f func()) *timer {
	st.check()

	tm := &timer{}

	tm.t = st.a.clock.AfterFunc(d, func() {
		st.post(func() {
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

// client is a client transaction handler posting to a strand.
type client struct {
	st       *Strand
	response func(*sip.Response)
	err      func(error)
}

func (st *Strand) client(response func(*sip.Response), err func(error)) transaction.ClientHandler {
	return client{st: st, response: response, err: err}
}

func (c client) HandleResponse(res *sip.Response) {
	c.st.post(func() { c.response(res) })
}

func (c client) HandleError(err error) {
	c.st.post(func() { c.err(err) })
}
