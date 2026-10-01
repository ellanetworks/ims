package transaction

import (
	"errors"
	"log/slog"
	"sync"
	"time"
)

type State int

const (
	Trying State = iota
	Calling
	Proceeding
	Completed
	Confirmed
	Accepted
	Terminated
)

var stateNames = [...]string{
	Trying:     "Trying",
	Calling:    "Calling",
	Proceeding: "Proceeding",
	Completed:  "Completed",
	Confirmed:  "Confirmed",
	Accepted:   "Accepted",
	Terminated: "Terminated",
}

func (s State) String() string {
	if s < 0 || int(s) >= len(stateNames) {
		return "State(?)"
	}

	return stateNames[s]
}

type sendJob struct {
	send   func() error
	result func(error)
}

type core struct {
	layer   *Layer
	forget  func()
	onError func(error)

	mu       sync.Mutex
	cond     sync.Cond
	state    State
	reliable bool
	interval time.Duration
	timers   []Timer
	err      error

	queue    []func()
	emitted  uint64
	ran      uint64
	draining bool

	out     []sendJob
	sending bool

	done     chan struct{}
	doneSent bool
}

func (tx *core) init(l *Layer, s State) {
	tx.layer = l
	tx.state = s
	tx.cond.L = &tx.mu
	tx.done = make(chan struct{})
}

func (tx *core) State() State {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	return tx.state
}

func (tx *core) Done() <-chan struct{} {
	return tx.done
}

func (tx *core) Err() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	return tx.err
}

func (tx *core) emit(f func()) {
	tx.queue = append(tx.queue, f)
	tx.emitted++
}

func (tx *core) unlock() {
	tx.mu.Unlock()
	tx.drain()
}

func (tx *core) unlockInOrder() {
	want := tx.emitted
	tx.mu.Unlock()
	tx.drain()
	tx.mu.Lock()

	for tx.ran < want {
		tx.cond.Wait()
	}

	tx.mu.Unlock()
}

func (tx *core) drain() {
	tx.mu.Lock()

	if tx.draining {
		tx.mu.Unlock()
		return
	}

	tx.draining = true

	for len(tx.queue) > 0 {
		f := tx.queue[0]
		tx.queue[0] = nil
		tx.queue = tx.queue[1:]

		tx.mu.Unlock()
		f()
		tx.mu.Lock()

		tx.ran++
		tx.cond.Broadcast()
	}

	tx.draining = false
	tx.mu.Unlock()
}

func (tx *core) push(send func() error, result func(error)) {
	tx.out = append(tx.out, sendJob{send: send, result: result})

	if tx.sending {
		return
	}

	if !tx.layer.enter() {
		tx.out = nil
		return
	}

	tx.sending = true

	go tx.flush()
}

func (tx *core) flush() {
	defer tx.layer.wg.Done()

	tx.mu.Lock()

	for len(tx.out) > 0 {
		j := tx.out[0]
		tx.out[0] = sendJob{}
		tx.out = tx.out[1:]

		tx.mu.Unlock()

		err := j.send()

		tx.mu.Lock()

		if j.result != nil && !errors.Is(tx.err, ErrClosed) {
			j.result(err)
		}
	}

	tx.sending = false
	tx.closeDone()
	tx.unlock()
}

func (tx *core) after(d time.Duration, f func()) {
	l := tx.layer

	tx.timers = append(tx.timers, l.clock.AfterFunc(d, func() {
		if !l.enter() {
			return
		}

		defer l.wg.Done()

		tx.mu.Lock()
		if tx.state != Terminated {
			f()
		}

		tx.unlock()
	}))
}

func (tx *core) afterUnreliable(d time.Duration, f func()) {
	if tx.reliable {
		f()
		return
	}

	tx.after(d, f)
}

func (tx *core) stopTimers() {
	for _, t := range tx.timers {
		t.Stop()
	}

	tx.timers = nil
}

func (tx *core) end() {
	tx.terminate(nil)
}

func (tx *core) report(err error) {
	tx.layer.log.Debug("SIP transaction failure", slog.String("state", tx.state.String()), slog.Any("error", err))

	if f := tx.onError; f != nil {
		tx.emit(func() { f(err) })
	}
}

func (tx *core) fail(err error) {
	if tx.state == Terminated {
		return
	}

	tx.report(err)
	tx.terminate(err)
}

func (tx *core) terminate(err error) {
	if tx.state == Terminated {
		return
	}

	tx.state = Terminated
	tx.err = err
	tx.stopTimers()

	if tx.forget != nil {
		tx.forget()
	}

	tx.closeDone()
}

func (tx *core) closeDone() {
	if tx.state != Terminated || tx.sending || tx.doneSent {
		return
	}

	tx.doneSent = true
	tx.emit(func() { close(tx.done) })
}

func (tx *core) shutdown() {
	tx.mu.Lock()
	tx.fail(ErrClosed)
	tx.unlock()
}
