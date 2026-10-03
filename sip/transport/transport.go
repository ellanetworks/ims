package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/internal/listen"
	"github.com/ellanetworks/ims/sip/internal/sockopt"
)

const (
	MaxUDPRequest = 1300

	DefaultIdleTimeout         = time.Hour
	DefaultFirstMessageTimeout = 20 * time.Second
	DefaultDialTimeout         = 10 * time.Second
	DefaultFallbackTimeout     = 2 * time.Second
	DefaultWriteTimeout        = 10 * time.Second
	DefaultMaxConnections      = 2048

	maxDatagram   = 65535
	udpReadBuffer = 4 << 20
	readBackoff   = 50 * time.Millisecond
)

var (
	ErrClosed = errors.New("sip/transport: closed")

	ErrNoListener = errors.New("sip/transport: no listener on the local address")

	ErrUnsupportedTransport = errors.New("sip/transport: unsupported transport")
)

type Handler interface {
	HandleMessage(m sip.Message)

	HandleParseError(err *sip.ParseError)
}

type Config struct {
	Handler Handler
	Logger  *slog.Logger

	Dial func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error)

	MaxMessageSize int

	MaxConnections int

	IdleTimeout         time.Duration
	FirstMessageTimeout time.Duration
	DialTimeout         time.Duration
	FallbackTimeout     time.Duration

	LargeUDP     bool
	WriteTimeout time.Duration

	Trace func(m sip.Message, f sip.Flow, sent bool)
}

type Transport struct {
	handler      Handler
	log          *slog.Logger
	dialFunc     func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error)
	maxSize      int
	maxConns     int
	idleTimeout  time.Duration
	firstTimeout time.Duration
	dialTimeout  time.Duration
	fallback     time.Duration
	largeUDP     bool
	writeTimeout time.Duration
	trace        func(m sip.Message, f sip.Flow, sent bool)

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	closed    bool
	listeners map[netip.AddrPort]*listener
	conns     map[sip.Flow]*conn
	wg        sync.WaitGroup
}

type listener struct {
	local netip.AddrPort
	udp   *net.UDPConn
	tcp   *net.TCPListener
}

func New(cfg Config) *Transport {
	if cfg.Handler == nil {
		panic("sip/transport: nil Handler")
	}

	t := &Transport{
		handler:      cfg.Handler,
		log:          cfg.Logger,
		dialFunc:     cfg.Dial,
		maxSize:      cfg.MaxMessageSize,
		maxConns:     cfg.MaxConnections,
		idleTimeout:  cfg.IdleTimeout,
		firstTimeout: cfg.FirstMessageTimeout,
		dialTimeout:  cfg.DialTimeout,
		fallback:     cfg.FallbackTimeout,
		largeUDP:     cfg.LargeUDP,
		writeTimeout: cfg.WriteTimeout,
		trace:        cfg.Trace,
		listeners:    make(map[netip.AddrPort]*listener),
		conns:        make(map[sip.Flow]*conn),
	}

	t.ctx, t.cancel = context.WithCancel(context.Background())

	if t.log == nil {
		t.log = slog.Default()
	}

	if t.maxSize <= 0 {
		t.maxSize = sip.DefaultMaxMessageSize
	}

	if t.maxConns <= 0 {
		t.maxConns = DefaultMaxConnections
	}

	if t.idleTimeout <= 0 {
		t.idleTimeout = DefaultIdleTimeout
	}

	if t.firstTimeout <= 0 {
		t.firstTimeout = DefaultFirstMessageTimeout
	}

	if t.dialTimeout <= 0 {
		t.dialTimeout = DefaultDialTimeout
	}

	if t.fallback <= 0 {
		t.fallback = DefaultFallbackTimeout
	}

	if t.writeTimeout <= 0 {
		t.writeTimeout = DefaultWriteTimeout
	}

	if t.dialFunc == nil {
		t.dialFunc = func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error) {
			return d.DialContext(ctx, network, address)
		}
	}

	return t
}

func (t *Transport) Listen(ctx context.Context, local netip.AddrPort) (netip.AddrPort, error) {
	local = unmap(local)
	if !local.Addr().IsValid() || local.Addr().IsUnspecified() || local.Addr().Zone() != "" {
		return netip.AddrPort{}, fmt.Errorf("sip/transport: listen on %s: a specific IP address is required", local)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return netip.AddrPort{}, ErrClosed
	}

	if _, ok := t.listeners[local]; ok {
		return netip.AddrPort{}, fmt.Errorf("sip/transport: already listening on %s", local)
	}

	l, err := bind(ctx, local)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("sip/transport: listen on %s: %w", local, err)
	}

	t.listeners[l.local] = l
	t.wg.Add(2)

	go t.readUDP(l)
	go t.accept(l)

	t.log.Debug("SIP listening", slog.String("local", l.local.String()))

	return l.local, nil
}

func bind(ctx context.Context, local netip.AddrPort) (*listener, error) {
	if local.Port() != 0 {
		if err := sockopt.CheckNoListener(local); err != nil {
			return nil, fmt.Errorf("port held by another socket: %w", err)
		}
	}

	tl, udp, err := listen.Pair(ctx, local)
	if err != nil {
		return nil, err
	}

	_ = udp.SetReadBuffer(udpReadBuffer)

	return &listener{local: unmap(tl.Addr().(*net.TCPAddr).AddrPort()), udp: udp, tcp: tl}, nil
}

func (t *Transport) Send(ctx context.Context, m sip.Message) error {
	var err error

	switch m := m.(type) {
	case *sip.Request:
		err = t.sendRequest(ctx, m)
	case *sip.Response:
		err = t.sendResponse(ctx, m)
	}

	if err != nil {
		return fmt.Errorf("sip/transport: send %s: %w", m.StartLine(), err)
	}

	return nil
}

func (t *Transport) SendOnFlow(ctx context.Context, m sip.Message) error {
	if err := t.sendOnFlow(ctx, m); err != nil {
		return fmt.Errorf("sip/transport: send %s: %w", m.StartLine(), err)
	}

	return nil
}

func (t *Transport) traced(m sip.Message, f sip.Flow, sent bool) {
	if t.trace != nil {
		t.trace(m, f, sent)
	}
}

func (t *Transport) sendUDPTraced(l *listener, m sip.Message, f sip.Flow, b []byte) error {
	t.traced(m, f, true)

	return sendUDP(l, f.Remote, b)
}

func (t *Transport) sendTCPTraced(ctx context.Context, m sip.Message, f sip.Flow, b []byte, dial bool) error {
	c, err := t.conn(ctx, f, dial)
	if err != nil {
		return err
	}

	t.traced(m, f, true)

	return t.write(c, b)
}

func (t *Transport) sendOnFlow(ctx context.Context, m sip.Message) error {
	f := normalize(m.Env().Flow)
	if !f.Remote.IsValid() {
		return errors.New("no remote address in the flow")
	}

	l, err := t.listener(f)
	if err != nil {
		return err
	}

	switch f.Transport {
	case sip.UDP:
		err = t.sendUDPTraced(l, m, f, m.Bytes())
	case sip.TCP:
		ensureContentLength(m.Env())
		err = t.sendTCPTraced(ctx, m, f, m.Bytes(), true)
	}

	if err != nil {
		return err
	}

	m.Env().Flow = f

	return nil
}

func ensureContentLength(env *sip.Envelope) {
	if !env.Header.Has("Content-Length") {
		env.Header.Set("Content-Length", strconv.Itoa(len(env.Body)))
	}
}

func (t *Transport) sendRequest(ctx context.Context, r *sip.Request) error {
	if r.Flow.Transport == sip.UDP && !t.largeUDP && len(r.Bytes()) > MaxUDPRequest {
		return t.sendLargeRequest(ctx, r)
	}

	return t.sendOnFlow(ctx, r)
}

func (t *Transport) sendLargeRequest(ctx context.Context, r *sip.Request) error {
	f := normalize(r.Flow)
	if !f.Remote.IsValid() {
		return errors.New("no remote address in the flow")
	}

	l, err := t.listener(f)
	if err != nil {
		return err
	}

	saved := r.Header.Clone()

	via, err := r.Header.TopVia()
	if err != nil {
		return err
	}

	via.Transport = sip.TCP
	if err := r.Header.SetTopVia(via); err != nil {
		return err
	}

	ensureContentLength(&r.Envelope)

	tf := f
	tf.Transport = sip.TCP

	tcpCtx, cancel := context.WithTimeout(ctx, t.fallback)
	err = t.sendTCPTraced(tcpCtx, r, tf, r.Bytes(), true)

	cancel()

	if err == nil {
		r.Flow = tf
		return nil
	}

	r.Header = saved

	if ctx.Err() != nil || !tcpFailed(err) {
		return err
	}

	t.log.Debug("TCP failed, sending large request over UDP", slog.String("remote", f.Remote.String()), slog.Any("error", err))

	if err := t.sendUDPTraced(l, r, f, r.Bytes()); err != nil {
		return err
	}

	r.Flow = f

	return nil
}

// RFC 3261 §18.1.1
func tcpFailed(err error) bool {
	var ne net.Error

	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ENOPROTOOPT) ||
		errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout()
}

func (t *Transport) sendResponse(ctx context.Context, r *sip.Response) error {
	f := normalize(r.Flow)

	l, err := t.listener(f)
	if err != nil {
		return err
	}

	via, err := r.Header.TopVia()
	if err != nil {
		return err
	}

	if f.Transport == sip.TCP {
		ensureContentLength(&r.Envelope)
	}

	b := r.Bytes()

	switch f.Transport {
	case sip.UDP:
		if f.Remote, err = udpDestination(via); err != nil {
			return err
		}

		if err := t.sendUDPTraced(l, r, f, b); err != nil {
			return err
		}
	case sip.TCP:
		err := t.sendTCPTraced(ctx, r, f, b, false)
		if err != nil && ctx.Err() == nil {
			if f.Remote, err = tcpDestination(via); err != nil {
				return err
			}

			err = t.sendTCPTraced(ctx, r, f, b, true)
		}

		if err != nil {
			return err
		}
	}

	r.Flow = f

	return nil
}

func (t *Transport) listener(f sip.Flow) (*listener, error) {
	if f.Transport != sip.UDP && f.Transport != sip.TCP {
		return nil, fmt.Errorf("%w %q", ErrUnsupportedTransport, f.Transport)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil, ErrClosed
	}

	l, ok := t.listeners[f.Local]
	if !ok {
		return nil, fmt.Errorf("%w %s", ErrNoListener, f.Local)
	}

	return l, nil
}

func sendUDP(l *listener, to netip.AddrPort, b []byte) error {
	if to.Addr().Is4() != l.local.Addr().Is4() {
		return fmt.Errorf("cannot send from %s to %s", l.local, to)
	}

	_, err := l.udp.WriteToUDPAddrPort(b, to)

	return err
}

func (t *Transport) CloseFlow(f sip.Flow) {
	f = normalize(f)

	t.mu.Lock()
	defer t.mu.Unlock()

	if c := t.conns[f]; c != nil {
		c.abort()
	}
}

func (t *Transport) Close() error {
	t.mu.Lock()

	if t.closed {
		t.mu.Unlock()
		return nil
	}

	t.closed = true
	t.cancel()

	for _, l := range t.listeners {
		_ = l.tcp.Close()
		_ = l.udp.Close()
	}

	for _, c := range t.conns {
		c.close()
	}

	t.mu.Unlock()

	t.wg.Wait()

	return nil
}

func (t *Transport) readUDP(l *listener) {
	defer t.wg.Done()

	buf := make([]byte, maxDatagram)

	for {
		n, src, err := l.udp.ReadFromUDPAddrPort(buf)
		if errors.Is(err, net.ErrClosed) {
			return
		}

		if err != nil {
			t.log.Warn("UDP read failed", slog.String("local", l.local.String()), slog.Any("error", err))
			time.Sleep(readBackoff)

			continue
		}

		data := buf[:n]
		if src.Port() == 0 || sip.IsKeepalive(data) {
			continue
		}

		if isSTUN(data) {
			if res := stunBindingReply(data, src); res != nil {
				_, _ = l.udp.WriteToUDPAddrPort(res, src)
			}

			continue
		}

		f := sip.Flow{Transport: sip.UDP, Local: l.local, Remote: unmap(src)}

		m, err := sip.Parse(data)
		if err != nil {
			t.malformed(err, f)
			continue
		}

		t.deliver(m, f)
	}
}

func (t *Transport) deliver(m sip.Message, f sip.Flow) {
	m.Env().Flow = f

	t.traced(m, f, false)

	switch m := m.(type) {
	case *sip.Request:
		if err := stampVia(m); err != nil {
			t.log.Debug("dropped request without a usable Via", slog.String("flow", f.String()), slog.Any("error", err))
			return
		}
	case *sip.Response:
		if err := m.Validate(); err != nil {
			t.log.Debug("dropped invalid response", slog.String("flow", f.String()), slog.Any("error", err))
			return
		}
	}

	t.handler.HandleMessage(m)
}

func (t *Transport) malformed(err error, f sip.Flow) {
	var perr *sip.ParseError
	if !errors.As(err, &perr) || perr.Request == nil {
		t.log.Debug("dropped malformed message", slog.String("flow", f.String()), slog.Any("error", err))
		return
	}

	perr.Request.Flow = f

	if err := stampVia(perr.Request); err != nil {
		t.log.Debug("dropped malformed request without a usable Via", slog.String("flow", f.String()), slog.Any("error", err))
		return
	}

	t.handler.HandleParseError(perr)
}

func network(prefix string, a netip.Addr) string {
	if a.Is4() {
		return prefix + "4"
	}

	return prefix + "6"
}

func unmap(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

func normalize(f sip.Flow) sip.Flow {
	f.Local = unmap(f.Local)
	if f.Remote.IsValid() {
		f.Remote = unmap(f.Remote)
	}

	return f
}
