package transaction

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
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

type core struct {
	layer   *Layer
	forget  func()
	onError func(error)
	onEnd   []func()

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

	// sending counts the sends of the transaction in the outboxes of the layer; dropped is set once its user dropped
	// it (Layer.Drop): what it queued is not sent.
	sending int
	dropped bool

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

// push sends a message of the transaction on the flow f, in the order of the flow (outbox); result, when not nil,
// receives the outcome under the lock of the transaction.
func (tx *core) push(f sip.Flow, send func() error, result func(error)) {
	tx.sending++

	ok := tx.layer.enqueue(f, func(ok bool) {
		tx.mu.Lock()
		dropped := tx.dropped
		tx.mu.Unlock()

		err := ErrClosed

		switch {
		case dropped:
			err = ErrDropped
		case ok:
			err = send()
		}

		tx.mu.Lock()

		tx.sending--

		if result != nil && !errors.Is(tx.err, ErrClosed) {
			result(err)
		}

		tx.closeDone()
		tx.unlock()
	})
	if !ok {
		tx.sending--
	}
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

	for _, f := range tx.onEnd {
		tx.emit(f)
	}

	tx.onEnd = nil

	tx.closeDone()
}

func (tx *core) OnTerminated(f func()) {
	tx.mu.Lock()

	if tx.state == Terminated {
		tx.emit(f)
	} else {
		tx.onEnd = append(tx.onEnd, f)
	}

	tx.unlock()
}

func (tx *core) closeDone() {
	if tx.state != Terminated || tx.sending > 0 || tx.doneSent {
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
