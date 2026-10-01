package transaction

import (
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

type core struct {
	layer  *Layer
	forget func()

	mu       sync.Mutex
	state    State
	reliable bool
	interval time.Duration
	timers   []Timer
	queue    []func()
	draining bool
	err      error
	done     chan struct{}
}

func (c *core) init(l *Layer, s State) {
	c.layer = l
	c.state = s
	c.done = make(chan struct{})
}

func (c *core) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.state
}

func (c *core) Done() <-chan struct{} {
	return c.done
}

func (c *core) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.err
}

func (c *core) emit(f func()) {
	c.queue = append(c.queue, f)
}

func (c *core) drain() {
	c.mu.Lock()

	if c.draining {
		c.mu.Unlock()
		return
	}

	c.draining = true

	for len(c.queue) > 0 {
		f := c.queue[0]
		c.queue[0] = nil
		c.queue = c.queue[1:]

		c.mu.Unlock()
		f()
		c.mu.Lock()
	}

	c.draining = false
	c.mu.Unlock()
}

func (c *core) after(d time.Duration, f func()) {
	l := c.layer

	c.timers = append(c.timers, l.clock.AfterFunc(d, func() {
		if !l.enter() {
			return
		}

		defer l.wg.Done()

		c.mu.Lock()
		if c.state != Terminated {
			f()
		}
		c.mu.Unlock()

		c.drain()
	}))
}

func (c *core) afterUnreliable(d time.Duration, f func()) {
	if c.reliable {
		f()
		return
	}

	c.after(d, f)
}

func (c *core) stopTimers() {
	for _, t := range c.timers {
		t.Stop()
	}

	c.timers = nil
}

func (c *core) end() {
	c.terminate(nil)
}

func (c *core) terminate(err error) {
	if c.state == Terminated {
		return
	}

	c.state = Terminated
	c.err = err
	c.stopTimers()
	close(c.done)

	if c.forget != nil {
		c.forget()
	}
}
