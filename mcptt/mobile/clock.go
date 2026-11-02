package mobile

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/sip/transaction"
)

const (
	wakeAfter = time.Minute

	awakeFor = 40 * time.Second
)

type clock struct {
	p Platform

	base time.Time
	boot time.Duration

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

func (c *clock) Now() time.Time { return c.base.Add(bootTime() - c.boot) }

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

func (c *clock) woken(id int64) {
	c.mu.Lock()
	t := c.alarm[id]
	c.mu.Unlock()

	if t != nil {
		t.fire(true)
	}
}

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
