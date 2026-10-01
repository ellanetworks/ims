package transaction

import (
	"context"
	"log/slog"

	"github.com/ellanetworks/ims/sip"
)

type ServerTransaction struct {
	core

	req    *sip.Request
	invite bool
	last   *sip.Response
}

func newServer(l *Layer, req *sip.Request) *ServerTransaction {
	tx := &ServerTransaction{req: req, invite: req.Method == "INVITE"}
	tx.init(l, Trying)
	tx.reliable = isReliable(req.Flow)

	if tx.invite {
		tx.state = Proceeding
	}

	return tx
}

func (tx *ServerTransaction) Request() *sip.Request {
	return tx.req
}

func (tx *ServerTransaction) Respond(ctx context.Context, res *sip.Response) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	return tx.respond(ctx, res.Clone())
}

func (tx *ServerTransaction) start() {
	trying := sip.NewResponse(tx.req, 100, "")

	if err := tx.respond(tx.layer.ctx, trying); err != nil {
		tx.layer.log.Debug("100 Trying failed", slog.String("flow", flowString(tx.req.Flow)), slog.Any("error", err))
		return
	}

	tx.emit(func() { tx.layer.h.HandleRequest(tx, tx.req) })
}

func (tx *ServerTransaction) respond(ctx context.Context, res *sip.Response) error {
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

	res.Flow = tx.req.Flow

	if err := tx.layer.tr.Send(ctx, res); err != nil {
		tx.terminate(err)
		return err
	}

	tx.last = res

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

func (tx *ServerTransaction) receive() {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.last == nil || (tx.state != Proceeding && tx.state != Completed) {
		return
	}

	tx.retransmit()
}

func (tx *ServerTransaction) receiveAck(ack *sip.Request) {
	tx.mu.Lock()

	switch tx.state {
	case Completed:
		tx.state = Confirmed
		tx.stopTimers()
		tx.afterUnreliable(tx.layer.t4, tx.end)
	case Accepted:
		tx.emit(func() { tx.layer.h.HandleAck(ack) })
	}

	tx.mu.Unlock()
	tx.drain()
}

func (tx *ServerTransaction) receiveCancel(cancel *sip.Request) {
	tx.mu.Lock()

	if tx.state == Proceeding {
		tx.emit(func() { tx.layer.h.HandleCancel(tx, cancel) })
	}

	tx.mu.Unlock()
	tx.drain()
}

func (tx *ServerTransaction) toTag() string {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.last == nil {
		return ""
	}

	to, err := tx.last.Header.To()
	if err != nil {
		return ""
	}

	return to.Tag()
}

func (tx *ServerTransaction) timerG() {
	if tx.state != Completed {
		return
	}

	if !tx.retransmit() {
		return
	}

	tx.interval = min(2*tx.interval, tx.layer.t2)
	tx.after(tx.interval, tx.timerG)
}

func (tx *ServerTransaction) timerH() {
	if tx.state == Completed {
		tx.terminate(ErrTimeout)
	}
}

func (tx *ServerTransaction) retransmit() bool {
	if err := tx.layer.tr.Send(tx.layer.ctx, tx.last); err != nil {
		tx.terminate(err)
		return false
	}

	return true
}

func (tx *ServerTransaction) shutdown() {
	tx.mu.Lock()
	tx.terminate(ErrClosed)
	tx.mu.Unlock()
}
