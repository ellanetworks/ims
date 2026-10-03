package proxy

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

type responseContext struct {
	p      *Proxy
	tx     *transaction.ServerTransaction
	invite bool

	mu        sync.Mutex
	branch    *branch
	final     bool
	success   bool
	cancelled bool
	reason    []sip.Field
	answers   map[string]bool

	queue    []func()
	draining bool
}

type branch struct {
	c       *responseContext
	onReply func(r Reply) Verdict
	timeout time.Duration

	client    *transaction.ClientTransaction
	done      bool
	responded bool

	timerC, timer transaction.Timer
	genC          int

	dialog  *Dialog
	req     *sip.Request
	initial bool
}

func newContext(p *Proxy, tx *transaction.ServerTransaction) *responseContext {
	return &responseContext{p: p, tx: tx, invite: tx.Request().Method == "INVITE", answers: make(map[string]bool)}
}

func (c *responseContext) open(opts Options) (*branch, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case c.final || c.success || c.cancelled:
		return nil, ErrAnswered
	case c.branch != nil && !c.branch.done:
		return nil, ErrForwarded
	}

	b := &branch{c: c, onReply: opts.OnReply, timeout: opts.Timeout}
	c.branch = b

	return b, nil
}

func (c *responseContext) abandon(b *branch, fresh bool) {
	c.mu.Lock()
	b.done = true

	if c.branch == b {
		c.branch = nil
	}

	c.mu.Unlock()

	if fresh {
		c.p.forget(c)
	}
}

func (c *responseContext) started(b *branch, client *transaction.ClientTransaction) {
	c.mu.Lock()

	b.client = client

	if !b.done {
		if c.invite {
			b.startTimerC()
		}

		if b.timeout > 0 && !b.stopsTimeout() {
			b.timer = c.p.clock.AfterFunc(b.timeout, b.timeoutFired)
		}
	}

	cancel := c.cancelled && !b.done
	reason := c.reason

	c.mu.Unlock()

	if cancel {
		_ = client.Cancel(reason...)
	}
}

func (b *branch) startTimerC() {
	if b.timerC != nil {
		b.timerC.Stop()
	}

	b.genC++
	gen := b.genC
	b.timerC = b.c.p.clock.AfterFunc(b.c.p.timerC, func() { b.timerCFired(gen) })
}

func (b *branch) finish() {
	b.done = true

	for _, t := range []transaction.Timer{b.timerC, b.timer} {
		if t != nil {
			t.Stop()
		}
	}
}

func (b *branch) timerCFired(gen int) {
	c := b.c

	c.mu.Lock()

	if b.done || gen != b.genC {
		c.mu.Unlock()
		return
	}

	b.finish()
	client := b.client
	responded := b.responded

	c.mu.Unlock()

	c.p.log.Debug("Timer C fired", slog.String("request", c.tx.Request().StartLine()))

	if client != nil {
		_ = client.Cancel()
	}

	c.dispatch(b, Reply{Response: c.generate(408), Err: fmt.Errorf("%w: Timer C", transaction.ErrTimeout), Responded: responded})
}

func (b *branch) timeoutFired() {
	c := b.c

	c.mu.Lock()

	if b.done || b.stopsTimeout() {
		c.mu.Unlock()
		return
	}

	b.finish()
	client := b.client
	responded := b.responded

	c.mu.Unlock()

	var res *sip.Response

	if c.invite {
		if client != nil {
			_ = client.Cancel()
		}

		res = c.generate(408)
	}

	c.dispatch(b, Reply{Response: res, Err: fmt.Errorf("%w: no response within %s", transaction.ErrTimeout, b.timeout), Responded: responded})
}

func (b *branch) stopsTimeout() bool {
	return b.c.invite && b.responded
}

func (b *branch) HandleResponse(res *sip.Response) {
	c := b.c

	res = res.Clone()

	if _, ok := res.Header.PopFirst("Via"); !ok || !res.Header.Has("Via") {
		return
	}

	c.mu.Lock()

	b.responded = true

	if b.timer != nil && b.stopsTimeout() {
		b.timer.Stop()
	}

	switch {
	case res.IsProvisional():
		if res.StatusCode == 100 || !c.invite || b.done || c.final {
			c.mu.Unlock()
			return
		}

		b.startTimerC()
	case res.IsSuccess():
		tag := toTag(res)

		if relayed, seen := c.answers[tag]; seen {
			c.mu.Unlock()

			switch {
			case relayed && c.invite:
				if err := c.tx.Relay(res); err != nil {
					c.p.log.Debug("relaying a 2xx retransmission failed", slog.Any("error", err))
				}
			case b.dialog != nil && b.initial:
				b.dialog.retransmitted(res)
			}

			return
		}

		if c.final && !c.invite {
			c.mu.Unlock()
			return
		}

		c.answers[tag] = false
		c.success = true

		b.finish()
	default:
		if b.done || c.final {
			c.mu.Unlock()
			return
		}

		b.finish()
	}

	c.mu.Unlock()

	c.dispatch(b, Reply{Response: res, Responded: true})
}

func (b *branch) HandleError(err error) {
	c := b.c

	c.mu.Lock()

	if b.done {
		c.mu.Unlock()
		return
	}

	b.finish()

	if errors.Is(err, transaction.ErrClosed) {
		c.mu.Unlock()
		c.p.forget(c)

		return
	}

	var res *sip.Response

	responded := b.responded

	switch {
	case !c.invite && errors.Is(err, transaction.ErrTimeout):
	case c.cancelled:
		res = c.generate(487)
	case errors.Is(err, transaction.ErrTimeout):
		res = c.generate(408)
	default:
		res = c.generate(500)
	}

	c.mu.Unlock()

	c.p.log.Debug("proxied request failed", slog.String("request", c.tx.Request().StartLine()), slog.Any("error", err))
	c.dispatch(b, Reply{Response: res, Err: err, Responded: responded})
}

func (c *responseContext) generate(code int) *sip.Response {
	return sip.NewResponse(c.tx.Request(), code, "")
}

func (c *responseContext) dispatch(b *branch, r Reply) {
	c.mu.Lock()

	c.queue = append(c.queue, func() { c.deliver(b, r) })

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

func (c *responseContext) deliver(b *branch, r Reply) {
	if b.dialog != nil && b.dialog.response(b.req, b.initial, r, c.isFinal()) {
		return
	}

	if b.onReply != nil && b.onReply(r) == Hold {
		return
	}

	if err := c.relayFrom(b, r.Response); err != nil {
		c.p.log.Debug("relaying a response failed", slog.Any("error", err))
	}
}

func (c *responseContext) relay(res *sip.Response) error {
	return c.relayFrom(nil, res)
}

func (c *responseContext) relayFrom(b *branch, res *sip.Response) error {
	c.mu.Lock()

	switch {
	case res == nil:
		if c.final || (b != nil && c.branch != b) {
			c.mu.Unlock()
			return ErrAnswered
		}

		c.final = true
		c.mu.Unlock()
		c.p.forget(c)

		return nil
	case res.IsProvisional():
		final := c.final
		c.mu.Unlock()

		if final {
			return ErrAnswered
		}

		return c.tx.Relay(res)
	case res.IsSuccess() && c.invite:
		c.final = true
		c.answers[toTag(res)] = true
		c.mu.Unlock()
	default:
		if c.final {
			c.mu.Unlock()
			return ErrAnswered
		}

		if b != nil && c.branch != b && !res.IsSuccess() {
			c.mu.Unlock()
			return ErrForwarded
		}

		c.final = true
		c.mu.Unlock()

		if res.StatusCode == 503 {
			res = c.generate(500)
		}
	}

	err := c.tx.Relay(res)
	c.p.forget(c)

	return err
}

func (c *responseContext) cancel(reason []sip.Field) {
	c.mu.Lock()

	if c.final || c.success || c.cancelled {
		c.mu.Unlock()
		return
	}

	c.cancelled = true
	c.reason = reason
	b := c.branch

	if b == nil || b.done {
		c.final = true
		c.mu.Unlock()

		c.p.answer(c.tx, 487)
		c.p.forget(c)

		return
	}

	client := b.client

	c.mu.Unlock()

	if client != nil {
		_ = client.Cancel(reason...)
	}
}

func (c *responseContext) isFinal() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.final
}

func (c *responseContext) dialog() *Dialog {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.branch == nil {
		return nil
	}

	return c.branch.dialog
}

func toTag(res *sip.Response) string {
	to, _ := res.Header.To()
	return to.Tag()
}
