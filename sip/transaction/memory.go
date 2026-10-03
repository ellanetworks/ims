package transaction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transport"
)

var ErrInUse = errors.New("sip/transaction: address in use")

func (l *Layer) Serve(local netip.AddrPort) error {
	local = unmap(local)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return ErrClosed
	}

	if _, ok := l.peers[local]; ok {
		return fmt.Errorf("%w: %s", ErrInUse, local)
	}

	l.locals[local] = struct{}{}

	return nil
}

func (l *Layer) Attach(addr netip.AddrPort, receive func(sip.Message)) (func(), error) {
	addr = unmap(addr)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil, ErrClosed
	}

	_, local := l.locals[addr]
	if _, peer := l.peers[addr]; local || peer {
		return nil, fmt.Errorf("%w: %s", ErrInUse, addr)
	}

	l.peers[addr] = receive

	return func() {
		l.mu.Lock()
		delete(l.peers, addr)
		l.mu.Unlock()
	}, nil
}

func (l *Layer) inMemory(addr netip.AddrPort) (func(sip.Message), bool) {
	if !addr.IsValid() {
		return nil, false
	}

	addr = unmap(addr)

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.locals[addr]; ok {
		return nil, true
	}

	receive, ok := l.peers[addr]

	return receive, ok
}

func (l *Layer) reliable(f sip.Flow) bool {
	if f.Transport == sip.TCP {
		return true
	}

	_, ok := l.inMemory(f.Remote)

	return ok
}

func (l *Layer) send(ctx context.Context, m sip.Message, exact bool) error {
	if receive, ok := l.inMemory(m.Env().Flow.Remote); ok {
		if err := l.deliver(m, receive); err != nil {
			return fmt.Errorf("sip/transaction: send %s: %w", m.StartLine(), err)
		}

		return nil
	}

	if exact {
		return l.tr.SendOnFlow(ctx, m)
	}

	return l.tr.Send(ctx, m)
}

func (l *Layer) deliver(m sip.Message, receive func(sip.Message)) error {
	f := m.Env().Flow
	f.Local, f.Remote = unmap(f.Local), unmap(f.Remote)

	if f.Transport != sip.UDP && f.Transport != sip.TCP {
		return fmt.Errorf("%w %q", transport.ErrUnsupportedTransport, f.Transport)
	}

	if _, ok := l.inMemory(f.Local); !ok {
		return fmt.Errorf("%w %s", transport.ErrNoListener, f.Local)
	}

	m.Env().Flow = f

	var c sip.Message

	switch m := m.(type) {
	case *sip.Request:
		c = m.Clone()
	case *sip.Response:
		c = m.Clone()
	}

	c.Env().Flow = sip.Flow{Transport: f.Transport, Local: f.Remote, Remote: f.Local}

	return l.post(f, func() {
		if receive != nil {
			receive(c)
			return
		}

		if res, ok := c.(*sip.Response); ok {
			if err := res.Validate(); err != nil {
				l.log.Debug("dropped invalid response", slog.String("flow", res.Flow.String()), slog.Any("error", err))
				return
			}
		}

		transportHandler{l}.HandleMessage(c)
	})
}

type lane struct {
	queue []func()
}

func (l *Layer) post(f sip.Flow, job func()) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return ErrClosed
	}

	if ln, ok := l.lanes[f]; ok {
		ln.queue = append(ln.queue, job)
		return nil
	}

	ln := &lane{queue: []func(){job}}
	l.lanes[f] = ln
	l.wg.Add(1)

	go l.drainLane(f, ln)

	return nil
}

func (l *Layer) drainLane(f sip.Flow, ln *lane) {
	defer l.wg.Done()

	for {
		l.mu.Lock()

		if len(ln.queue) == 0 || l.closed {
			delete(l.lanes, f)
			l.mu.Unlock()

			return
		}

		job := ln.queue[0]
		ln.queue[0] = nil
		ln.queue = ln.queue[1:]

		l.mu.Unlock()

		job()
	}
}

func unmap(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}
