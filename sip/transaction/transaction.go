package transaction

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transport"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
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

	// RFC 3261 section 17.1.4
	// RFC 3261 section 18.4
	// RFC 3261 section 8.1.3.1
	ErrTransport = errors.New("sip/transaction: transport error")

	ErrDropped = errors.New("sip/transaction: dropped")

	ErrTerminated = errors.New("sip/transaction: transaction terminated")
	ErrCanceled   = errors.New("sip/transaction: canceled")

	ErrFinalResponseSent = errors.New("sip/transaction: final response already sent")

	ErrNotInvite = errors.New("sip/transaction: not an INVITE transaction")

	ErrExists = errors.New("sip/transaction: transaction already exists")

	ErrAck = errors.New("sip/transaction: ACK has no client transaction")

	ErrInvalidResponse = errors.New("sip/transaction: response not allowed")
)

type Handler interface {
	HandleRequest(ctx context.Context, tx *ServerTransaction, req *sip.Request)

	HandleCancel(ctx context.Context, tx *ServerTransaction, cancel *sip.Request)

	HandleAck(ctx context.Context, ack *sip.Request)

	HandleTransactionError(ctx context.Context, tx *ServerTransaction, err error)
}

type Config struct {
	Handler Handler
	Logger  *slog.Logger

	Transport transport.Config

	Aliases []string

	Filter func(m sip.Message) error

	ResponseFlow func(req *sip.Request, res *sip.Response) (sip.Flow, bool, error)

	OnServerDone func(ctx context.Context, req *sip.Request, res *sip.Response, elapsed time.Duration)

	OnListenerFailed func(local netip.AddrPort, err error)

	OnClientError func(req *sip.Request, in State, err error)

	OnConnectionLost func(f sip.Flow, err error)

	T1, T2, T4 time.Duration

	Clock Clock

	Scheduler Scheduler

	Network *Network

	TracerProvider trace.TracerProvider

	TraceContext func(f sip.Flow) bool

	SpanAttributes func(req *sip.Request, kind trace.SpanKind) []attribute.KeyValue

	SpanLinks func(req *sip.Request) []trace.SpanContext

	Untraced func(req *sip.Request) bool
}

type Layer struct {
	h        Handler
	log      *slog.Logger
	tr       *transport.Transport
	clock    Clock
	sched    Scheduler
	network  *Network
	aliases  map[string]struct{}
	filter   func(m sip.Message) error
	respFlow func(req *sip.Request, res *sip.Response) (sip.Flow, bool, error)
	onDone   func(ctx context.Context, req *sip.Request, res *sip.Response, elapsed time.Duration)
	onFailed func(local netip.AddrPort, err error)
	onLost   func(f sip.Flow, err error)

	onClientError func(req *sip.Request, in State, err error)

	tp           trace.TracerProvider
	tracer       trace.Tracer
	traceContext func(f sip.Flow) bool
	attributes   func(req *sip.Request, kind trace.SpanKind) []attribute.KeyValue
	spanLinks    func(req *sip.Request) []trace.SpanContext
	skipSpan     func(req *sip.Request) bool

	t1, t2, t4, t100 time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	locals atomic.Pointer[addrSet]

	mu      sync.Mutex
	closed  bool
	clients map[clientKey]*ClientTransaction
	servers map[serverKey]*ServerTransaction
	pending map[branchKey]*ServerTransaction

	qmu      sync.Mutex
	lanes    map[sip.Flow]*lane
	outboxes map[sip.Flow]*outbox

	drained chan struct{}
	wg      sync.WaitGroup
}

type addrSet map[netip.AddrPort]struct{}

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

	if cfg.Transport.OnListenerFailed != nil {
		panic("sip/transaction: Transport.OnListenerFailed is set by the transaction layer; set OnListenerFailed")
	}

	l := &Layer{
		h:        cfg.Handler,
		log:      cfg.Logger,
		clock:    cfg.Clock,
		sched:    cfg.Scheduler,
		network:  cfg.Network,
		aliases:  make(map[string]struct{}),
		filter:   cfg.Filter,
		respFlow: cfg.ResponseFlow,
		onDone:   cfg.OnServerDone,
		onFailed: cfg.OnListenerFailed,
		onLost:   cfg.OnConnectionLost,

		onClientError: cfg.OnClientError,
		tp:            providerOf(cfg.TracerProvider),
		traceContext:  cfg.TraceContext,
		attributes:    cfg.SpanAttributes,
		spanLinks:     cfg.SpanLinks,
		skipSpan:      cfg.Untraced,
		t1:            cfg.T1,
		t2:            cfg.T2,
		t4:            cfg.T4,
		lanes:         make(map[sip.Flow]*lane),
		outboxes:      make(map[sip.Flow]*outbox),
		clients:       make(map[clientKey]*ClientTransaction),
		servers:       make(map[serverKey]*ServerTransaction),
		pending:       make(map[branchKey]*ServerTransaction),
	}

	l.locals.Store(&addrSet{})

	for _, a := range cfg.Aliases {
		l.aliases[normalizeHost(a)] = struct{}{}
	}

	if l.log == nil {
		l.log = slog.Default()
	}

	if l.clock == nil {
		l.clock = SystemClock{}
	}

	if l.sched == nil {
		l.sched = GoScheduler{}
	}

	if l.network == nil {
		l.network = NewNetwork()
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
	l.tracer = l.tp.Tracer(tracerName, trace.WithSchemaURL(semconv.SchemaURL))

	l.ctx, l.cancel = context.WithCancel(context.Background())

	tc := cfg.Transport
	tc.Handler = transportHandler{l}
	tc.OnListenerFailed = l.listenerFailed
	tc.OnConnectionLost = l.connectionLost

	if tc.Logger == nil {
		tc.Logger = l.log
	}

	l.tr = transport.New(tc)

	return l
}

func (l *Layer) Clock() Clock {
	return l.clock
}

func (l *Layer) Scheduler() Scheduler {
	return l.sched
}

func (l *Layer) T2() time.Duration {
	return l.t2
}

func (l *Layer) T1() time.Duration {
	return l.t1
}

func (l *Layer) Listen(ctx context.Context, local netip.AddrPort) (netip.AddrPort, error) {
	bound, err := l.tr.Listen(ctx, local)
	if err != nil {
		return netip.AddrPort{}, err
	}

	l.mu.Lock()
	l.addLocal(bound)
	l.mu.Unlock()

	return bound, nil
}

func (l *Layer) ListenTLS(ctx context.Context, local netip.AddrPort, cfg *tls.Config) (netip.AddrPort, error) {
	bound, err := l.tr.ListenTLS(ctx, local, cfg)
	if err != nil {
		return netip.AddrPort{}, err
	}

	l.mu.Lock()
	l.addLocal(bound)
	l.mu.Unlock()

	return bound, nil
}

func (l *Layer) ClientTLS(ctx context.Context, local netip.AddrPort) (netip.AddrPort, error) {
	bound, err := l.tr.ClientTLS(ctx, local)
	if err != nil {
		return netip.AddrPort{}, err
	}

	l.mu.Lock()
	l.addLocal(bound)
	l.mu.Unlock()

	return bound, nil
}

func (l *Layer) Persist(targets transport.Targets, ka transport.KeepAlive) (func(), error) {
	if l.tr == nil {
		return nil, errors.New("sip/transaction: no transport")
	}

	return l.tr.Persist(targets, ka)
}

func (l *Layer) Ping(ctx context.Context, f sip.Flow) error {
	if l.tr == nil {
		return errors.New("sip/transaction: no transport")
	}

	return l.tr.Ping(ctx, f)
}

func (l *Layer) Sockets(local netip.AddrPort) ([]syscall.RawConn, error) {
	if l.tr == nil {
		return nil, errors.New("sip/transaction: no transport")
	}

	return l.tr.Sockets(local)
}

func (l *Layer) addLocal(a netip.AddrPort) {
	locals := maps.Clone(*l.locals.Load())
	locals[a] = struct{}{}
	l.locals.Store(&locals)
}

func (l *Layer) removeLocal(a netip.AddrPort) {
	locals := maps.Clone(*l.locals.Load())
	delete(locals, a)
	l.locals.Store(&locals)
}

func (l *Layer) listenerFailed(local netip.AddrPort, err error) {
	l.mu.Lock()

	if l.closed {
		l.mu.Unlock()
		return
	}

	l.removeLocal(local)
	l.mu.Unlock()

	if l.onFailed != nil {
		l.onFailed(local, err)
	}
}

// RFC 3261 section 18.2.2
func (l *Layer) connectionLost(f sip.Flow, err error) {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()

	if !closed && l.onLost != nil {
		l.onLost(f, err)
	}
}

// TS 24.229 clause 5.1.1.4.1
func (l *Layer) Drop(match func(sip.Flow) bool) {
	l.mu.Lock()

	if l.closed {
		l.mu.Unlock()
		return
	}

	clients := make([]*ClientTransaction, 0, len(l.clients))
	for _, tx := range l.clients {
		clients = append(clients, tx)
	}

	servers := make([]*ServerTransaction, 0, len(l.servers))
	for _, tx := range l.servers {
		servers = append(servers, tx)
	}

	l.mu.Unlock()

	for _, tx := range clients {
		tx.mu.Lock()

		if tx.state != Terminated && match(tx.req.Flow) {
			tx.dropped = true
			tx.failClient(ErrDropped)
		}

		tx.unlock()
	}

	for _, tx := range servers {
		tx.mu.Lock()

		if tx.state != Terminated && match(tx.req.Flow) {
			tx.dropped = true
			tx.fail(ErrDropped)
		}

		tx.unlock()
	}
}

func (l *Layer) isLocalAddr(a netip.AddrPort) bool {
	_, ok := (*l.locals.Load())[a]
	return ok
}

func (l *Layer) SendAck(ctx context.Context, ack *sip.Request) error {
	if ack.Method != "ACK" {
		return errNotAck(ack)
	}

	ack = ack.Clone()
	l.propagate(ctx, ack)

	return l.sendInOrder(ack, false)
}

func (l *Layer) SendResponse(_ context.Context, res *sip.Response) error {
	return l.sendInOrder(res, false)
}

func (l *Layer) SendOnFlow(_ context.Context, m sip.Message) error {
	return l.sendInOrder(m, true)
}

func (l *Layer) CloseFlow(f sip.Flow) {
	if l.inMemory(f.Remote) {
		return
	}

	l.tr.CloseFlow(f)
}

func (l *Layer) Close() error {
	l.mu.Lock()

	if l.closed {
		l.mu.Unlock()
		return nil
	}

	l.qmu.Lock()
	l.closed = true
	l.qmu.Unlock()
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

	l.network.unbind(l)

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
		l.reject(req, serr.StatusCode, serr.Header...)
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
			l.handleAck(req, trace.SpanContext{})
		}

		return
	}

	parent, links := l.parent(req)

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
		tx.startSpan(parent, append(links, target.spanContext()))
		l.answerCancel(tx, target)
	} else {
		tx.startSpan(parent, links)
		tx.start()
	}

	ctx := tx.ctx

	tx.unlockInOrder()

	if target != nil && target.invite {
		target.receiveCancel(ctx, req)
	}
}

func (l *Layer) answerCancel(tx, target *ServerTransaction) {
	res := sip.NewResponse(tx.req, 481, "")
	if target != nil {
		res = sip.NewResponse(tx.req, 200, "")
		_ = res.Header.SetToTag(target.cancelTag())
	}

	if err := tx.respond(res, true); err != nil {
		l.log.Debug("response to CANCEL failed", slog.String("flow", tx.flow.String()), slog.Any("error", err))
	}
}

func (l *Layer) reject(req *sip.Request, code int, fields ...sip.Field) {
	if req.Method == "ACK" {
		return
	}

	res := sip.NewResponse(req, code, "")

	for _, f := range fields {
		res.Header.Add(f.Name, f.Value)
	}

	_ = res.Header.SetToTag(sip.NewStatelessTag())

	l.sendStateless(req, res)
}

func (l *Layer) sendStateless(req *sip.Request, res *sip.Response) {
	f, exact, err := l.responseFlow(req, res)
	if err != nil {
		l.log.Debug("stateless response dropped", slog.String("response", res.StartLine()), slog.Any("error", err))
		return
	}

	res.Flow = f

	l.enqueue(f, func(ok bool) {
		if !ok {
			return
		}

		if err := l.send(l.ctx, res, exact); err != nil {
			l.log.Debug("stateless response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}
	})
}

func (l *Layer) Go(ctx context.Context, f func(ctx context.Context)) error {
	if !l.enter() {
		return ErrClosed
	}

	ctx = l.scope(ctx)

	l.sched.Go(func() {
		defer l.wg.Done()

		f(ctx)
	})

	return nil
}

func (l *Layer) isStatelessAck(ack *sip.Request) bool {
	to, err := ack.Header.To()
	return err == nil && sip.IsStatelessTag(to.Tag())
}

func (l *Layer) checkFlow(f sip.Flow) error {
	if f.Transport != sip.UDP && f.Transport != sip.TCP && f.Transport != sip.TLS {
		return fmt.Errorf("sip/transaction: %w %q", transport.ErrUnsupportedTransport, f.Transport)
	}

	if !f.Remote.IsValid() {
		return errors.New("sip/transaction: no remote address in the flow")
	}

	if !l.isLocalAddr(unmap(f.Local)) {
		return fmt.Errorf("sip/transaction: %w %s", transport.ErrNoListener, f.Local)
	}

	return nil
}

func (l *Layer) responseFlow(req *sip.Request, res *sip.Response) (sip.Flow, bool, error) {
	if l.respFlow != nil {
		f, ok, err := l.respFlow(req, res)
		if err != nil {
			return sip.Flow{}, false, err
		}

		if ok {
			return f, true, nil
		}
	}

	return req.Flow, false, nil
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
	return l.IsLocal(via.Host, sentByPort(via))
}

func (l *Layer) IsLocal(host string, port uint16) bool {
	if port == 0 {
		port = sip.DefaultPort
	}

	if a, ok := sip.HostAddr(host); ok && l.isLocalAddr(netip.AddrPortFrom(a.Unmap(), port)) {
		return true
	}

	if _, ok := l.aliases[normalizeHost(host)]; !ok {
		return false
	}

	for local := range *l.locals.Load() {
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
		return sip.DefaultPortFor(via.Transport)
	}

	return via.Port
}

func normalizeHost(host string) string {
	if a, ok := sip.HostAddr(host); ok {
		return a.Unmap().String()
	}

	return strings.ToLower(host)
}

func errNotAck(req *sip.Request) error {
	return fmt.Errorf("sip/transaction: SendAck of a %s request", req.Method)
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
