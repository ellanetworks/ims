package transaction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transport"
	"go.opentelemetry.io/otel/trace"
)

// ClientHandler is the user of a client transaction. Each call's ctx is that of the request (Layer.Request), ending
// only once the layer closes: what a response causes is part of the trace of the request.
type ClientHandler interface {
	HandleResponse(ctx context.Context, res *sip.Response)

	HandleError(ctx context.Context, err error)
}

type ClientTransaction struct {
	core

	req    *sip.Request
	h      ClientHandler
	invite bool
	sent   bool
	early  []*sip.Response
	ack    *sip.Request

	cancelled bool
	prepare   func(*sip.Request)

	// ctx carries the span of the transaction, the parent of its CANCEL's, and caller the trace of the request, which
	// the handler's calls get.
	ctx    context.Context
	caller context.Context
	span   span
}

// Request starts a client transaction of req, in a span of the trace of ctx. The request carries the trace context of
// the span when its flow carries trace context (Config.TraceContext). ctx is the trace the request is part of: the
// transaction does not end with it.
func (l *Layer) Request(ctx context.Context, req *sip.Request, h ClientHandler) (*ClientTransaction, error) {
	if req.Method == "ACK" {
		return nil, ErrAck
	}

	branch, err := branchOf(req)
	if err != nil {
		return nil, err
	}

	if err := l.checkFlow(req.Flow); err != nil {
		return nil, err
	}

	req = req.Clone()

	tx := &ClientTransaction{req: req, h: h, invite: req.Method == "INVITE"}
	tx.init(l, Trying)

	if tx.invite {
		tx.state = Calling
	}

	tx.caller = l.scope(ctx)

	if h != nil {
		tx.onError = func(err error) { h.HandleError(tx.caller, err) }
	}

	tx.mu.Lock()
	defer tx.unlock()

	if err := l.addClient(clientKey{branch: branch, method: req.Method}, tx); err != nil {
		return nil, err
	}

	spanCtx, s := l.tracer.Start(ctx, req.Method, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(l.requestAttributes(req)...))

	tx.ctx, tx.span = spanCtx, span{Span: s}
	tx.finish = func(err error) { tx.span.end(nil, err, 400) }

	l.propagate(spanCtx, req)

	if !l.reliable(req.Flow) {
		tx.interval = l.t1
		tx.after(tx.interval, tx.retransmitTimer)
	}

	tx.after(64*l.t1, tx.timeoutTimer)

	out := req.Clone()

	tx.push(out.Flow, func() error { return l.send(l.ctx, out, false) }, func(err error) { tx.sentResult(out, err) })

	return tx, nil
}

func (tx *ClientTransaction) Request() *sip.Request {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	return tx.req.Clone()
}

// Cancel cancels the INVITE (RFC 3261 §9.1), the CANCEL carrying the extra header fields.
func (tx *ClientTransaction) Cancel(extra ...sip.Field) error {
	extra = slices.Clone(extra)

	return tx.CancelWith(func(cancel *sip.Request) {
		for _, f := range extra {
			cancel.Header.Insert(f.Name, f.Value)
		}
	})
}

// CancelWith cancels the INVITE (RFC 3261 §9.1), prepare, when not nil, completing the CANCEL built for it: a Reason
// (RFC 3326), or the SDP of a precondition failure (RFC 3312 §8).
func (tx *ClientTransaction) CancelWith(prepare func(cancel *sip.Request)) error {
	if !tx.invite {
		return ErrNotInvite
	}

	tx.mu.Lock()
	defer tx.unlock()

	if tx.cancelled || (tx.state != Calling && tx.state != Proceeding) {
		return nil
	}

	tx.cancelled = true
	tx.prepare = prepare

	if tx.state == Proceeding {
		tx.sendCancel()
	}

	return nil
}

// sentResult takes the outcome of sending the request: a failure toward the network is a transport error (RFC 3261
// §17.1.4, §18.4). Once sent, the request waits for its response, which can come on another connection than the one
// it went out on (§18.2.2), or for its timer.
func (tx *ClientTransaction) sentResult(sent *sip.Request, err error) {
	if err != nil {
		if networkFailure(err) {
			err = fmt.Errorf("%w: %w", ErrTransport, err)
		}

		tx.failClient(err)

		return
	}

	tx.sent = true
	tx.req = sent
	tx.reliable = tx.layer.reliable(sent.Flow)

	early := tx.early
	tx.early = nil

	for _, res := range early {
		tx.handle(res)
	}
}

// networkFailure reports whether err, of sending a request, is the network's: not the layer closing, nor a local
// failure, such as no listener on the local address, which the next hop has nothing to do with.
func networkFailure(err error) bool {
	return !errors.Is(err, ErrClosed) && !errors.Is(err, transport.ErrNoListener) &&
		!errors.Is(err, transport.ErrUnsupportedTransport)
}

// failClient fails the transaction, and tells the layer's OnClientError of it, with the state it failed in.
func (tx *ClientTransaction) failClient(err error) {
	if tx.state == Terminated {
		return
	}

	// Told before the transaction's user, in the order of the transaction's events.
	if f := tx.layer.onClientError; f != nil {
		in, req := tx.state, tx.req
		tx.emit(func() { f(req, in, err) })
	}

	tx.fail(err)
}

func (tx *ClientTransaction) sendCancel() {
	tx.after(64*tx.layer.t1, tx.cancelTimer)

	cancel, err := sip.NewCancel(tx.req)
	if err == nil {
		if tx.prepare != nil {
			tx.prepare(cancel)
		}

		_, err = tx.layer.Request(tx.ctx, cancel, nil)
	}

	if err != nil {
		tx.layer.log.Debug("CANCEL failed", slog.String("request", tx.req.StartLine()), slog.Any("error", err))
	}
}

func (tx *ClientTransaction) receive(res *sip.Response) {
	tx.mu.Lock()

	if tx.sent {
		tx.handle(res)
	} else {
		tx.early = append(tx.early, res)
	}

	tx.unlockInOrder()
}

func (tx *ClientTransaction) handle(res *sip.Response) {
	if tx.invite {
		tx.handleInvite(res)
	} else {
		tx.handleNonInvite(res)
	}
}

func (tx *ClientTransaction) handleInvite(res *sip.Response) {
	switch tx.state {
	case Calling, Proceeding:
	case Completed:
		if res.StatusCode >= 300 {
			tx.sendAck()
		}

		return
	case Accepted:
		if res.IsSuccess() {
			tx.deliver(res)
		}

		return
	default:
		return
	}

	switch {
	case res.IsProvisional():
		if res.StatusCode > 100 {
			tx.span.event(provisionalEvent, statusKey.Int(res.StatusCode))
		}

		first := tx.state == Calling
		if first {
			tx.state = Proceeding
			tx.stopTimers()
		}

		tx.deliver(res)

		if first && tx.cancelled {
			tx.sendCancel()
		}
	case res.IsSuccess():
		tx.state = Accepted
		tx.stopTimers()
		tx.span.end(res, nil, 400)
		tx.deliver(res)
		tx.after(64*tx.layer.t1, tx.end)
	default:
		tx.state = Completed
		tx.stopTimers()
		tx.span.end(res, nil, 400)
		tx.deliver(res)

		ack, err := sip.NewAck(tx.req, res)
		if err != nil {
			tx.failClient(err)
			return
		}

		tx.ack = ack
		tx.sendAck()
		tx.afterUnreliable(timerD, tx.end)
	}
}

func (tx *ClientTransaction) handleNonInvite(res *sip.Response) {
	if tx.state != Trying && tx.state != Proceeding {
		return
	}

	if res.IsProvisional() {
		tx.deliver(res)
		tx.state = Proceeding

		return
	}

	tx.span.end(res, nil, 400)
	tx.deliver(res)

	tx.state = Completed
	tx.stopTimers()
	tx.afterUnreliable(tx.layer.t4, tx.end)
}

func (tx *ClientTransaction) sendAck() {
	ack := tx.ack.Clone()
	tx.push(ack.Flow, func() error { return tx.layer.send(tx.layer.ctx, ack, true) }, tx.sendResult)
}

func (tx *ClientTransaction) sendResult(err error) {
	if err != nil {
		tx.failClient(err)
	}
}

func (tx *ClientTransaction) pending() bool {
	if tx.invite {
		return tx.state == Calling
	}

	return tx.state == Trying || tx.state == Proceeding
}

func (tx *ClientTransaction) retransmitTimer() {
	if !tx.pending() || tx.reliable {
		return
	}

	if tx.sent {
		tx.span.event(retransmissionEvent)

		req := tx.req.Clone()
		tx.push(req.Flow, func() error { return tx.layer.send(tx.layer.ctx, req, true) }, tx.sendResult)
	}

	switch {
	case tx.invite:
		tx.interval *= 2
	case tx.state == Proceeding:
		tx.interval = tx.layer.t2
	default:
		tx.interval = min(2*tx.interval, tx.layer.t2)
	}

	tx.after(tx.interval, tx.retransmitTimer)
}

func (tx *ClientTransaction) timeoutTimer() {
	if tx.pending() {
		tx.failClient(ErrTimeout)
	}
}

func (tx *ClientTransaction) cancelTimer() {
	if tx.state == Proceeding {
		tx.failClient(ErrTimeout)
	}
}

func (tx *ClientTransaction) deliver(res *sip.Response) {
	if h := tx.h; h != nil {
		ctx := tx.caller
		tx.emit(func() { h.HandleResponse(ctx, res) })
	}
}
