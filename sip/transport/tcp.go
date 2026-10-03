package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/internal/sockopt"
)

var errNoConn = errors.New("no connection for the flow")

var crlf = []byte("\r\n")

const drainTimeout = time.Second

type conn struct {
	flow  sip.Flow
	ready chan struct{}
	nc    *net.TCPConn
	err   error

	wmu    sync.Mutex
	active atomic.Int64
	limit  atomic.Int64
	closed atomic.Bool
}

func (c *conn) touch() {
	c.active.Store(time.Now().UnixNano())
}

func (c *conn) lastActive() time.Time {
	return time.Unix(0, c.active.Load())
}

func (c *conn) close() {
	c.closed.Store(true)

	if c.nc != nil {
		_ = c.nc.Close()
	}
}

func (c *conn) abort() {
	if c.nc != nil {
		_ = c.nc.SetLinger(0)
	}

	c.close()
}

func (t *Transport) write(c *conn, b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	_ = c.nc.SetWriteDeadline(time.Now().Add(t.writeTimeout))

	if _, err := c.nc.Write(b); err != nil {
		c.close()
		return err
	}

	c.touch()

	return nil
}

func (t *Transport) conn(ctx context.Context, f sip.Flow, dial bool) (*conn, error) {
	t.mu.Lock()

	if t.closed {
		t.mu.Unlock()
		return nil, ErrClosed
	}

	c, ok := t.conns[f]
	if ok && c.closed.Load() {
		delete(t.conns, f)

		ok = false
	}

	switch {
	case ok:
		t.mu.Unlock()
	case !dial:
		t.mu.Unlock()
		return nil, errNoConn
	default:
		c = &conn{flow: f, ready: make(chan struct{})}
		t.conns[f] = c
		t.wg.Add(1)
		t.mu.Unlock()

		go t.dial(c)
	}

	select {
	case <-c.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if c.err != nil {
		return nil, c.err
	}

	return c, nil
}

func (t *Transport) dial(c *conn) {
	defer t.wg.Done()

	d := net.Dialer{
		LocalAddr: net.TCPAddrFromAddrPort(c.flow.Local),
		Timeout:   t.dialTimeout,
		Control:   sockopt.ReusePort,
	}

	nc, err := t.dialFunc(t.ctx, &d, network("tcp", c.flow.Remote.Addr()), c.flow.Remote.String())

	t.mu.Lock()
	defer t.mu.Unlock()
	defer close(c.ready)

	if err == nil && (t.closed || c.closed.Load() || t.conns[c.flow] != c) {
		_ = nc.Close()
		err = ErrClosed
	}

	if err != nil {
		c.err = err

		if t.conns[c.flow] == c {
			delete(t.conns, c.flow)
		}

		return
	}

	c.nc = nc.(*net.TCPConn)
	c.limit.Store(int64(t.idleTimeout))
	c.touch()
	t.wg.Add(1)

	go t.readTCP(c)

	t.log.Debug("SIP connection dialed", slog.String("flow", c.flow.String()))
}

func (t *Transport) accept(l *listener) {
	defer t.wg.Done()

	for {
		nc, err := l.tcp.AcceptTCP()
		if errors.Is(err, net.ErrClosed) {
			return
		}

		if err != nil {
			t.log.Warn("SIP accept failed", slog.String("local", l.local.String()), slog.Any("error", err))
			time.Sleep(50 * time.Millisecond)

			continue
		}

		f := sip.Flow{Transport: sip.TCP, Local: l.local, Remote: unmap(nc.RemoteAddr().(*net.TCPAddr).AddrPort())}
		c := &conn{flow: f, ready: make(chan struct{}), nc: nc}
		c.limit.Store(int64(min(t.firstTimeout, t.idleTimeout)))
		c.touch()
		close(c.ready)

		t.mu.Lock()

		if t.closed {
			t.mu.Unlock()

			_ = nc.Close()

			return
		}

		if len(t.conns) >= t.maxConns {
			t.mu.Unlock()
			t.log.Warn("SIP connection refused: too many connections", slog.String("flow", f.String()), slog.Int("limit", t.maxConns))
			c.abort()

			continue
		}

		if old := t.conns[f]; old != nil {
			old.close()
		}

		t.conns[f] = c
		t.wg.Add(1)

		go t.readTCP(c)

		t.mu.Unlock()

		t.log.Debug("SIP connection accepted", slog.String("flow", f.String()))
	}
}

func (t *Transport) readTCP(c *conn) {
	defer t.wg.Done()
	defer t.forget(c)

	sr := sip.NewStreamReader(idleReader{c: c}, t.maxSize)

	for {
		m, ka, err := sr.Next()

		var (
			perr *sip.ParseError
			terr *sip.TooLargeError
		)

		switch {
		case err == nil && m != nil:
			c.limit.Store(int64(t.idleTimeout))
			t.deliver(m, c.flow)
		case err == nil:
			if ka == sip.KeepalivePing {
				if err := t.write(c, crlf); err != nil {
					return
				}
			}
		case errors.As(err, &terr):
			t.tooLarge(c, terr)
			return
		case errors.As(err, &perr):
			c.limit.Store(int64(t.idleTimeout))
			t.malformed(err, c.flow)
		case errors.Is(err, os.ErrDeadlineExceeded):
			t.log.Debug("closing idle SIP connection", slog.String("flow", c.flow.String()))
			c.abort()

			return
		default:
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				t.log.Debug("SIP connection closed", slog.String("flow", c.flow.String()), slog.Any("error", err))
			}

			return
		}
	}
}

func (t *Transport) tooLarge(c *conn, err *sip.TooLargeError) {
	t.log.Debug("closing connection after an oversized message", slog.String("flow", c.flow.String()), slog.Any("error", err))

	req := err.Request
	if req == nil || req.Method == "ACK" {
		return
	}

	req.Flow = c.flow

	if stampVia(req) != nil {
		return
	}

	if t.write(c, tooLargeResponse(req).Bytes()) != nil {
		return
	}

	_ = c.nc.CloseWrite()
	_ = c.nc.SetReadDeadline(time.Now().Add(drainTimeout))
	_, _ = io.CopyN(io.Discard, c.nc, int64(t.maxSize))
}

func tooLargeResponse(req *sip.Request) *sip.Response {
	res := sip.NewResponse(req, 513, "")
	_ = res.Header.SetToTag(sip.NewStatelessTag())

	return res
}

func (t *Transport) forget(c *conn) {
	c.close()

	t.mu.Lock()
	if t.conns[c.flow] == c {
		delete(t.conns, c.flow)
	}
	t.mu.Unlock()
}

type idleReader struct {
	c *conn
}

func (r idleReader) Read(p []byte) (int, error) {
	for {
		idle := time.Duration(r.c.limit.Load())
		_ = r.c.nc.SetReadDeadline(r.c.lastActive().Add(idle))

		n, err := r.c.nc.Read(p)
		if n > 0 {
			r.c.touch()
			return n, nil
		}

		if errors.Is(err, os.ErrDeadlineExceeded) && time.Since(r.c.lastActive()) < idle {
			continue
		}

		return n, err
	}
}
