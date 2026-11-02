package transaction

import (
	"context"
	"errors"

	"github.com/ellanetworks/ims/sip"
)

// RFC 3261 section 12.2.2
type outbox struct {
	jobs []func(ok bool)
}

func (l *Layer) enqueue(f sip.Flow, job func(ok bool)) bool {
	f = normalizedFlow(f)

	l.qmu.Lock()
	defer l.qmu.Unlock()

	if l.closed {
		return false
	}

	if ob, ok := l.outboxes[f]; ok {
		ob.jobs = append(ob.jobs, job)
		return true
	}

	ob := &outbox{jobs: []func(bool){job}}
	l.outboxes[f] = ob
	l.wg.Add(1)

	l.sched.Go(func() { l.drainOutbox(f, ob) })

	return true
}

func (l *Layer) drainOutbox(f sip.Flow, ob *outbox) {
	defer l.wg.Done()

	for {
		l.qmu.Lock()

		if len(ob.jobs) == 0 {
			delete(l.outboxes, f)
			l.checkDrained()

			l.qmu.Unlock()

			return
		}

		job := ob.jobs[0]
		ob.jobs[0] = nil
		ob.jobs = ob.jobs[1:]
		ok := !l.closed

		l.qmu.Unlock()

		job(ok)
	}
}

func (l *Layer) Shutdown(ctx context.Context) error {
	l.qmu.Lock()

	var drained chan struct{}

	if (len(l.outboxes) > 0 || len(l.lanes) > 0) && !l.closed {
		if l.drained == nil {
			l.drained = make(chan struct{})
		}

		drained = l.drained
	}

	l.qmu.Unlock()

	if drained != nil {
		select {
		case <-drained:
		case <-ctx.Done():
			return errors.Join(ctx.Err(), l.Close())
		}
	}

	return l.Close()
}

func (l *Layer) checkDrained() {
	if len(l.outboxes) == 0 && len(l.lanes) == 0 && l.drained != nil {
		close(l.drained)
		l.drained = nil
	}
}

func (l *Layer) sendInOrder(m sip.Message, exact bool) error {
	done := make(chan error, 1)

	if !l.enqueue(m.Env().Flow, func(ok bool) {
		if !ok {
			done <- ErrClosed
			return
		}

		done <- l.send(l.ctx, m, exact)
	}) {
		return ErrClosed
	}

	select {
	case err := <-done:
		return err
	case <-l.ctx.Done():
		return ErrClosed
	}
}

// RFC 3261 section 13.2.2.4
func (l *Layer) QueueAck(ctx context.Context, ack *sip.Request, done func(error)) error {
	if ack.Method != "ACK" {
		return errNotAck(ack)
	}

	ack = ack.Clone()
	l.propagate(ctx, ack)

	if !l.enqueue(ack.Flow, func(ok bool) {
		err := ErrClosed
		if ok {
			err = l.send(l.ctx, ack, false)
		}

		if done != nil {
			done(err)
		}
	}) {
		return ErrClosed
	}

	return nil
}

func normalizedFlow(f sip.Flow) sip.Flow {
	f.Local, f.Remote = unmap(f.Local), unmap(f.Remote)
	return f
}
