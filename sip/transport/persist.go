package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/internal/sockopt"
)

const (
	// RFC 5626 section 4.4.1
	DefaultKeepAliveInterval = 120 * time.Second

	// RFC 5626 section 4.4.1
	DefaultPongTimeout = 10 * time.Second

	minReconnectBackoff = 250 * time.Millisecond
	maxReconnectBackoff = 30 * time.Second
)

// RFC 1122 section 4.2.3.5
const userTimeout = 100 * time.Second

var (
	errClosedEarly = errors.New("closed before the keep-alive interval")
	errNoTarget    = errors.New("no server to connect to")
)

type KeepAlive struct {
	Interval time.Duration

	PongTimeout time.Duration
}

type Targets func(ctx context.Context) ([]sip.Flow, error)

// SIPconnect 2.0 section 17.2
// SIPconnect 2.0 section 17.4
func (t *Transport) Persist(targets Targets, ka KeepAlive) (stop func(), err error) {
	if targets == nil {
		return nil, errors.New("sip/transport: persistent connection without targets")
	}

	if ka.Interval <= 0 {
		ka.Interval = DefaultKeepAliveInterval
	}

	ctx, cancel := context.WithCancel(t.ctx)
	done := make(chan struct{})

	t.mu.Lock()

	if t.closed {
		t.mu.Unlock()
		cancel()

		return nil, ErrClosed
	}

	t.wg.Add(1)
	t.mu.Unlock()

	go func() {
		defer close(done)
		defer t.wg.Done()

		t.persist(ctx, targets, ka)
	}()

	return func() {
		cancel()
		<-done
	}, nil
}

// SIPconnect 2.0 section 17.2
func (t *Transport) persist(ctx context.Context, targets Targets, ka KeepAlive) {
	var backoff time.Duration

	for {
		flows, err := targets(ctx)
		if ctx.Err() != nil {
			return
		}

		if err == nil && len(flows) == 0 {
			err = errNoTarget
		}

		stable := false

		for _, f := range flows {
			f = normalize(f)

			c, ferr := t.persistent(ctx, f)
			if ctx.Err() != nil {
				if c != nil {
					c.close()
				}

				return
			}

			if ferr != nil {
				err = fmt.Errorf("%s: %w", f, ferr)
				t.log.Debug("SIP persistent connection failed", slog.String("flow", f.String()), slog.Any("error", ferr))

				continue
			}

			start := time.Now()

			if !t.keepAlive(ctx, c, f, ka) {
				c.close()
				return
			}

			if time.Since(start) >= ka.Interval {
				stable = true

				t.log.Info("SIP persistent connection lost, reconnecting", slog.String("flow", f.String()))

				break
			}

			err = fmt.Errorf("%s: %w", f, errClosedEarly)
		}

		if stable {
			backoff = 0
			continue
		}

		backoff = min(max(2*backoff, minReconnectBackoff), maxReconnectBackoff)
		t.log.Warn("no SIP persistent connection to any server", slog.Any("error", err), slog.Duration("retry", backoff))

		if !sleep(ctx, backoff) {
			return
		}
	}
}

func (t *Transport) persistent(ctx context.Context, f sip.Flow) (*conn, error) {
	if f.Transport != sip.TCP && f.Transport != sip.TLS {
		return nil, ErrUnsupportedTransport
	}

	if !f.Remote.IsValid() {
		return nil, errors.New("no remote address")
	}

	if _, err := t.listener(f); err != nil {
		return nil, err
	}

	c, err := t.acquire(ctx, f, true, true, true)
	if err != nil {
		return nil, err
	}

	if rc, err := c.tcp.SyscallConn(); err == nil {
		_ = sockopt.UserTimeout(rc, userTimeout)
	}

	return c, nil
}

func (t *Transport) abandon(c *conn) {
	t.mu.Lock()
	defer t.mu.Unlock()

	c.close()

	if t.conns[c.flow] == c {
		delete(t.conns, c.flow)
	}
}

// RFC 5626 section 4.4.1
// SIPconnect 2.0 section 17.4
func (t *Transport) keepAlive(ctx context.Context, c *conn, f sip.Flow, ka KeepAlive) bool {
	for {
		timer := time.NewTimer(keepAliveDelay(ka.Interval))

		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-c.done:
			timer.Stop()
			return true
		case <-timer.C:
		}

		err := t.ping(ctx, c, f, ka.PongTimeout)

		switch {
		case err == nil:
			continue
		case ctx.Err() != nil:
			return false
		case errors.Is(err, ErrNoPong) && !c.ponged.Load():
			continue
		}

		t.log.Debug("SIP keep-alive failed", slog.String("flow", f.String()), slog.Any("error", err))
		c.abort()

		select {
		case <-c.done:
		case <-ctx.Done():
			return false
		}

		return true
	}
}

func (t *Transport) ping(ctx context.Context, c *conn, f sip.Flow, wait time.Duration) error {
	if wait <= 0 {
		return t.write(c, []byte("\r\n\r\n"))
	}

	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	return t.pingConn(ctx, c, f)
}

// RFC 5626 section 4.4.1
func keepAliveDelay(upper time.Duration) time.Duration {
	lower := upper * 4 / 5

	return lower + rand.N(upper-lower+1)
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
