package listener

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/pki"
)

const (
	ALPNRaft = "ims-raft/1"
	ALPNHTTP = "ims-http/1"
	ALPNJoin = "ims-join/1"

	handshakeTimeout = 10 * time.Second
)

type Pins func(fingerprint string) (nodeID string, ok bool)

const AnyNode = ""

type Removed func(fingerprint string) bool

type Config struct {
	Address string
	Cert    func() *tls.Certificate
	Pins    Pins
	Removed Removed
	Logger  *slog.Logger
}

type Listener struct {
	cfg      Config
	tls      *tls.Config
	ln       net.Listener
	mu       sync.Mutex
	handlers map[string]func(net.Conn)
	conns    map[*tls.Conn]string
	fenced   map[*tls.Conn]bool
	closed   bool
	wg       sync.WaitGroup
}

func New(cfg Config) *Listener {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	l := &Listener{
		cfg: cfg, handlers: map[string]func(net.Conn){}, conns: map[*tls.Conn]string{}, fenced: map[*tls.Conn]bool{},
	}
	l.tls = &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: tls.RequestClientCert,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			if c := cfg.Cert(); c != nil {
				return c, nil
			}

			return nil, errors.New("no cluster certificate yet")
		},
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			if c := cfg.Cert(); c != nil {
				return c, nil
			}

			return &tls.Certificate{}, nil
		},
		NextProtos:         []string{ALPNRaft, ALPNHTTP, ALPNJoin},
		InsecureSkipVerify: true, //nolint:gosec
		VerifyConnection: func(cs tls.ConnectionState) error {
			if cs.NegotiatedProtocol == ALPNJoin {
				return nil
			}

			_, err := verify(cs, cfg.Pins)
			if err != nil && cfg.removed(cs) {
				return nil
			}

			return err
		},
	}

	return l
}

func (c Config) removed(cs tls.ConnectionState) bool {
	return c.Removed != nil && len(cs.PeerCertificates) > 0 && c.Removed(pki.Fingerprint(cs.PeerCertificates[0]))
}

func verify(cs tls.ConnectionState, pins Pins) (string, error) {
	if len(cs.PeerCertificates) == 0 {
		return "", errors.New("cluster TLS: the peer presented no certificate")
	}

	leaf := cs.PeerCertificates[0]
	fp := pki.Fingerprint(leaf)

	owner, ok := pins(fp)
	if !ok {
		return "", fmt.Errorf("cluster TLS: certificate %s is not pinned", fp)
	}

	_, nodeID, err := pki.Identity(leaf)
	if err != nil {
		return "", fmt.Errorf("cluster TLS: %w", err)
	}

	if owner != AnyNode && nodeID != owner {
		return "", fmt.Errorf("cluster TLS: certificate of node %s is pinned to node %s", nodeID, owner)
	}

	return nodeID, nil
}

func (l *Listener) Handle(alpn string, h func(net.Conn)) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.handlers[alpn] = h
}

func (l *Listener) Start(ctx context.Context) error {
	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", l.cfg.Address)
	if err != nil {
		return fmt.Errorf("listen for the cluster: %w", err)
	}

	l.mu.Lock()
	l.ln = ln
	l.mu.Unlock()

	l.wg.Go(func() { l.accept(ln) })

	return nil
}

func (l *Listener) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.ln == nil {
		return nil
	}

	return l.ln.Addr()
}

func (l *Listener) accept(ln net.Listener) {
	delay := 5 * time.Millisecond

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}

			l.cfg.Logger.Warn("cluster accept failed", slog.Any("error", err))
			time.Sleep(delay)

			delay = min(2*delay, time.Second)

			continue
		}

		delay = 5 * time.Millisecond

		l.wg.Go(func() { l.serve(conn) })
	}
}

func (l *Listener) serve(raw net.Conn) {
	conn := tls.Server(raw, l.tls)

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	err := conn.HandshakeContext(ctx)

	cancel()

	if err != nil {
		l.cfg.Logger.Debug("cluster handshake failed", slog.String("remote", raw.RemoteAddr().String()),
			slog.Any("error", err))

		_ = raw.Close()

		return
	}

	cs := conn.ConnectionState()

	removed, rejected := false, false

	if cs.NegotiatedProtocol != ALPNJoin {
		if _, err := verify(cs, l.cfg.Pins); err != nil {
			removed, rejected = l.cfg.removed(cs), !l.cfg.removed(cs)
		}
	}

	l.mu.Lock()
	h, ok := l.handlers[cs.NegotiatedProtocol]

	fp := ""
	if len(cs.PeerCertificates) > 0 && cs.NegotiatedProtocol != ALPNJoin && !removed {
		fp = pki.Fingerprint(cs.PeerCertificates[0])
	}

	if rejected || removed && cs.NegotiatedProtocol != ALPNHTTP {
		ok = false
	}

	if ok && !l.closed {
		l.conns[conn] = fp
		if removed {
			l.fenced[conn] = true
		}
	} else {
		ok = false
	}
	l.mu.Unlock()

	if !ok {
		_ = conn.Close()
		return
	}

	h(&tracked{Conn: conn, l: l})
}

type tracked struct {
	*tls.Conn
	l    *Listener
	once sync.Once
}

func (t *tracked) Close() error {
	t.once.Do(func() {
		t.l.mu.Lock()
		delete(t.l.conns, t.Conn)
		delete(t.l.fenced, t.Conn)
		t.l.mu.Unlock()
	})

	return t.Conn.Close()
}

func (l *Listener) Revalidate() {
	l.mu.Lock()

	var stale []*tls.Conn

	for c, fp := range l.conns {
		if fp == "" {
			continue
		}

		if _, ok := l.cfg.Pins(fp); !ok {
			stale = append(stale, c)
		}
	}
	l.mu.Unlock()

	for _, c := range stale {
		_ = c.Close()
	}
}

func (l *Listener) Dial(ctx context.Context, addr, alpn string, pins Pins) (*tls.Conn, string, error) {
	if pins == nil {
		pins = l.cfg.Pins
	}

	var peer string

	cfg := l.tls.Clone()
	cfg.NextProtos = []string{alpn}
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		id, err := verify(cs, pins)
		peer = id

		return err
	}

	d := tls.Dialer{NetDialer: &net.Dialer{}, Config: cfg}

	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("dial %s: %w", addr, err)
	}

	tc, _ := conn.(*tls.Conn)
	if tc.ConnectionState().NegotiatedProtocol != alpn {
		_ = tc.Close()
		return nil, "", fmt.Errorf("dial %s: the peer does not speak %s", addr, alpn)
	}

	return tc, peer, nil
}

func Fenced(conn net.Conn) bool {
	t, ok := conn.(*tracked)
	if !ok {
		return false
	}

	t.l.mu.Lock()
	defer t.l.mu.Unlock()

	return t.l.fenced[t.Conn]
}

func State(conn net.Conn) (tls.ConnectionState, bool) {
	switch c := conn.(type) {
	case *tracked:
		return c.ConnectionState(), true
	case *tls.Conn:
		return c.ConnectionState(), true
	}

	return tls.ConnectionState{}, false
}

func PeerNodeID(conn net.Conn) string {
	var tc *tls.Conn

	switch c := conn.(type) {
	case *tracked:
		tc = c.Conn
	case *tls.Conn:
		tc = c
	default:
		return ""
	}

	cs := tc.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return ""
	}

	_, id, err := pki.Identity(cs.PeerCertificates[0])
	if err != nil {
		return ""
	}

	return id
}

func (l *Listener) Close() {
	l.mu.Lock()
	l.closed = true
	ln := l.ln

	conns := make([]*tls.Conn, 0, len(l.conns))
	for c := range l.conns {
		conns = append(conns, c)
	}
	l.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}

	for _, c := range conns {
		_ = c.Close()
	}

	l.wg.Wait()
}
