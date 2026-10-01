package siptest

import (
	"slices"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip/transaction"
)

type Clock struct {
	advance sync.Mutex

	mu     sync.Mutex
	now    time.Duration
	seq    uint64
	timers []*fakeTimer
}

type fakeTimer struct {
	c    *Clock
	when time.Duration
	seq  uint64
	f    func()
}

func NewClock() *Clock {
	return &Clock{}
}

func (c *Clock) AfterFunc(d time.Duration, f func()) transaction.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.seq++
	t := &fakeTimer{c: c, when: c.now + max(d, 0), seq: c.seq, f: f}
	c.timers = append(c.timers, t)

	return t
}

func (c *Clock) Now() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *Clock) Advance(d time.Duration) {
	c.advance.Lock()
	defer c.advance.Unlock()

	c.mu.Lock()

	end := c.now + max(d, 0)

	for {
		i := c.next(end)
		if i < 0 {
			break
		}

		t := c.timers[i]
		c.timers = slices.Delete(c.timers, i, i+1)
		c.now = t.when

		c.mu.Unlock()
		t.f()
		c.mu.Lock()
	}

	c.now = end
	c.mu.Unlock()
}

func (c *Clock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.timers)
}

func (c *Clock) next(end time.Duration) int {
	best := -1

	for i, t := range c.timers {
		if t.when > end {
			continue
		}

		if best < 0 || t.when < c.timers[best].when || (t.when == c.timers[best].when && t.seq < c.timers[best].seq) {
			best = i
		}
	}

	return best
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()

	i := slices.Index(t.c.timers, t)
	if i < 0 {
		return false
	}

	t.c.timers = slices.Delete(t.c.timers, i, i+1)

	return true
}
