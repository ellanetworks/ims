package transaction

import (
	"context"
	"log/slog"

	"github.com/ellanetworks/ims/sip"
)

type ClientHandler interface {
	HandleResponse(res *sip.Response)

	HandleError(err error)
}

type ClientTransaction struct {
	core

	req    *sip.Request
	h      ClientHandler
	invite bool
	ack    *sip.Request

	cancelled, cancelSent bool
}

func (l *Layer) Request(ctx context.Context, req *sip.Request, h ClientHandler) (*ClientTransaction, error) {
	if req.Method == "ACK" {
		return nil, ErrAck
	}

	branch, err := branchOf(req)
	if err != nil {
		return nil, err
	}

	req = req.Clone()

	tx := &ClientTransaction{req: req, h: h, invite: req.Method == "INVITE"}
	tx.init(l, Trying)

	if tx.invite {
		tx.state = Calling
	}

	tx.mu.Lock()
	defer tx.mu.Unlock()

	if err := l.addClient(clientKey{branch: branch, method: req.Method}, tx); err != nil {
		return nil, err
	}

	if err := l.tr.Send(ctx, req); err != nil {
		tx.terminate(err)
		return nil, err
	}

	tx.reliable = isReliable(req.Flow)

	if !tx.reliable {
		tx.interval = l.t1
		tx.after(tx.interval, tx.retransmitTimer)
	}

	tx.after(64*l.t1, tx.timeoutTimer)

	return tx, nil
}

func (tx *ClientTransaction) Request() *sip.Request {
	return tx.req
}

func (tx *ClientTransaction) Cancel(ctx context.Context) error {
	if !tx.invite {
		return ErrNotInvite
	}

	tx.mu.Lock()

	if tx.cancelled || (tx.state != Calling && tx.state != Proceeding) {
		tx.mu.Unlock()
		return nil
	}

	tx.cancelled = true

	if tx.state == Calling {
		tx.mu.Unlock()
		return nil
	}

	tx.cancelSent = true
	tx.after(64*tx.layer.t1, tx.cancelTimer)
	tx.mu.Unlock()

	return tx.sendCancel(ctx)
}

func (tx *ClientTransaction) sendCancel(ctx context.Context) error {
	cancel, err := sip.NewCancel(tx.req)
	if err != nil {
		return err
	}

	_, err = tx.layer.Request(ctx, cancel, nil)

	return err
}

func (tx *ClientTransaction) receive(res *sip.Response) {
	tx.mu.Lock()

	if tx.invite {
		tx.receiveInvite(res)
	} else {
		tx.receiveNonInvite(res)
	}

	tx.mu.Unlock()
	tx.drain()
}

func (tx *ClientTransaction) receiveInvite(res *sip.Response) {
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
		if tx.state == Calling {
			tx.state = Proceeding
			tx.stopTimers()
		}

		tx.deliver(res)

		if tx.cancelled && !tx.cancelSent {
			tx.cancelSent = true
			tx.after(64*tx.layer.t1, tx.cancelTimer)
			tx.emit(func() {
				if err := tx.sendCancel(tx.layer.ctx); err != nil {
					tx.layer.log.Debug("CANCEL failed", slog.String("request", tx.req.StartLine()), slog.Any("error", err))
				}
			})
		}
	case res.IsSuccess():
		tx.state = Accepted
		tx.stopTimers()
		tx.deliver(res)
		tx.after(64*tx.layer.t1, tx.end)
	default:
		tx.state = Completed
		tx.stopTimers()
		tx.deliver(res)

		ack, err := sip.NewAck(tx.req, res)
		if err != nil {
			tx.fail(err)
			return
		}

		tx.ack = ack

		if tx.sendAck() {
			tx.afterUnreliable(timerD, tx.end)
		}
	}
}

func (tx *ClientTransaction) receiveNonInvite(res *sip.Response) {
	if tx.state != Trying && tx.state != Proceeding {
		return
	}

	tx.deliver(res)

	if res.IsProvisional() {
		tx.state = Proceeding
		return
	}

	tx.state = Completed
	tx.stopTimers()
	tx.afterUnreliable(tx.layer.t4, tx.end)
}

func (tx *ClientTransaction) sendAck() bool {
	if err := tx.layer.tr.SendOnFlow(tx.layer.ctx, tx.ack); err != nil {
		tx.fail(err)
		return false
	}

	return true
}

func (tx *ClientTransaction) pending() bool {
	if tx.invite {
		return tx.state == Calling
	}

	return tx.state == Trying || tx.state == Proceeding
}

func (tx *ClientTransaction) retransmitTimer() {
	if !tx.pending() {
		return
	}

	if err := tx.layer.tr.SendOnFlow(tx.layer.ctx, tx.req.Clone()); err != nil {
		tx.fail(err)
		return
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
		tx.fail(ErrTimeout)
	}
}

func (tx *ClientTransaction) cancelTimer() {
	if tx.state == Proceeding || tx.state == Calling {
		tx.fail(ErrTimeout)
	}
}

func (tx *ClientTransaction) deliver(res *sip.Response) {
	if h := tx.h; h != nil {
		tx.emit(func() { h.HandleResponse(res) })
	}
}

func (tx *ClientTransaction) fail(err error) {
	tx.terminate(err)

	if h := tx.h; h != nil {
		tx.emit(func() { h.HandleError(err) })
	}
}

func (tx *ClientTransaction) shutdown() {
	tx.mu.Lock()
	tx.terminate(ErrClosed)
	tx.mu.Unlock()
}
