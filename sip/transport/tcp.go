package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/internal/sockopt"
)

var errNoConn = errors.New("no connection for the flow")

var crlf = []byte("\r\n")

const drainTimeout = time.Second

type conn struct {
	flow    sip.Flow
	ready   chan struct{}
	done    chan struct{}
	nc      net.Conn
	tcp     *net.TCPConn
	err     error
	inbound bool
	slot    bool
	peers   []*x509.Certificate

	wmu      sync.Mutex
	active   atomic.Int64
	limit    atomic.Int64
	closed   atomic.Bool
	received atomic.Bool
}

func newConn(f sip.Flow) *conn {
	return &conn{flow: f, ready: make(chan struct{}), done: make(chan struct{})}
}

func (c *conn) touch() {
	c.active.Store(time.Now().UnixNano())
}

func (c *conn) lastActive() time.Time {
	return time.Unix(0, c.active.Load())
}

func (c *conn) close() {
	c.closed.Store(true)

	if c.tcp != nil {
		_ = c.tcp.Close()
	}
}

func (c *conn) abort() {
	if c.tcp != nil {
		_ = c.tcp.SetLinger(0)
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

func (t *Transport) conn(ctx context.Context, f sip.Flow, dial, request bool) (*conn, error) {
	strict := request && f.Transport == sip.TLS

	var v *verifier
	if strict {
		v = t.reuseVerifier(f)
	}

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

	if ok && strict && c.inbound && !v.accepts(c) {
		t.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrUnauthorizedConnection, f)
	}

	if !ok && f.Transport == sip.TLS {
		c = t.reusable(f, v, strict)
		ok = c != nil
	}

	switch {
	case ok:
		t.mu.Unlock()
	case !dial:
		t.mu.Unlock()
		return nil, errNoConn
	default:
		c = newConn(f)
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

	local := c.flow.Local.String()

	d := net.Dialer{
		LocalAddr: net.TCPAddrFromAddrPort(c.flow.Local),
		Timeout:   t.dialTimeout,
		Control: func(network, address string, rc syscall.RawConn) error {
			if err := sockopt.ReusePort(network, address, rc); err != nil {
				return err
			}

			if t.control != nil {
				return t.control(network, local, rc)
			}

			return nil
		},
	}

	d.SetMultipathTCP(false)

	nc, err := t.dialFunc(t.ctx, &d, network("tcp", c.flow.Remote.Addr()), c.flow.Remote.String())

	if errors.Is(err, syscall.EADDRNOTAVAIL) {
		t.awaitAccept(c)
	}

	sc := nc

	if err == nil && c.flow.Transport == sip.TLS {
		sc, err = t.clientHandshake(nc, c.flow)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if c.nc != nil {
		return
	}

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

	c.tcp = nc.(*net.TCPConn)
	c.nc = sc

	if tc, ok := sc.(*tls.Conn); ok {
		c.peers = tc.ConnectionState().PeerCertificates
	}

	c.limit.Store(int64(t.idleTimeout))
	c.touch()
	t.index(c)
	t.wg.Add(1)

	go t.readTCP(c)

	t.log.Debug("SIP connection dialed", slog.String("flow", c.flow.String()))
}

func (t *Transport) awaitAccept(c *conn) {
	timer := time.NewTimer(t.dialTimeout)
	defer timer.Stop()

	select {
	case <-c.ready:
	case <-timer.C:
	case <-t.ctx.Done():
	}
}

func (t *Transport) accept(l *listener) {
	defer t.wg.Done()

	var backoff time.Duration

	for {
		nc, err := l.tcp.AcceptTCP()
		if errors.Is(err, net.ErrClosed) {
			return
		}

		if err != nil && socketFailed(err) {
			t.fail(l, err)
			return
		}

		if err != nil {
			backoff = retryBackoff(backoff)
			t.log.Warn("SIP accept failed", slog.String("local", l.local.String()), slog.Any("error", err),
				slog.Duration("retry", backoff))

			if !t.pause(backoff) {
				return
			}

			continue
		}

		backoff = 0

		f := sip.Flow{Transport: sip.TCP, Local: l.local, Remote: unmap(nc.RemoteAddr().(*net.TCPAddr).AddrPort())}

		if !t.admit(f, nc) {
			continue
		}

		if l.tls != nil {
			f.Transport = sip.TLS
			if !t.startHandshake(l, f, nc) {
				return
			}

			continue
		}

		if !t.register(f, nc, nc, nil) {
			return
		}
	}
}

func (t *Transport) admit(f sip.Flow, nc *net.TCPConn) bool {
	if t.acceptFunc != nil && !t.acceptFunc(f.Local, f.Remote) {
		t.log.Debug("SIP connection refused: source not allowed", slog.String("flow", f.String()))

		_ = nc.SetLinger(0)
		_ = nc.Close()

		return false
	}

	a := f.Remote.Addr()

	t.mu.Lock()

	if t.maxPerAddr > 0 && t.inbound[a] >= t.maxPerAddr {
		t.mu.Unlock()
		t.log.Warn("SIP connection refused: too many connections from the address", slog.String("flow", f.String()),
			slog.Int("limit", t.maxPerAddr))

		_ = nc.SetLinger(0)
		_ = nc.Close()

		return false
	}

	t.inbound[a]++
	t.mu.Unlock()

	return true
}

func (t *Transport) release(a netip.Addr) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.releaseLocked(a)
}

func (t *Transport) releaseLocked(a netip.Addr) {
	if n := t.inbound[a]; n > 1 {
		t.inbound[a] = n - 1
	} else {
		delete(t.inbound, a)
	}
}

func (t *Transport) register(f sip.Flow, raw *net.TCPConn, nc net.Conn, peers []*x509.Certificate) bool {
	c := newConn(f)
	c.nc = nc
	c.tcp = raw
	c.inbound = true
	c.peers = peers
	c.limit.Store(int64(min(t.firstTimeout, t.idleTimeout)))
	c.touch()
	close(c.ready)

	t.mu.Lock()

	if t.closed {
		t.releaseLocked(f.Remote.Addr())
		t.mu.Unlock()

		_ = raw.Close()

		return false
	}

	if old := t.conns[f]; old != nil && old.nc == nil && !old.closed.Load() {
		if f.Transport == sip.TLS {
			t.releaseLocked(f.Remote.Addr())
			t.mu.Unlock()
			t.log.Debug("SIP TLS connection refused: a dial on the flow is pending", slog.String("flow", f.String()))
			c.abort()

			return true
		}

		old.slot = true
		t.adopt(old, raw, nc)
		t.mu.Unlock()

		return true
	}

	if len(t.conns)+int(t.handshaking.Load()) >= t.maxConns {
		t.releaseLocked(f.Remote.Addr())
		t.mu.Unlock()
		t.log.Warn("SIP connection refused: too many connections", slog.String("flow", f.String()), slog.Int("limit", t.maxConns))
		c.abort()

		return true
	}

	if old := t.conns[f]; old != nil {
		old.close()
	}

	c.slot = true
	t.conns[f] = c

	if len(peers) > 0 {
		t.index(c)
	}

	t.wg.Add(1)

	go t.readTCP(c)

	t.mu.Unlock()

	t.log.Debug("SIP connection accepted", slog.String("flow", f.String()))

	return true
}

func (t *Transport) adopt(c *conn, raw *net.TCPConn, nc net.Conn) {
	c.nc = nc
	c.tcp = raw
	c.limit.Store(int64(t.idleTimeout))
	c.touch()
	close(c.ready)
	t.wg.Add(1)

	go t.readTCP(c)

	t.log.Debug("SIP connection accepted for a dial", slog.String("flow", c.flow.String()))
}

func (t *Transport) readTCP(c *conn) {
	defer t.wg.Done()

	var cause error

	defer func() { t.forget(c, cause) }()

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
			switch ka {
			case sip.KeepalivePing:
				if err := t.write(c, crlf); err != nil {
					cause = err
					return
				}
			case sip.KeepalivePong:
				t.tcpPong(c)
			}
		case errors.As(err, &terr):
			cause = err

			t.tooLarge(c, terr)

			return
		case errors.As(err, &perr):
			c.limit.Store(int64(t.idleTimeout))
			t.malformed(err, c.flow)
		case errors.Is(err, os.ErrDeadlineExceeded):
			t.log.Debug("closing idle SIP connection", slog.String("flow", c.flow.String()))
			c.abort()

			cause = errors.New("idle")

			return
		default:
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				t.log.Debug("SIP connection closed", slog.String("flow", c.flow.String()), slog.Any("error", err))
			}

			cause = err

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

	if cw, ok := c.nc.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}

	_ = c.nc.SetReadDeadline(time.Now().Add(drainTimeout))
	_, _ = io.CopyN(io.Discard, c.nc, int64(t.maxSize))
}

func tooLargeResponse(req *sip.Request) *sip.Response {
	res := sip.NewResponse(req, 513, "")
	_ = res.Header.SetToTag(sip.NewStatelessTag())

	return res
}

func (t *Transport) forget(c *conn, cause error) {
	c.close()

	t.mu.Lock()

	if t.conns[c.flow] == c {
		delete(t.conns, c.flow)
	}

	t.unindex(c)

	if c.slot {
		c.slot = false
		t.releaseLocked(c.flow.Remote.Addr())
	}

	close(c.done)

	closed := t.closed
	t.mu.Unlock()

	if closed || t.onLost == nil {
		return
	}

	go t.onLost(c.flow, fmt.Errorf("%w: %s: %w", ErrConnectionLost, c.flow, cause))
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
			r.c.received.Store(true)
			r.c.touch()

			return n, nil
		}

		if errors.Is(err, os.ErrDeadlineExceeded) && time.Since(r.c.lastActive()) < idle {
			continue
		}

		return n, err
	}
}

type peerKey struct {
	local netip.AddrPort
	addr  netip.Addr
}

func keyOf(f sip.Flow) peerKey {
	return peerKey{local: f.Local, addr: f.Remote.Addr()}
}

func (t *Transport) index(c *conn) {
	if c.flow.Transport != sip.TLS {
		return
	}

	k := keyOf(c.flow)
	t.peers[k] = append(t.peers[k], c)
}

func (t *Transport) unindex(c *conn) {
	if c.flow.Transport != sip.TLS {
		return
	}

	k := keyOf(c.flow)

	cs := slices.DeleteFunc(t.peers[k], func(x *conn) bool { return x == c })
	if len(cs) == 0 {
		delete(t.peers, k)
	} else {
		t.peers[k] = cs
	}
}

// SIPconnect 2.0 section 17.1.1.2
// SIPconnect 2.0 section 17.2
// RFC 5922 section 7.4
func (t *Transport) reusable(f sip.Flow, v *verifier, strict bool) *conn {
	var found *conn

	for _, c := range t.peers[keyOf(f)] {
		if c.closed.Load() || c.nc == nil || strict && !v.accepts(c) {
			continue
		}

		if !c.inbound {
			return c
		}

		if found == nil {
			found = c
		}
	}

	return found
}
