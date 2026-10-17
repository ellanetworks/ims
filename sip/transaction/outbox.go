package transaction

import (
	"context"

	"github.com/ellanetworks/ims/sip"
)

// An outbox sends the messages of one flow in the order the layer took them: the requests of a dialog in the order
// of their CSeq, which their recipient enforces (RFC 3261 §12.2.2), an ACK before the request that follows it, a
// response before the request it precedes. One goroutine drains each outbox while it has messages; the transport
// writes on it.
type outbox struct {
	jobs []func(ok bool)
}

// enqueue takes a send of the flow f in order. The job runs with ok on the goroutine of the outbox, or without it once
// the layer closed, to complete; enqueue reports false when the layer is already closed.
func (l *Layer) enqueue(f sip.Flow, job func(ok bool)) bool {
	f = normalizedFlow(f)

	l.mu.Lock()
	defer l.mu.Unlock()

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

	go l.drainOutbox(f, ob)

	return true
}

func (l *Layer) drainOutbox(f sip.Flow, ob *outbox) {
	defer l.wg.Done()

	for {
		l.mu.Lock()

		if len(ob.jobs) == 0 {
			delete(l.outboxes, f)

			if len(l.outboxes) == 0 && l.drained != nil {
				close(l.drained)
				l.drained = nil
			}

			l.mu.Unlock()

			return
		}

		job := ob.jobs[0]
		ob.jobs[0] = nil
		ob.jobs = ob.jobs[1:]
		ok := !l.closed

		l.mu.Unlock()

		job(ok)
	}
}

// Shutdown closes the layer once the messages it took are sent, or ctx is done: the responses and requests of the
// transactions under way leave before the transport closes.
func (l *Layer) Shutdown(ctx context.Context) error {
	l.mu.Lock()

	var drained chan struct{}

	if len(l.outboxes) > 0 && !l.closed {
		if l.drained == nil {
			l.drained = make(chan struct{})
		}

		drained = l.drained
	}

	l.mu.Unlock()

	if drained != nil {
		select {
		case <-drained:
		case <-ctx.Done():
		}
	}

	return l.Close()
}

// sendInOrder sends m in its flow's order, and waits for the send.
func (l *Layer) sendInOrder(ctx context.Context, m sip.Message, exact bool) error {
	done := make(chan error, 1)

	if !l.enqueue(m.Env().Flow, func(ok bool) {
		if !ok {
			done <- ErrClosed
			return
		}

		done <- l.send(ctx, m, exact)
	}) {
		return ErrClosed
	}

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// QueueAck sends the ACK of a 2xx (RFC 3261 §13.2.2.4), outside any transaction, in the order of its flow: before the
// requests of the dialog sent after it. done, when not nil, receives the outcome on the goroutine of the outbox.
func (l *Layer) QueueAck(ack *sip.Request, done func(error)) error {
	if ack.Method != "ACK" {
		return errNotAck(ack)
	}

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
