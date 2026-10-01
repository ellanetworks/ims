package transaction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transport"
)

const (
	DefaultT1 = 500 * time.Millisecond
	DefaultT2 = 4 * time.Second
	DefaultT4 = 5 * time.Second

	timerD      = 32 * time.Second
	defaultPort = 5060
)

var (
	ErrClosed = transport.ErrClosed

	ErrTimeout = errors.New("sip/transaction: timeout")

	ErrTerminated = errors.New("sip/transaction: transaction terminated")

	ErrFinalResponseSent = errors.New("sip/transaction: final response already sent")

	ErrNotInvite = errors.New("sip/transaction: not an INVITE transaction")

	ErrExists = errors.New("sip/transaction: transaction already exists")

	ErrAck = errors.New("sip/transaction: ACK has no client transaction")
)

type Handler interface {
	HandleRequest(tx *ServerTransaction, req *sip.Request)

	HandleCancel(tx *ServerTransaction, cancel *sip.Request)

	HandleAck(ack *sip.Request)

	HandleStrayResponse(res *sip.Response)
}

type Config struct {
	Handler Handler
	Logger  *slog.Logger

	Transport transport.Config

	Hostnames []string

	T1, T2, T4 time.Duration

	Clock Clock
}

type Layer struct {
	h         Handler
	log       *slog.Logger
	tr        *transport.Transport
	clock     Clock
	hostnames []string

	t1, t2, t4 time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	closed  bool
	locals  map[netip.AddrPort]struct{}
	clients map[clientKey]*ClientTransaction
	servers map[serverKey]*ServerTransaction
	wg      sync.WaitGroup
}

type clientKey struct {
	branch, method string
}

type serverKey struct {
	branch, sentBy, method string
}

func New(cfg Config) *Layer {
	if cfg.Handler == nil {
		panic("sip/transaction: nil Handler")
	}

	l := &Layer{
		h:         cfg.Handler,
		log:       cfg.Logger,
		clock:     cfg.Clock,
		hostnames: cfg.Hostnames,
		t1:        cfg.T1,
		t2:        cfg.T2,
		t4:        cfg.T4,
		locals:    make(map[netip.AddrPort]struct{}),
		clients:   make(map[clientKey]*ClientTransaction),
		servers:   make(map[serverKey]*ServerTransaction),
	}

	if l.log == nil {
		l.log = slog.Default()
	}

	if l.clock == nil {
		l.clock = realClock{}
	}

	if l.t1 <= 0 {
		l.t1 = DefaultT1
	}

	if l.t2 <= 0 {
		l.t2 = DefaultT2
	}

	if l.t4 <= 0 {
		l.t4 = DefaultT4
	}

	l.ctx, l.cancel = context.WithCancel(context.Background())

	tc := cfg.Transport
	tc.Handler = transportHandler{l}

	if tc.Logger == nil {
		tc.Logger = l.log
	}

	l.tr = transport.New(tc)

	return l
}

func (l *Layer) Listen(ctx context.Context, local netip.AddrPort) (netip.AddrPort, error) {
	bound, err := l.tr.Listen(ctx, local)
	if err != nil {
		return netip.AddrPort{}, err
	}

	l.mu.Lock()
	l.locals[bound] = struct{}{}
	l.mu.Unlock()

	return bound, nil
}

func (l *Layer) SendAck(ctx context.Context, ack *sip.Request) error {
	if ack.Method != "ACK" {
		return fmt.Errorf("sip/transaction: SendAck of a %s request", ack.Method)
	}

	return l.tr.Send(ctx, ack)
}

func (l *Layer) SendResponse(ctx context.Context, res *sip.Response) error {
	return l.tr.Send(ctx, res)
}

func (l *Layer) Close() error {
	l.mu.Lock()

	if l.closed {
		l.mu.Unlock()
		return nil
	}

	l.closed = true
	l.cancel()

	clients, servers := l.clients, l.servers
	l.clients, l.servers = nil, nil

	l.mu.Unlock()

	err := l.tr.Close()

	for _, tx := range clients {
		tx.shutdown()
	}

	for _, tx := range servers {
		tx.shutdown()
	}

	l.wg.Wait()

	return err
}

func (l *Layer) enter() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return false
	}

	l.wg.Add(1)

	return true
}

func (l *Layer) addClient(key clientKey, tx *ClientTransaction) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return ErrClosed
	}

	if _, ok := l.clients[key]; ok {
		return fmt.Errorf("%w: branch %s, method %s", ErrExists, key.branch, key.method)
	}

	l.clients[key] = tx
	tx.forget = func() {
		l.mu.Lock()
		if l.clients[key] == tx {
			delete(l.clients, key)
		}
		l.mu.Unlock()
	}

	return nil
}

func (l *Layer) client(key clientKey) *ClientTransaction {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.clients[key]
}

func (l *Layer) server(key serverKey) *ServerTransaction {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.servers[key]
}

func (l *Layer) addServer(key serverKey, tx *ServerTransaction) (*ServerTransaction, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil, false
	}

	if old, ok := l.servers[key]; ok {
		return old, false
	}

	l.servers[key] = tx
	tx.forget = func() {
		l.mu.Lock()
		if l.servers[key] == tx {
			delete(l.servers, key)
		}
		l.mu.Unlock()
	}

	return tx, true
}

type transportHandler struct {
	l *Layer
}

func (h transportHandler) HandleMessage(m sip.Message) {
	switch m := m.(type) {
	case *sip.Request:
		h.l.handleRequest(m)
	case *sip.Response:
		h.l.handleResponse(m)
	}
}

func (h transportHandler) HandleParseError(err *sip.ParseError) {
	h.l.log.Debug("malformed SIP request", slog.String("flow", flowString(err.Request.Flow)), slog.Any("error", err))
	h.l.reject(err.Request, 400)
}

func (l *Layer) handleRequest(req *sip.Request) {
	var serr *sip.StatusError

	if err := req.Validate(); err != nil {
		l.log.Debug("invalid SIP request", slog.String("flow", flowString(req.Flow)), slog.Any("error", err))

		if errors.As(err, &serr) {
			l.reject(req, serr.StatusCode)
		}

		return
	}

	via, _ := req.Header.TopVia()
	key := serverKey{branch: via.Branch(), sentBy: strings.ToLower(via.SentBy()), method: req.Method}

	if req.Method == "ACK" {
		key.method = "INVITE"

		if tx := l.server(key); tx != nil {
			tx.receiveAck(req)
		} else {
			l.h.HandleAck(req)
		}

		return
	}

	tx := newServer(l, req)

	tx.mu.Lock()

	found, created := l.addServer(key, tx)
	if !created {
		tx.mu.Unlock()

		if found != nil {
			found.receive()
		}

		return
	}

	var invite *ServerTransaction

	switch req.Method {
	case "CANCEL":
		key.method = "INVITE"
		invite = l.server(key)
		l.answerCancel(tx, invite)
	case "INVITE":
		tx.start()
	default:
		tx.emit(func() { l.h.HandleRequest(tx, req) })
	}

	tx.mu.Unlock()
	tx.drain()

	if invite != nil {
		invite.receiveCancel(req)
	}
}

func (l *Layer) answerCancel(tx, invite *ServerTransaction) {
	code, tag := 481, ""
	if invite != nil {
		code, tag = 200, invite.toTag()
	}

	if tag == "" {
		tag = sip.NewTag()
	}

	res := sip.NewResponse(tx.req, code, "")
	_ = res.Header.SetToTag(tag)

	if err := tx.respond(l.ctx, res); err != nil {
		l.log.Debug("response to CANCEL failed", slog.String("flow", flowString(tx.req.Flow)), slog.Any("error", err))
	}
}

func (l *Layer) reject(req *sip.Request, code int) {
	if req.Method == "ACK" {
		return
	}

	res := sip.NewResponse(req, code, "")
	_ = res.Header.SetToTag(sip.NewTag())

	if err := l.tr.Send(l.ctx, res); err != nil {
		l.log.Debug("stateless response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}

func (l *Layer) handleResponse(res *sip.Response) {
	via, _ := res.Header.TopVia()
	if !l.isLocal(via) {
		l.log.Debug("dropped response for another sent-by", slog.String("flow", flowString(res.Flow)), slog.String("sent-by", via.SentBy()))
		return
	}

	cseq, _ := res.Header.CSeq()

	tx := l.client(clientKey{branch: via.Branch(), method: cseq.Method})
	if tx == nil {
		l.h.HandleStrayResponse(res)
		return
	}

	tx.receive(res)
}

func (l *Layer) isLocal(via sip.Via) bool {
	port := via.Port
	if port == 0 {
		port = defaultPort
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if a, ok := via.Addr(); ok {
		_, ok := l.locals[netip.AddrPortFrom(a.Unmap(), port)]
		return ok
	}

	for _, h := range l.hostnames {
		if !strings.EqualFold(h, via.Host) {
			continue
		}

		for local := range l.locals {
			if local.Port() == port {
				return true
			}
		}
	}

	return false
}

func flowString(f sip.Flow) string {
	return string(f.Transport) + " " + f.Local.String() + " <-> " + f.Remote.String()
}

func isReliable(f sip.Flow) bool {
	return f.Transport == sip.TCP
}

func branchOf(req *sip.Request) (string, error) {
	via, err := req.Header.TopVia()
	if err != nil {
		return "", fmt.Errorf("sip/transaction: %w", err)
	}

	b := via.Branch()
	if len(b) <= len(sip.MagicCookie) || !strings.HasPrefix(b, sip.MagicCookie) {
		return "", fmt.Errorf("sip/transaction: top Via branch %q is not an RFC 3261 branch", b)
	}

	return b, nil
}
