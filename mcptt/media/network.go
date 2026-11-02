package media

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
)

type Network interface {
	Listen(addr netip.AddrPort, h func(b []byte, from netip.AddrPort)) (Conn, error)
}

type Conn interface {
	WriteTo(b []byte, to netip.AddrPort) error
	LocalAddr() netip.AddrPort
	Close() error
}

type UDP struct {
	Control func(network, address string, c syscall.RawConn) error
}

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
	c    *net.UDPConn
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
