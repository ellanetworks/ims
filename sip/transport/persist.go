package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/ellanetworks/ims/sip"
)

const (
	// RFC 5626 section 4.4.1
	DefaultKeepAliveInterval = 120 * time.Second

	// RFC 5626 section 4.4.1
	DefaultPongTimeout = 10 * time.Second

	minReconnectBackoff = 250 * time.Millisecond
	maxReconnectBackoff = 30 * time.Second
)

var errClosedEarly = errors.New("closed before any data or keep-alive interval")

type KeepAlive struct {
	Interval time.Duration

	PongTimeout time.Duration
}

// SIPconnect 2.0 section 17.2
// SIPconnect 2.0 section 17.4
func (t *Transport) Persist(f sip.Flow, ka KeepAlive) (stop func(), err error) {
	f = normalize(f)

	if f.Transport != sip.TCP && f.Transport != sip.TLS {
		return nil, fmt.Errorf("sip/transport: persistent connection on %s: %w", f, ErrUnsupportedTransport)
	}

	if !f.Remote.IsValid() {
		return nil, fmt.Errorf("sip/transport: persistent connection on %s: no remote address", f)
	}

	if _, err := t.listener(f); err != nil {
		return nil, fmt.Errorf("sip/transport: persistent connection on %s: %w", f, err)
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

		t.persist(ctx, f, ka)
	}()

	return func() {
		cancel()
		<-done
	}, nil
}

func (t *Transport) persist(ctx context.Context, f sip.Flow, ka KeepAlive) {
	var backoff time.Duration

	for {
		c, err := t.conn(ctx, f, true, true)
		if err == nil {
			start := time.Now()

			if !t.keepAlive(ctx, c, f, ka) {
				return
			}

			if c.received.Load() || time.Since(start) >= ka.Interval {
				backoff = 0

				t.log.Info("SIP persistent connection lost, reconnecting", slog.String("flow", f.String()))

				continue
			}

			err = errClosedEarly
		}

		if ctx.Err() != nil {
			return
		}

		backoff = min(max(2*backoff, minReconnectBackoff), maxReconnectBackoff)
		t.log.Warn("SIP persistent connection failed", slog.String("flow", f.String()), slog.Any("error", err),
			slog.Duration("retry", backoff))

		if !sleep(ctx, backoff) {
			return
		}
	}
}

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

		if err := t.ping(ctx, c, f, ka.PongTimeout); err != nil {
			if ctx.Err() != nil {
				return false
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
