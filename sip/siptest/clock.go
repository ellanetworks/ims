package siptest

import (
	"slices"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip/transaction"
)

// Clock is a fake clock: time passes by Advance only.
type Clock struct {
	advance sync.Mutex

	// settle, on the clock of a Net, waits for the tasks of the net: Advance runs each timer once those its
	// predecessor started are done.
	settle func()

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

// epoch is the time of a fake clock before it advances.
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Now is the fake time: epoch plus the time the clock advanced.
func (c *Clock) Now() time.Time {
	return epoch.Add(c.Elapsed())
}

// Elapsed is the time the clock advanced.
func (c *Clock) Elapsed() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// Advance advances the clock by d, running the timers due in the order of their time, then of their arming. On the
// clock of a Net, the tasks under way finish first, and each timer runs once those the previous one started are done:
// a timer may arm the next one, which runs in turn if due.
func (c *Clock) Advance(d time.Duration) {
	c.advance.Lock()
	defer c.advance.Unlock()

	c.quiesce()

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
		c.quiesce()
		c.mu.Lock()
	}

	c.now = end
	c.mu.Unlock()
}

// Next is the time until the next timer, if one is armed.
func (c *Clock) Next() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	i := c.next(1<<63 - 1)
	if i < 0 {
		return 0, false
	}

	return c.timers[i].when - c.now, true
}

func (c *Clock) quiesce() {
	if c.settle != nil {
		c.settle()
	}
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
