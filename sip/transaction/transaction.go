package transaction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
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

	timerD = 32 * time.Second
)

var (
	ErrClosed = transport.ErrClosed

	ErrTimeout = errors.New("sip/transaction: timeout")

	ErrTerminated = errors.New("sip/transaction: transaction terminated")

	ErrFinalResponseSent = errors.New("sip/transaction: final response already sent")

	ErrNotInvite = errors.New("sip/transaction: not an INVITE transaction")

	ErrExists = errors.New("sip/transaction: transaction already exists")

	ErrAck = errors.New("sip/transaction: ACK has no client transaction")

	ErrInvalidResponse = errors.New("sip/transaction: response not allowed")
)

type Handler interface {
	HandleRequest(tx *ServerTransaction, req *sip.Request)

	HandleCancel(tx *ServerTransaction, cancel *sip.Request)

	HandleAck(ack *sip.Request)

	HandleTransactionError(tx *ServerTransaction, err error)
}

type Config struct {
	Handler Handler
	Logger  *slog.Logger

	Transport transport.Config

	Aliases []string

	Filter func(m sip.Message) error

	ResponseFlow func(req *sip.Request) (sip.Flow, bool)

	T1, T2, T4 time.Duration

	Clock Clock
}

type Layer struct {
	h        Handler
	log      *slog.Logger
	tr       *transport.Transport
	clock    Clock
	aliases  map[string]struct{}
	filter   func(m sip.Message) error
	respFlow func(req *sip.Request) (sip.Flow, bool)

	t1, t2, t4, t100 time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	closed  bool
	locals  map[netip.AddrPort]struct{}
	clients map[clientKey]*ClientTransaction
	servers map[serverKey]*ServerTransaction
	pending map[branchKey]*ServerTransaction
	wg      sync.WaitGroup
}

type clientKey struct {
	branch, method string
}

type branchKey struct {
	branch, sentBy string
}

type serverKey struct {
	branchKey

	method string
}

func New(cfg Config) *Layer {
	if cfg.Handler == nil {
		panic("sip/transaction: nil Handler")
	}

	if cfg.Transport.Handler != nil {
		panic("sip/transaction: Transport.Handler is set by the transaction layer")
	}

	l := &Layer{
		h:        cfg.Handler,
		log:      cfg.Logger,
		clock:    cfg.Clock,
		aliases:  make(map[string]struct{}),
		filter:   cfg.Filter,
		respFlow: cfg.ResponseFlow,
		t1:       cfg.T1,
		t2:       cfg.T2,
		t4:       cfg.T4,
		locals:   make(map[netip.AddrPort]struct{}),
		clients:  make(map[clientKey]*ClientTransaction),
		servers:  make(map[serverKey]*ServerTransaction),
		pending:  make(map[branchKey]*ServerTransaction),
	}

	for _, a := range cfg.Aliases {
		l.aliases[normalizeHost(a)] = struct{}{}
	}

	if l.log == nil {
		l.log = slog.Default()
	}

	if l.clock == nil {
		l.clock = SystemClock{}
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

	l.t100 = timerEReachesT2(l.t1, l.t2)

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

func (l *Layer) SendOnFlow(ctx context.Context, m sip.Message) error {
	return l.tr.SendOnFlow(ctx, m)
}

func (l *Layer) CloseFlow(f sip.Flow) {
	l.tr.CloseFlow(f)
}

func (l *Layer) Close() error {
	l.mu.Lock()

	if l.closed {
		l.mu.Unlock()
		return nil
	}

	l.closed = true
	l.mu.Unlock()

	err := l.tr.Close()

	l.mu.Lock()
	clients, servers := l.clients, l.servers
	l.clients, l.servers = nil, nil
	l.mu.Unlock()

	for _, tx := range clients {
		tx.shutdown()
	}

	for _, tx := range servers {
		tx.shutdown()
	}

	l.cancel()
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

	if key.method != "CANCEL" {
		l.pending[key.branchKey] = tx
	}

	tx.forget = func() {
		l.mu.Lock()
		defer l.mu.Unlock()

		if l.servers[key] == tx {
			delete(l.servers, key)
		}

		if l.pending[key.branchKey] == tx {
			delete(l.pending, key.branchKey)
		}
	}

	return tx, true
}

func (l *Layer) cancelTarget(key branchKey) *ServerTransaction {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.pending[key]
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
	h.l.log.Debug("malformed SIP request", slog.String("flow", err.Request.Flow.String()), slog.Any("error", err))

	if h.l.admit(err.Request) {
		h.l.reject(err.Request, 400)
	}
}

func (l *Layer) admit(m sip.Message) bool {
	if l.filter == nil {
		return true
	}

	err := l.filter(m)
	if err == nil {
		return true
	}

	l.log.Debug("SIP message refused", slog.String("message", m.StartLine()), slog.String("flow", m.Env().Flow.String()), slog.Any("error", err))

	var serr *sip.StatusError
	if req, ok := m.(*sip.Request); ok && errors.As(err, &serr) {
		l.reject(req, serr.StatusCode)
	}

	return false
}

func (l *Layer) handleRequest(req *sip.Request) {
	var serr *sip.StatusError

	if err := req.Validate(); err != nil {
		l.log.Debug("invalid SIP request", slog.String("flow", req.Flow.String()), slog.Any("error", err))

		if errors.As(err, &serr) {
			l.reject(req, serr.StatusCode)
		}

		return
	}

	if !l.admit(req) {
		return
	}

	via, _ := req.Header.TopVia()
	key := serverKey{branchKey: branchKey{branch: via.Branch(), sentBy: sentByKey(via)}, method: req.Method}

	if req.Method == "ACK" {
		key.method = "INVITE"

		tx := l.server(key)

		switch {
		case tx != nil:
			tx.receiveAck(req)
		case l.isStatelessAck(req):
			l.log.Debug("absorbed ACK to a stateless response", slog.String("flow", req.Flow.String()))
		default:
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

	var target *ServerTransaction

	if req.Method == "CANCEL" {
		target = l.cancelTarget(key.branchKey)
		l.answerCancel(tx, target)
	} else {
		tx.start()
	}

	tx.unlockInOrder()

	if target != nil && target.invite {
		target.receiveCancel(req)
	}
}

func (l *Layer) answerCancel(tx, target *ServerTransaction) {
	res := sip.NewResponse(tx.req, 481, "")
	if target != nil {
		res = sip.NewResponse(tx.req, 200, "")
		_ = res.Header.SetToTag(target.ToTag())
	}

	if err := tx.respond(res, true); err != nil {
		l.log.Debug("response to CANCEL failed", slog.String("flow", tx.flow.String()), slog.Any("error", err))
	}
}

func (l *Layer) reject(req *sip.Request, code int) {
	if req.Method == "ACK" {
		return
	}

	res := sip.NewResponse(req, code, "")
	_ = res.Header.SetToTag(sip.NewStatelessTag())

	f, exact := l.responseFlow(req)
	l.sendStateless(res, f, exact)
}

func (l *Layer) sendStateless(res *sip.Response, f sip.Flow, exact bool) {
	res.Flow = f

	send := l.tr.Send
	if exact {
		send = l.tr.SendOnFlow
	}

	l.spawn(func() {
		if err := send(l.ctx, res); err != nil {
			l.log.Debug("stateless response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}
	})
}

func (l *Layer) Go(f func(ctx context.Context)) error {
	if !l.enter() {
		return ErrClosed
	}

	go func() {
		defer l.wg.Done()

		f(l.ctx)
	}()

	return nil
}

func (l *Layer) spawn(f func()) {
	if !l.enter() {
		return
	}

	go func() {
		defer l.wg.Done()

		f()
	}()
}

func (l *Layer) isStatelessAck(ack *sip.Request) bool {
	to, err := ack.Header.To()
	return err == nil && sip.IsStatelessTag(to.Tag())
}

func (l *Layer) checkFlow(f sip.Flow) error {
	if f.Transport != sip.UDP && f.Transport != sip.TCP {
		return fmt.Errorf("sip/transaction: %w %q", transport.ErrUnsupportedTransport, f.Transport)
	}

	if !f.Remote.IsValid() {
		return errors.New("sip/transaction: no remote address in the flow")
	}

	local := netip.AddrPortFrom(f.Local.Addr().Unmap(), f.Local.Port())

	l.mu.Lock()
	_, ok := l.locals[local]
	l.mu.Unlock()

	if !ok {
		return fmt.Errorf("sip/transaction: %w %s", transport.ErrNoListener, f.Local)
	}

	return nil
}

func (l *Layer) responseFlow(req *sip.Request) (sip.Flow, bool) {
	if l.respFlow != nil {
		if f, ok := l.respFlow(req); ok {
			return f, true
		}
	}

	return req.Flow, false
}

func (l *Layer) handleResponse(res *sip.Response) {
	via, _ := res.Header.TopVia()
	if !l.isLocal(via) {
		l.log.Debug("dropped response for another sent-by", slog.String("flow", res.Flow.String()), slog.String("sent-by", via.SentBy()))
		return
	}

	if !l.admit(res) {
		return
	}

	cseq, _ := res.Header.CSeq()

	tx := l.client(clientKey{branch: via.Branch(), method: cseq.Method})
	if tx == nil {
		l.log.Debug("discarded response without a transaction", slog.String("response", res.StartLine()), slog.String("flow", res.Flow.String()))
		return
	}

	tx.receive(res)
}

func (l *Layer) isLocal(via sip.Via) bool {
	return l.IsLocal(via.Host, via.Port)
}

func (l *Layer) IsLocal(host string, port uint16) bool {
	if port == 0 {
		port = sip.DefaultPort
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if a, ok := sip.HostAddr(host); ok {
		if _, ok := l.locals[netip.AddrPortFrom(a.Unmap(), port)]; ok {
			return true
		}
	}

	if _, ok := l.aliases[normalizeHost(host)]; !ok {
		return false
	}

	for local := range l.locals {
		if local.Port() == port {
			return true
		}
	}

	return false
}

func sentByKey(via sip.Via) string {
	return normalizeHost(via.Host) + ":" + strconv.Itoa(int(sentByPort(via)))
}

func sentByPort(via sip.Via) uint16 {
	if via.Port == 0 {
		return sip.DefaultPort
	}

	return via.Port
}

func normalizeHost(host string) string {
	if a, ok := sip.HostAddr(host); ok {
		return a.Unmap().String()
	}

	return strings.ToLower(host)
}

func errInvalid(res *sip.Response, why string) error {
	return fmt.Errorf("%w: %d: %s", ErrInvalidResponse, res.StatusCode, why)
}

func timerEReachesT2(t1, t2 time.Duration) time.Duration {
	var d time.Duration

	for interval := t1; ; {
		d += interval
		if interval = min(2*interval, t2); interval >= t2 {
			return d
		}
	}
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
