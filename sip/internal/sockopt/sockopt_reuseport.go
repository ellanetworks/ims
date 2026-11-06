//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package sockopt

import (
	"errors"
	"io"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

func ReusePort(_, _ string, c syscall.RawConn) error {
	var serr error

	err := c.Control(func(fd uintptr) {
		if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); serr != nil {
			return
		}

		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	})

	return errors.Join(err, serr)
}

func CheckNoListener(ap netip.AddrPort) error {
	family := unix.AF_INET
	if ap.Addr().Is6() {
		family = unix.AF_INET6
	}

	fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, unix.IPPROTO_TCP)
	if err != nil {
		return err
	}

	defer func() { _ = unix.Close(fd) }()

	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
		return err
	}

	return unix.Bind(fd, sockaddr(ap))
}

type reservation int

func (r reservation) Close() error {
	return unix.Close(int(r))
}

func Reserve(ap netip.AddrPort) (netip.AddrPort, io.Closer, error) {
	family := unix.AF_INET
	if ap.Addr().Is6() {
		family = unix.AF_INET6
	}

	fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, unix.IPPROTO_TCP)
	if err != nil {
		return netip.AddrPort{}, nil, err
	}

	bound, err := reserve(fd, ap)
	if err != nil {
		_ = unix.Close(fd)
		return netip.AddrPort{}, nil, err
	}

	return bound, reservation(fd), nil
}

func reserve(fd int, ap netip.AddrPort) (netip.AddrPort, error) {
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
		return netip.AddrPort{}, err
	}

	if err := unix.Bind(fd, sockaddr(ap)); err != nil {
		return netip.AddrPort{}, err
	}

	sa, err := unix.Getsockname(fd)
	if err != nil {
		return netip.AddrPort{}, err
	}

	switch sa := sa.(type) {
	case *unix.SockaddrInet4:
		return netip.AddrPortFrom(netip.AddrFrom4(sa.Addr), uint16(sa.Port)), nil
	case *unix.SockaddrInet6:
		return netip.AddrPortFrom(netip.AddrFrom16(sa.Addr), uint16(sa.Port)), nil
	}

	return netip.AddrPort{}, errors.New("sockopt: unexpected socket address")
}

func sockaddr(ap netip.AddrPort) unix.Sockaddr {
	if ap.Addr().Is6() {
		return &unix.SockaddrInet6{Port: int(ap.Port()), Addr: ap.Addr().As16()}
	}

	return &unix.SockaddrInet4{Port: int(ap.Port()), Addr: ap.Addr().As4()}
}
