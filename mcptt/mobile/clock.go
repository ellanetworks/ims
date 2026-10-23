package mobile

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	// wakeAfter is the shortest timer that also wakes the device: those of the registration and the subscriptions, not
	// those of the transactions and the floor, which run while the device is awake for them.
	wakeAfter = time.Minute

	// awakeFor is how long the device stays awake after an alarm: a transaction's Timer F (64·T1, RFC 3261 §17.1.2.2)
	// and a margin, for the registration it fires.
	awakeFor = 40 * time.Second
)

// clock is the clock of a session: its time the boot clock's, which counts the time the device sleeps (Go's
// monotonic clock does not), its long timers also on the platform's alarms, since Go's timers do not run while the
// device sleeps.
type clock struct {
	p Platform

	// base and boot are the time and the boot clock when the clock started: Now is base and the boot clock since.
	base time.Time
	boot time.Duration

	// epoch distinguishes the alarms of this clock from those of a previous session.
	epoch int64
	next  atomic.Int64

	mu     sync.Mutex
	alarm  map[int64]*timer
	closed bool
}

func newClock(p Platform) *clock {
	return &clock{
		p: p, base: time.Now().Round(0), boot: bootTime(), epoch: int64(rand.Uint32()>>1) << 32, alarm: map[int64]*timer{},
	}
}

// Now is the time of the boot clock: its differences include the time the device slept, and are immune to changes of
// the wall clock.
func (c *clock) Now() time.Time { return c.base.Add(bootTime() - c.boot) }

// timer is a timer of the clock, which fires once: by the Go timer or by the platform's alarm, the first.
type timer struct {
	c     *clock
	id    int64
	t     *time.Timer
	f     func()
	fired atomic.Bool
}

func (c *clock) AfterFunc(d time.Duration, f func()) transaction.Timer {
	t := &timer{c: c, f: f}
	t.t = time.AfterFunc(d, func() { t.fire(false) })

	if d >= wakeAfter {
		c.mu.Lock()

		if !c.closed {
			t.id = c.epoch | c.next.Add(1)
			c.alarm[t.id] = t
		}

		c.mu.Unlock()

		if t.id != 0 {
			c.p.Wake(t.id, d.Milliseconds())
		}
	}

	return t
}

// fire runs the function of the timer once: by its Go timer, the alarm cancelled; or by the alarm, woken, on a
// goroutine of its own, the device kept awake meanwhile.
func (t *timer) fire(woken bool) {
	if !t.fired.CompareAndSwap(false, true) {
		return
	}

	if !woken {
		t.forget(true)
		t.f()

		return
	}

	t.t.Stop()
	t.forget(false)
	t.c.p.KeepAwake(awakeFor.Milliseconds())

	go t.f()
}

// forget drops the timer's alarm, cancelling it with the platform unless it went off.
func (t *timer) forget(cancel bool) {
	if t.id == 0 {
		return
	}

	t.c.mu.Lock()
	delete(t.c.alarm, t.id)
	t.c.mu.Unlock()

	if cancel {
		t.c.p.CancelWake(t.id)
	}
}

func (t *timer) Stop() bool {
	if !t.fired.CompareAndSwap(false, true) {
		return false
	}

	t.t.Stop()
	t.forget(true)

	return true
}

// woken fires the timer of the alarm id, if it is still due.
func (c *clock) woken(id int64) {
	c.mu.Lock()
	t := c.alarm[id]
	c.mu.Unlock()

	if t != nil {
		t.fire(true)
	}
}

// sleep waits d on the clock, an alarm waking the device for a long one; false if done is closed first.
func (c *clock) sleep(d time.Duration, done <-chan struct{}) bool {
	ch := make(chan struct{})
	t := c.AfterFunc(d, func() { close(ch) })

	select {
	case <-ch:
		return true
	case <-done:
		t.Stop()
		return false
	}
}

// close cancels the alarms of the clock, its timers to fire no more by them.
func (c *clock) close() {
	c.mu.Lock()
	c.closed = true
	ids := make([]int64, 0, len(c.alarm))

	for id := range c.alarm {
		ids = append(ids, id)
	}

	clear(c.alarm)
	c.mu.Unlock()

	for _, id := range ids {
		c.p.CancelWake(id)
	}
}
