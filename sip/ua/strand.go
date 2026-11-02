package ua

import (
	"bytes"
	"context"
	"log/slog"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

var checkLoop = false

type Strand struct {
	a  *Agent
	mu sync.Mutex

	worker atomic.Uint64

	qmu     sync.Mutex
	items   []item
	running bool
}

type item struct {
	f      func()
	locked bool
}

func (a *Agent) NewStrand() *Strand {
	return &Strand{a: a}
}

func (st *Strand) Post(f func()) {
	st.enqueue(item{f: f})
}

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

type Timer struct {
	mu      sync.Mutex
	t       transaction.Timer
	stopped bool
}

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

func (st *Strand) post(f func()) {
	st.enqueue(item{f: f, locked: true})
}

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

		st.a.sched.Go(st.run)
	}
}

func (st *Strand) run() {
	defer st.a.working(-1)

	if checkStrands.Load() {
		st.worker.Store(goroutine())
		defer st.worker.Store(0)
	}

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

func (st *Strand) safely(f func()) {
	defer func() {
		if v := recover(); v != nil {
			st.a.log.Error("panic on a strand", slog.Any("panic", v), slog.String("stack", string(debug.Stack())))
		}
	}()

	f()
}

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

func get[T any](st *Strand, f func() T) T {
	st.mu.Lock()
	defer st.mu.Unlock()

	return f()
}

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

func (st *Strand) later(ctx context.Context, d time.Duration, f func(ctx context.Context)) *timer {
	return st.after(d, func() { f(ctx) })
}

func (st *Strand) detached(ctx context.Context, d time.Duration, name string, f func(ctx context.Context)) *timer {
	link := trace.SpanContextFromContext(ctx)

	return st.after(d, func() {
		ctx, span := st.a.startDetached([]trace.SpanContext{link}, name)
		defer span.End()

		f(ctx)
	})
}

func (tm *timer) stop() {
	if tm != nil && !tm.stopped {
		tm.stopped = true
		tm.t.Stop()
	}
}

type client struct {
	st       *Strand
	response func(context.Context, *sip.Response)
	err      func(context.Context, error)
}

func (st *Strand) client(response func(context.Context, *sip.Response), err func(context.Context, error)) transaction.ClientHandler {
	return client{st: st, response: response, err: err}
}

func (c client) HandleResponse(ctx context.Context, res *sip.Response) {
	c.st.post(func() { c.response(ctx, res) })
}

func (c client) HandleError(ctx context.Context, err error) {
	c.st.post(func() { c.err(ctx, err) })
}

var checkStrands atomic.Bool

func CheckStrands(on bool) {
	checkStrands.Store(on)
}

func (st *Strand) Confined() bool {
	return !checkStrands.Load() || st.worker.Load() == goroutine()
}

func goroutine() uint64 {
	var buf [32]byte

	b := buf[:runtime.Stack(buf[:], false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))

	var id uint64

	for _, c := range b {
		if c < '0' || c > '9' {
			break
		}

		id = id*10 + uint64(c-'0')
	}

	return id
}
