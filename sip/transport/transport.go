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
	"github.com/ellanetworks/ims/sip/internal/sockopt"
)

const (
	MaxUDPRequest = 1300

	DefaultIdleTimeout         = time.Hour
	DefaultFirstMessageTimeout = 20 * time.Second
	DefaultDialTimeout         = 10 * time.Second
	DefaultWriteTimeout        = 10 * time.Second
	DefaultMaxConnections      = 2048

	maxDatagram   = 65535
	udpReadBuffer = 4 << 20
	readBackoff   = 50 * time.Millisecond
	bindRetries   = 16
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
	WriteTimeout        time.Duration
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
	writeTimeout time.Duration

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
		writeTimeout: cfg.WriteTimeout,
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

	var (
		l   *listener
		err error
	)

	for range bindRetries {
		if l, err = bind(ctx, local); err == nil || local.Port() != 0 || !errors.Is(err, syscall.EADDRINUSE) {
			break
		}
	}

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

	lc := net.ListenConfig{Control: sockopt.ReusePort}

	tl, err := lc.Listen(ctx, network("tcp", local.Addr()), local.String())
	if err != nil {
		return nil, err
	}

	bound := unmap(tl.Addr().(*net.TCPAddr).AddrPort())

	var plain net.ListenConfig

	pc, err := plain.ListenPacket(ctx, network("udp", local.Addr()), bound.String())
	if err != nil {
		_ = tl.Close()
		return nil, err
	}

	udp := pc.(*net.UDPConn)
	_ = udp.SetReadBuffer(udpReadBuffer)

	return &listener{local: bound, udp: udp, tcp: tl.(*net.TCPListener)}, nil
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
		err = sendUDP(l, f.Remote, m.Bytes())
	case sip.TCP:
		ensureContentLength(m.Env())
		err = t.sendTCP(ctx, f, m.Bytes(), true)
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
	if r.Flow.Transport == sip.UDP && len(r.Bytes()) > MaxUDPRequest {
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

	err = t.sendTCP(ctx, tf, r.Bytes(), true)
	if err == nil {
		r.Flow = tf
		return nil
	}

	r.Header = saved

	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.ECONNRESET) && !errors.Is(err, syscall.ENOPROTOOPT) {
		return err
	}

	t.log.Debug("TCP refused, sending large request over UDP", slog.String("remote", f.Remote.String()), slog.Any("error", err))

	if err := sendUDP(l, f.Remote, r.Bytes()); err != nil {
		return err
	}

	r.Flow = f

	return nil
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

		if err := sendUDP(l, f.Remote, b); err != nil {
			return err
		}
	case sip.TCP:
		err := t.sendTCP(ctx, f, b, false)
		if err != nil && ctx.Err() == nil {
			if f.Remote, err = tcpDestination(via); err != nil {
				return err
			}

			err = t.sendTCP(ctx, f, b, true)
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
