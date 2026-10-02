package transaction

import (
	"github.com/ellanetworks/ims/sip"
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
}

func newServer(l *Layer, req *sip.Request) *ServerTransaction {
	tx := &ServerTransaction{req: req, invite: req.Method == "INVITE", tag: sip.NewTag(), trying: sip.NewResponse(req, 100, "")}
	tx.init(l, Trying)
	tx.flow = req.Flow
	tx.reliable = isReliable(tx.flow)
	tx.may100 = tx.invite || tx.reliable

	if req.Method != "CANCEL" {
		tx.onError = func(err error) { l.h.HandleTransactionError(tx, err) }
	}

	if tx.invite {
		tx.state = Proceeding
	}

	return tx
}

func (tx *ServerTransaction) Request() *sip.Request {
	return tx.req
}

func (tx *ServerTransaction) ToTag() string {
	return tx.tag
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

	tx.emit(func() { tx.layer.h.HandleRequest(tx, tx.req) })
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

	tx.push(func() error {
		f, exact, err := tx.layer.responseFlow(tx.req, res)
		if err != nil {
			return err
		}

		res.Flow = f

		if exact {
			return tx.layer.tr.SendOnFlow(tx.layer.ctx, res)
		}

		return tx.layer.tr.Send(tx.layer.ctx, res)
	}, func(err error) {
		if err != nil {
			tx.report(err)
		}
	})
}

func (tx *ServerTransaction) receive() {
	tx.mu.Lock()

	if tx.last != nil && len(tx.out) == 0 && (tx.state == Proceeding || tx.state == Completed) {
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
		tx.emit(func() { tx.layer.h.HandleAck(ack) })
	}

	tx.unlockInOrder()
}

func (tx *ServerTransaction) receiveCancel(cancel *sip.Request) {
	tx.mu.Lock()

	if tx.state == Proceeding {
		tx.emit(func() { tx.layer.h.HandleCancel(tx, cancel) })
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

	if len(tx.out) == 0 {
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
