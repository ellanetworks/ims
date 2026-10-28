package transaction

import (
	"context"
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

	// ErrTransport is the error of a client transaction whose request the transport failed to send, or whose
	// connection was lost before a response (RFC 3261 §17.1.4, §18.4): a fatal transport error, which the TU treats
	// as a 503 (Service Unavailable), a timeout as a 408 (§8.1.3.1).
	ErrTransport = errors.New("sip/transaction: transport error")

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

	ResponseFlow func(req *sip.Request, res *sip.Response) (sip.Flow, bool, error)

	// OnServerDone is called once per server transaction, with its first final
	// response, or with a nil response if it ended without one.
	OnServerDone func(req *sip.Request, res *sip.Response, elapsed time.Duration)

	// OnListenerFailed is told of a listener of the layer that failed for good, with the error
	// (transport.Config.OnListenerFailed): the layer no longer serves local, which it can listen on again.
	OnListenerFailed func(local netip.AddrPort, err error)

	// OnConnectionLost is told of a TCP connection of flow f that ended, with why, wrapping
	// transport.ErrConnectionLost, once the client transactions waiting on it failed (transport.Config.OnConnectionLost).
	OnConnectionLost func(f sip.Flow, err error)

	T1, T2, T4 time.Duration

	Clock Clock

	// Scheduler starts the tasks of the layer and of the agents on it; GoScheduler without one.
	Scheduler Scheduler

	// Network carries the messages between the addresses the layer serves or attaches and those of the other layers
	// on it; without one, the layer has a network of its own.
	Network *Network
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
	onDone   func(req *sip.Request, res *sip.Response, elapsed time.Duration)
	onFailed func(local netip.AddrPort, err error)
	onLost   func(f sip.Flow, err error)

	t1, t2, t4, t100 time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	// locals are the addresses the layer listens on or serves, read without a lock on every send and response; they
	// change under mu, a copy replacing them.
	locals atomic.Pointer[addrSet]

	// mu guards the transactions, qmu the queues of the flows, so that the sends of a flow do not wait on the matching
	// of messages: closed changes with both held, and is read with either; mu is never taken with qmu held.
	mu      sync.Mutex
	closed  bool
	clients map[clientKey]*ClientTransaction
	servers map[serverKey]*ServerTransaction
	pending map[branchKey]*ServerTransaction

	qmu      sync.Mutex
	lanes    map[sip.Flow]*lane
	outboxes map[sip.Flow]*outbox

	// drained, while not nil, closes once the outboxes are empty, for Shutdown.
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
		t1:       cfg.T1,
		t2:       cfg.T2,
		t4:       cfg.T4,
		lanes:    make(map[sip.Flow]*lane),
		outboxes: make(map[sip.Flow]*outbox),
		clients:  make(map[clientKey]*ClientTransaction),
		servers:  make(map[serverKey]*ServerTransaction),
		pending:  make(map[branchKey]*ServerTransaction),
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

// Clock is the clock of the timers of the layer, for the timers of its users to run on the same one.
func (l *Layer) Clock() Clock {
	return l.clock
}

// Scheduler starts the tasks of the layer, for the agents on it to start theirs on the same one.
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

// Ping sends a keep-alive on flow f and waits for its answer (transport.Transport.Ping).
func (l *Layer) Ping(ctx context.Context, f sip.Flow) error {
	if l.tr == nil {
		return errors.New("sip/transaction: no transport")
	}

	return l.tr.Ping(ctx, f)
}

// Sockets are the sockets of the transport listening on local (transport.Transport.Sockets).
func (l *Layer) Sockets(local netip.AddrPort) ([]syscall.RawConn, error) {
	if l.tr == nil {
		return nil, errors.New("sip/transaction: no transport")
	}

	return l.tr.Sockets(local)
}

// addLocal adds a to the local addresses. Called with l.mu held.
func (l *Layer) addLocal(a netip.AddrPort) {
	locals := maps.Clone(*l.locals.Load())
	locals[a] = struct{}{}
	l.locals.Store(&locals)
}

// removeLocal removes a from the local addresses. Called with l.mu held.
func (l *Layer) removeLocal(a netip.AddrPort) {
	locals := maps.Clone(*l.locals.Load())
	delete(locals, a)
	l.locals.Store(&locals)
}

// listenerFailed forgets a listener the transport dropped, and tells the layer's user.
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

// connectionLost fails the client transactions waiting for a response to a request that went out on the TCP connection
// c, lost (RFC 3261 §17.1.4, §18.4), and tells the layer's user.
func (l *Layer) connectionLost(f sip.Flow, c transport.Conn, err error) {
	l.mu.Lock()

	if l.closed {
		l.mu.Unlock()
		return
	}

	clients := make([]*ClientTransaction, 0, len(l.clients))
	for _, tx := range l.clients {
		clients = append(clients, tx)
	}

	l.mu.Unlock()

	for _, tx := range clients {
		tx.mu.Lock()

		if tx.conn == c && tx.pending() {
			tx.fail(fmt.Errorf("%w: %w", ErrTransport, err))
		}

		tx.unlock()
	}

	if l.onLost != nil {
		l.onLost(f, err)
	}
}

// isLocalAddr reports whether the layer listens on a, or serves it.
func (l *Layer) isLocalAddr(a netip.AddrPort) bool {
	_, ok := (*l.locals.Load())[a]
	return ok
}

func (l *Layer) SendAck(ctx context.Context, ack *sip.Request) error {
	if ack.Method != "ACK" {
		return errNotAck(ack)
	}

	return l.sendInOrder(ctx, ack, false)
}

func (l *Layer) SendResponse(ctx context.Context, res *sip.Response) error {
	return l.sendInOrder(ctx, res, false)
}

func (l *Layer) SendOnFlow(ctx context.Context, m sip.Message) error {
	return l.sendInOrder(ctx, m, true)
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
		_ = res.Header.SetToTag(target.cancelTag())
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

func (l *Layer) Go(f func(ctx context.Context)) error {
	if !l.enter() {
		return ErrClosed
	}

	l.sched.Go(func() {
		defer l.wg.Done()

		f(l.ctx)
	})

	return nil
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
	return l.IsLocal(via.Host, via.Port)
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
