package transaction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transport"
)

var (
	ErrInUse = errors.New("sip/transaction: address in use")

	// ErrRefused is the error of a send over TCP to an address a closed network does not have.
	ErrRefused = errors.New("sip/transaction: connection refused")
)

// A Network carries messages in memory between layers. Each address on it is served by a layer (Layer.Serve), or
// attached to one for a peer that takes its messages itself (Layer.Attach); a layer also delivers in memory to the
// addresses it listens on. A message to an address of the network is
// delivered in memory, without loss, in the order of its flow, so the transactions on it are reliable. A message to
// another address goes to the transport of the layer, unless the network is closed: then it is lost over UDP, as to a
// host that is gone, and refused over TCP.
type Network struct {
	closed bool

	mu        sync.Mutex
	endpoints map[netip.AddrPort]endpoint
}

// endpoint is an address of a network: served by layer, or attached to it with receive.
type endpoint struct {
	layer   *Layer
	receive func(sip.Message)
}

// NewNetwork makes a network whose layers send to other addresses through their transport.
func NewNetwork() *Network {
	return &Network{endpoints: make(map[netip.AddrPort]endpoint)}
}

// NewClosedNetwork makes a network that keeps every message: the in-memory network of tests.
func NewClosedNetwork() *Network {
	n := NewNetwork()
	n.closed = true

	return n
}

func (n *Network) bind(addr netip.AddrPort, ep endpoint) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if _, ok := n.endpoints[addr]; ok {
		return fmt.Errorf("%w: %s", ErrInUse, addr)
	}

	n.endpoints[addr] = ep

	return nil
}

func (n *Network) lookup(addr netip.AddrPort) (endpoint, bool) {
	if !addr.IsValid() {
		return endpoint{}, false
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	ep, ok := n.endpoints[unmap(addr)]

	return ep, ok
}

// unbind removes the addresses of l, once closed.
func (n *Network) unbind(l *Layer) {
	n.mu.Lock()
	defer n.mu.Unlock()

	for addr, ep := range n.endpoints {
		if ep.layer == l {
			delete(n.endpoints, addr)
		}
	}
}

// Serve serves local in memory: the layers of its network send to it without a transport.
func (l *Layer) Serve(local netip.AddrPort) error {
	local = unmap(local)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return ErrClosed
	}

	if err := l.network.bind(local, endpoint{layer: l}); err != nil {
		return err
	}

	l.addLocal(local)

	return nil
}

// Attach attaches addr to the network of the layer for a peer: receive takes the messages to addr, and the peer
// sends from addr through the layer (SendOnFlow). It returns the function that detaches it.
func (l *Layer) Attach(addr netip.AddrPort, receive func(sip.Message)) (func(), error) {
	addr = unmap(addr)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil, ErrClosed
	}

	if l.isLocalAddr(addr) {
		return nil, fmt.Errorf("%w: %s", ErrInUse, addr)
	}

	if err := l.network.bind(addr, endpoint{layer: l, receive: receive}); err != nil {
		return nil, err
	}

	return func() {
		l.network.mu.Lock()
		delete(l.network.endpoints, addr)
		l.network.mu.Unlock()
	}, nil
}

// endpoint is the endpoint of addr in memory: on the network of the layer, or an address the layer listens on, which
// it delivers to itself.
func (l *Layer) endpoint(addr netip.AddrPort) (endpoint, bool) {
	if ep, ok := l.network.lookup(addr); ok {
		return ep, true
	}

	return endpoint{layer: l}, addr.IsValid() && l.isLocalAddr(unmap(addr))
}

// inMemory reports whether addr is reached in memory.
func (l *Layer) inMemory(addr netip.AddrPort) bool {
	_, ok := l.endpoint(addr)
	return ok
}

// sendsFrom reports whether the layer sends from local: an address it serves or listens on, or one of a peer attached
// to it.
func (l *Layer) sendsFrom(local netip.AddrPort) bool {
	if l.isLocalAddr(local) {
		return true
	}

	ep, ok := l.network.lookup(local)

	return ok && ep.layer == l
}

// reliable reports whether the transactions on f need no retransmissions: over TCP, or in memory.
func (l *Layer) reliable(f sip.Flow) bool {
	return f.Transport == sip.TCP || l.inMemory(f.Remote)
}

func (l *Layer) send(ctx context.Context, m sip.Message, exact bool) error {
	f := m.Env().Flow

	if l.inMemory(f.Remote) {
		if err := l.deliver(m); err != nil {
			return fmt.Errorf("sip/transaction: send %s: %w", m.StartLine(), err)
		}

		return nil
	}

	if l.network.closed {
		if f.Transport == sip.TCP {
			return fmt.Errorf("sip/transaction: send %s to %s: %w", m.StartLine(), f.Remote, ErrRefused)
		}

		return nil
	}

	if exact {
		return l.tr.SendOnFlow(ctx, m)
	}

	return l.tr.Send(ctx, m)
}

// sendRequest sends the request of a client transaction, and returns the TCP connection it went out on: zero in memory
// or over UDP.
func (l *Layer) sendRequest(ctx context.Context, r *sip.Request) (transport.Conn, error) {
	if l.inMemory(r.Flow.Remote) || l.network.closed {
		return 0, l.send(ctx, r, false)
	}

	return l.tr.SendRequest(ctx, r)
}

// connected reports whether the TCP connection c of flow f is not lost: always for none, in memory or over UDP.
func (l *Layer) connected(f sip.Flow, c transport.Conn) bool {
	return c == 0 || l.tr.Connected(f, c)
}

// deliver passes a copy of m to the endpoint of its remote address, on the lane of its flow.
func (l *Layer) deliver(m sip.Message) error {
	f := m.Env().Flow
	f.Local, f.Remote = unmap(f.Local), unmap(f.Remote)

	if f.Transport != sip.UDP && f.Transport != sip.TCP {
		return fmt.Errorf("%w %q", transport.ErrUnsupportedTransport, f.Transport)
	}

	if !l.sendsFrom(f.Local) {
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
		// The endpoint as the message arrives: one detached or closed meanwhile is gone, the message lost.
		to, ok := l.endpoint(f.Remote)
		if !ok {
			return
		}

		if to.receive != nil {
			to.receive(c)
			return
		}

		if res, ok := c.(*sip.Response); ok {
			if err := res.Validate(); err != nil {
				to.layer.log.Debug("dropped invalid response", slog.String("flow", res.Flow.String()), slog.Any("error", err))
				return
			}
		}

		transportHandler{to.layer}.HandleMessage(c)
	})
}

type lane struct {
	queue []func()
}

func (l *Layer) post(f sip.Flow, job func()) error {
	l.qmu.Lock()
	defer l.qmu.Unlock()

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

	l.sched.Go(func() { l.drainLane(f, ln) })

	return nil
}

func (l *Layer) drainLane(f sip.Flow, ln *lane) {
	defer l.wg.Done()

	for {
		l.qmu.Lock()

		if len(ln.queue) == 0 || l.closed {
			delete(l.lanes, f)
			l.checkDrained()
			l.qmu.Unlock()

			return
		}

		job := ln.queue[0]
		ln.queue[0] = nil
		ln.queue = ln.queue[1:]

		l.qmu.Unlock()

		job()
	}
}

func unmap(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}
