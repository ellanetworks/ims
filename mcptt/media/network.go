// Package media is the media plane of the MCPTT server (TS 24.380 §4.2, §6.3): the media distributor of each call,
// which forwards the RTP media of the participant with the floor to the others, and the floor control channels of its
// participants, over UDP.
//
// It is push-based: each socket has one reader, which forwards audio inline, without a hop to the call's strand per
// packet, and reports to the call only floor control messages and, at a bounded rate, that media arrives. Its
// configuration changes, rare, come from the call's strand.
package media

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
)

// Network opens the UDP sockets of the relay: the system's, or mediatest's.
type Network interface {
	// Listen opens a socket on addr, and calls h with each datagram it receives, from one goroutine or task at a
	// time; b is valid during the call only.
	Listen(addr netip.AddrPort, h func(b []byte, from netip.AddrPort)) (Conn, error)
}

// Conn is a socket of a Network.
type Conn interface {
	WriteTo(b []byte, to netip.AddrPort) error
	LocalAddr() netip.AddrPort
	// Close closes the socket, and returns once its handler returned, never to be called again.
	Close() error
}

// UDP is the system's UDP network. Control, if set, runs on each socket before it is bound, as net.ListenConfig's:
// to bind it to a network interface.
type UDP struct {
	Control func(network, address string, c syscall.RawConn) error
}

// maxDatagram is the size of the read buffer: of the largest UDP datagram, none truncated.
const maxDatagram = 1<<16 - 1

func (n UDP) Listen(addr netip.AddrPort, h func(b []byte, from netip.AddrPort)) (Conn, error) {
	lc := net.ListenConfig{Control: n.Control}

	pc, err := lc.ListenPacket(context.Background(), "udp", addr.String())
	if err != nil {
		return nil, err
	}

	c := pc.(*net.UDPConn)
	u := &udpConn{c: c, done: make(chan struct{})}

	go func() {
		defer close(u.done)

		buf := make([]byte, maxDatagram)

		for {
			n, from, err := c.ReadFromUDPAddrPort(buf)
			if err != nil {
				// Closed, or failing for good: no datagram is read again.
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					continue
				}

				return
			}

			h(buf[:n], netip.AddrPortFrom(from.Addr().Unmap(), from.Port()))
		}
	}()

	return u, nil
}

type udpConn struct {
	c *net.UDPConn
	// done is closed once the reader returned.
	done chan struct{}
}

func (u *udpConn) WriteTo(b []byte, to netip.AddrPort) error {
	_, err := u.c.WriteToUDPAddrPort(b, to)
	return err
}

func (u *udpConn) LocalAddr() netip.AddrPort {
	a := u.c.LocalAddr().(*net.UDPAddr).AddrPort()
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
}

func (u *udpConn) Close() error {
	err := u.c.Close()
	<-u.done

	return err
}
