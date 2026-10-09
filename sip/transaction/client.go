package transaction

import (
	"log/slog"
	"slices"

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
	sent   bool
	early  []*sip.Response
	ack    *sip.Request

	cancelled bool
	prepare   func(*sip.Request)
}

func (l *Layer) Request(req *sip.Request, h ClientHandler) (*ClientTransaction, error) {
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

	if h != nil {
		tx.onError = h.HandleError
	}

	tx.mu.Lock()
	defer tx.unlock()

	if err := l.addClient(clientKey{branch: branch, method: req.Method}, tx); err != nil {
		return nil, err
	}

	if !l.reliable(req.Flow) {
		tx.interval = l.t1
		tx.after(tx.interval, tx.retransmitTimer)
	}

	tx.after(64*l.t1, tx.timeoutTimer)

	out := req.Clone()

	tx.push(func() error { return l.send(l.ctx, out, false) }, func(err error) { tx.sentResult(out, err) })

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

func (tx *ClientTransaction) sentResult(sent *sip.Request, err error) {
	if err != nil {
		tx.fail(err)
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

func (tx *ClientTransaction) sendCancel() {
	tx.after(64*tx.layer.t1, tx.cancelTimer)

	cancel, err := sip.NewCancel(tx.req)
	if err == nil {
		if tx.prepare != nil {
			tx.prepare(cancel)
		}

		_, err = tx.layer.Request(cancel, nil)
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
		tx.sendAck()
		tx.afterUnreliable(timerD, tx.end)
	}
}

func (tx *ClientTransaction) handleNonInvite(res *sip.Response) {
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

func (tx *ClientTransaction) sendAck() {
	ack := tx.ack.Clone()
	tx.push(func() error { return tx.layer.send(tx.layer.ctx, ack, true) }, tx.sendResult)
}

func (tx *ClientTransaction) sendResult(err error) {
	if err != nil {
		tx.fail(err)
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
		req := tx.req.Clone()
		tx.push(func() error { return tx.layer.send(tx.layer.ctx, req, true) }, tx.sendResult)
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
	if tx.state == Proceeding {
		tx.fail(ErrTimeout)
	}
}

func (tx *ClientTransaction) deliver(res *sip.Response) {
	if h := tx.h; h != nil {
		tx.emit(func() { h.HandleResponse(res) })
	}
}
