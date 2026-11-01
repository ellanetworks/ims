package transaction

import (
	"context"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/lifetime"
	"go.opentelemetry.io/otel/trace"
)

type ServerTransaction struct {
	core

	req    *sip.Request
	invite bool
	flow   sip.Flow
	tag    string
	trying *sip.Response
	last   *sip.Response
	may100 bool

	started time.Time
	final   bool

	// ctx is the context of the handler's calls about the transaction, cancelled by cancel once the transaction ends
	// or is cancelled; span is the transaction's.
	ctx    context.Context
	cancel context.CancelCauseFunc
	span   span
}

func newServer(l *Layer, req *sip.Request) *ServerTransaction {
	tx := &ServerTransaction{
		req: req, invite: req.Method == "INVITE", tag: sip.NewTag(), trying: sip.NewResponse(req, 100, ""), started: time.Now(),
	}
	tx.init(l, Trying)

	if l.onDone != nil {
		tx.onEnd = append(tx.onEnd, func() {
			tx.mu.Lock()
			final := tx.final
			tx.mu.Unlock()

			if !final {
				l.onDone(tx.ctx, req, nil, time.Since(tx.started))
			}
		})
	}

	tx.flow = req.Flow
	tx.reliable = l.reliable(tx.flow)
	tx.may100 = tx.invite || tx.reliable

	if req.Method != "CANCEL" {
		tx.onError = func(err error) { l.h.HandleTransactionError(tx.ctx, tx, err) }
	}

	if tx.invite {
		tx.state = Proceeding
	}

	return tx
}

// startSpan starts the span of the transaction, a child of parent, the trace context of its request, and linked to the
// valid span contexts of links: what a request that starts a trace continues, the INVITE that a CANCEL cancels. Called
// with tx.mu held.
func (tx *ServerTransaction) startSpan(parent context.Context, links []trace.SpanContext) {
	l := tx.layer

	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(l.requestAttributes(tx.req)...),
		trace.WithLinks(validLinks(links)...),
	}

	_, s := l.tracer.Start(parent, spanName(tx.req.Method), opts...)

	tx.span = span{Span: s}
	tx.ctx, tx.cancel = context.WithCancelCause(lifetime.With(l.ctx, trace.ContextWithSpan(context.Background(), s)))
	tx.finish = func(err error) {
		tx.span.end(nil, err, serverFailed)

		if err == nil {
			err = ErrTerminated
		}

		tx.cancel(err)
	}
}

// spanContext is the span context of the transaction, if it has one.
func (tx *ServerTransaction) spanContext() trace.SpanContext {
	if tx == nil {
		return trace.SpanContext{}
	}

	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.span.Span == nil {
		return trace.SpanContext{}
	}

	return tx.span.SpanContext()
}

func (tx *ServerTransaction) Request() *sip.Request {
	return tx.req
}

func (tx *ServerTransaction) ToTag() string {
	return tx.tag
}

// RFC 3261 §9.2
func (tx *ServerTransaction) cancelTag() string {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.last != nil {
		if to, err := tx.last.Header.To(); err == nil && to.Tag() != "" {
			return to.Tag()
		}
	}

	return tx.tag
}

// Status is the status code of the final response the transaction sent, or 0 if it has sent none.
func (tx *ServerTransaction) Status() int {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if !tx.final {
		return 0
	}

	return tx.last.StatusCode
}

func (tx *ServerTransaction) Respond(res *sip.Response) error {
	res = res.Clone()

	tx.mu.Lock()
	defer tx.unlock()

	return tx.respond(res, res.StatusCode > 100)
}

func (tx *ServerTransaction) Relay(res *sip.Response) error {
	res = res.Clone()

	tx.mu.Lock()
	defer tx.unlock()

	if tx.invite && res.IsSuccess() && tx.state != Proceeding && tx.state != Accepted {
		tx.layer.sendStateless(tx.req, res)
		return nil
	}

	return tx.respond(res, res.StatusCode >= 300)
}

func (tx *ServerTransaction) start() {
	if tx.invite {
		_ = tx.respond(tx.trying, false)
	} else {
		tx.after(tx.layer.t100, tx.timer100)
		tx.after(64*tx.layer.t1, tx.expire)
	}

	tx.emit(func() { tx.layer.h.HandleRequest(tx.ctx, tx, tx.req) })
}

func (tx *ServerTransaction) respond(res *sip.Response, tag bool) error {
	switch tx.state {
	case Terminated:
		return ErrTerminated
	case Completed, Confirmed:
		return ErrFinalResponseSent
	case Accepted:
		if !res.IsSuccess() {
			return ErrFinalResponseSent
		}
	}

	if !tx.invite {
		if err := tx.checkNonInvite(res); err != nil {
			return err
		}
	}

	if tag {
		if err := res.Header.SetToTag(tx.tag); err != nil {
			return err
		}
	}

	tx.last = res
	tx.transmit()

	if res.IsProvisional() && res.StatusCode > 100 {
		tx.span.event(provisionalEvent, statusKey.Int(res.StatusCode))
	}

	if !res.IsProvisional() && !tx.final {
		tx.final = true
		tx.span.end(res, nil, serverFailed)

		if f := tx.layer.onDone; f != nil {
			elapsed := time.Since(tx.started)
			tx.emit(func() { f(tx.ctx, tx.req, res, elapsed) })
		}
	}

	switch {
	case tx.state == Accepted:
	case res.IsProvisional():
		tx.state = Proceeding
	case tx.invite && res.IsSuccess():
		tx.state = Accepted
		tx.after(64*tx.layer.t1, tx.end)
	case tx.invite:
		tx.state = Completed

		if !tx.reliable {
			tx.interval = tx.layer.t1
			tx.after(tx.interval, tx.timerG)
		}

		tx.after(64*tx.layer.t1, tx.timerH)
	default:
		tx.state = Completed
		tx.afterUnreliable(64*tx.layer.t1, tx.end)
	}

	return nil
}

func (tx *ServerTransaction) checkNonInvite(res *sip.Response) error {
	switch {
	case res.StatusCode == 408:
		return errInvalid(res, "408 to a non-INVITE request (RFC 4320)")
	case res.IsProvisional() && res.StatusCode != 100:
		return errInvalid(res, "provisional response other than 100 to a non-INVITE request (RFC 4320)")
	case res.StatusCode == 100 && !tx.may100:
		return errInvalid(res, "100 to a non-INVITE request over UDP before Timer E reaches T2 (RFC 4320)")
	}

	return nil
}

func (tx *ServerTransaction) transmit() {
	res := tx.last.Clone()

	f, exact, err := tx.layer.responseFlow(tx.req, res)
	if err != nil {
		tx.report(err)
		return
	}

	res.Flow = f

	tx.push(f, func() error { return tx.layer.send(tx.layer.ctx, res, exact) }, func(err error) {
		if err != nil {
			tx.report(err)
		}
	})
}

func (tx *ServerTransaction) receive() {
	tx.mu.Lock()

	tx.span.event(retransmissionEvent)

	if tx.last != nil && tx.sending == 0 && (tx.state == Proceeding || tx.state == Completed) {
		tx.transmit()
	}

	tx.unlockInOrder()
}

func (tx *ServerTransaction) receiveAck(ack *sip.Request) {
	tx.mu.Lock()

	switch tx.state {
	case Completed:
		tx.state = Confirmed
		tx.stopTimers()
		tx.afterUnreliable(tx.layer.t4, tx.end)
	case Accepted:
		var invite trace.SpanContext
		if tx.span.Span != nil {
			invite = tx.span.SpanContext()
		}

		tx.emit(func() { tx.layer.handleAck(ack, invite) })
	}

	tx.unlockInOrder()
}

// receiveCancel tells the handler of a CANCEL of the transaction, ctx carrying the CANCEL's span.
func (tx *ServerTransaction) receiveCancel(ctx context.Context, cancel *sip.Request) {
	tx.mu.Lock()

	if tx.state == Proceeding {
		// The client gave up: the work done for the request is no longer wanted (RFC 3261 §9.2).
		tx.cancel(ErrCanceled)
		tx.emit(func() { tx.layer.h.HandleCancel(ctx, tx, cancel) })
	}

	tx.unlockInOrder()
}

func (tx *ServerTransaction) timer100() {
	tx.may100 = true

	if tx.state == Trying {
		_ = tx.respond(tx.trying, false)
	}
}

func (tx *ServerTransaction) expire() {
	if tx.state == Trying || tx.state == Proceeding {
		tx.fail(ErrTimeout)
	}
}

func (tx *ServerTransaction) timerG() {
	if tx.state != Completed {
		return
	}

	if tx.sending == 0 {
		tx.transmit()
	}

	tx.interval = min(2*tx.interval, tx.layer.t2)
	tx.after(tx.interval, tx.timerG)
}

func (tx *ServerTransaction) timerH() {
	if tx.state == Completed {
		tx.fail(ErrTimeout)
	}
}
