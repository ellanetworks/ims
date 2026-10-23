// Package mediatest is a network in memory for tests of the media plane: each datagram is delivered as a task of a
// scheduler, siptest.Net's, so that its Quiesce waits for media as for SIP.
package mediatest

import (
	"errors"
	"net/netip"
	"slices"
	"sync"

	"github.com/ellanetworks/ims/mcptt/media"
)

// Scheduler starts tasks: siptest.Net.Scheduler().
type Scheduler interface {
	Go(f func())
}

// Net is a UDP network in memory. Datagrams to an address without a socket are lost.
type Net struct {
	sched Scheduler

	mu    sync.Mutex
	conns map[netip.AddrPort]*conn
	// lose, if set, tells which datagrams are lost.
	lose func(from, to netip.AddrPort, b []byte) bool
}

var _ media.Network = (*Net)(nil)

// New is a network delivering on sched.
func New(sched Scheduler) *Net {
	return &Net{sched: sched, conns: map[netip.AddrPort]*conn{}}
}

// Lose has the datagrams lose reports lost.
func (n *Net) Lose(lose func(from, to netip.AddrPort, b []byte) bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.lose = lose
}

// ErrInUse is the error of a socket on an address taken.
var ErrInUse = errors.New("mediatest: address in use")

func (n *Net) Listen(addr netip.AddrPort, h func(b []byte, from netip.AddrPort)) (media.Conn, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if _, ok := n.conns[addr]; ok {
		return nil, ErrInUse
	}

	c := &conn{n: n, addr: addr, h: h}
	n.conns[addr] = c

	return c, nil
}

type conn struct {
	n    *Net
	addr netip.AddrPort
	h    func([]byte, netip.AddrPort)
	// mu serializes the deliveries to the socket, as its one reader would.
	mu sync.Mutex
}

func (c *conn) WriteTo(b []byte, to netip.AddrPort) error {
	n := c.n

	n.mu.Lock()
	dst, lose := n.conns[to], n.lose
	n.mu.Unlock()

	if dst == nil || lose != nil && lose(c.addr, to, b) {
		return nil
	}

	b, from := slices.Clone(b), c.addr

	n.sched.Go(func() {
		dst.mu.Lock()
		defer dst.mu.Unlock()

		if dst.open() {
			dst.h(b, from)
		}
	})

	return nil
}

func (c *conn) open() bool {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()

	return c.n.conns[c.addr] == c
}

func (c *conn) LocalAddr() netip.AddrPort { return c.addr }

func (c *conn) Close() error {
	c.n.mu.Lock()

	if c.n.conns[c.addr] == c {
		delete(c.n.conns, c.addr)
	}

	c.n.mu.Unlock()

	// A delivery under way ends first; the later ones find the socket closed.
	c.mu.Lock()
	defer c.mu.Unlock()

	return nil
}

// Peer is a socket of a test on the net, recording what it receives.
type Peer struct {
	media.Conn

	mu       sync.Mutex
	received []Datagram
}

// Datagram is a datagram a peer received.
type Datagram struct {
	From netip.AddrPort
	B    []byte
}

// Peer opens a peer on addr.
func (n *Net) Peer(addr netip.AddrPort) (*Peer, error) {
	p := &Peer{}

	c, err := n.Listen(addr, func(b []byte, from netip.AddrPort) {
		p.mu.Lock()
		defer p.mu.Unlock()

		p.received = append(p.received, Datagram{from, slices.Clone(b)})
	})
	if err != nil {
		return nil, err
	}

	p.Conn = c

	return p, nil
}

// Take returns the datagrams received since the last Take.
func (p *Peer) Take() []Datagram {
	p.mu.Lock()
	defer p.mu.Unlock()

	r := p.received
	p.received = nil

	return r
}
