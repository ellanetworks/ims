package proxy

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

type responseContext struct {
	p      *Proxy
	stx    *transaction.ServerTransaction
	invite bool
	hook   func(res *sip.Response)

	mu        sync.Mutex
	client    *transaction.ClientTransaction
	timer     transaction.Timer
	final     bool
	cancelled bool
	upstream  bool
}

func (c *responseContext) HandleResponse(res *sip.Response) {
	res = res.Clone()

	if _, ok := res.Header.PopFirst("Via"); !ok || !res.Header.Has("Via") {
		return
	}

	c.mu.Lock()

	switch {
	case res.StatusCode == 100:
		c.mu.Unlock()
		return
	case res.IsProvisional():
		if c.final {
			c.mu.Unlock()
			return
		}

		c.resetTimerC()
	case res.IsSuccess():
		late := c.final
		if late && !c.invite {
			c.mu.Unlock()
			return
		}

		c.finish()
		c.mu.Unlock()

		c.relay(res, late)

		return
	default:
		if c.final {
			c.mu.Unlock()
			return
		}

		c.finish()

		if res.StatusCode == 503 {
			res = sip.NewResponse(c.stx.Request(), 500, "")
		}
	}

	c.mu.Unlock()

	c.relay(res, false)
}

func (c *responseContext) HandleError(err error) {
	c.mu.Lock()

	if c.final || errors.Is(err, transaction.ErrClosed) {
		c.finish()
		c.mu.Unlock()

		return
	}

	c.finish()

	code := 500

	switch {
	case c.upstream:
		code = 487
	case errors.Is(err, transaction.ErrTimeout):
		code = 408
	}

	c.mu.Unlock()

	c.p.log.Debug("proxied request failed", slog.String("request", c.stx.Request().StartLine()), slog.Any("error", err))
	c.relay(sip.NewResponse(c.stx.Request(), code, ""), false)
}

func (c *responseContext) relay(res *sip.Response, late bool) {
	if c.hook != nil {
		c.hook(res)
	}

	err := c.stx.Respond(res)
	if err == nil {
		return
	}

	if late && errors.Is(err, transaction.ErrFinalResponseSent) {
		c.p.relayStateless(c.stx, res)
		return
	}

	c.p.log.Debug("relaying a response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
}

func (c *responseContext) finish() {
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}

	if !c.final {
		c.final = true
		c.p.forget(c.stx, c)
	}
}

func (c *responseContext) resetTimerC() {
	if !c.invite || c.timer == nil {
		return
	}

	c.timer.Stop()
	c.timer = c.p.clock.AfterFunc(c.p.timerC, c.timerC)
}

func (c *responseContext) timerC() {
	c.mu.Lock()

	if c.final {
		c.mu.Unlock()
		return
	}

	c.finish()
	c.cancelled = true
	client := c.client

	c.mu.Unlock()

	c.p.log.Debug("Timer C fired", slog.String("request", c.stx.Request().StartLine()))
	c.relay(sip.NewResponse(c.stx.Request(), 408, ""), false)

	if client != nil {
		_ = client.Cancel()
	}
}

func (c *responseContext) cancel() {
	c.mu.Lock()

	if c.final || c.cancelled {
		c.mu.Unlock()
		return
	}

	c.cancelled = true
	c.upstream = true
	client := c.client

	c.mu.Unlock()

	if client != nil {
		_ = client.Cancel()
	}
}
