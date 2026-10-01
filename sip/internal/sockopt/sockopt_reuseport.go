//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package sockopt

import (
	"errors"
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

	var sa unix.Sockaddr = &unix.SockaddrInet4{Port: int(ap.Port()), Addr: ap.Addr().As4()}
	if ap.Addr().Is6() {
		sa = &unix.SockaddrInet6{Port: int(ap.Port()), Addr: ap.Addr().As16()}
	}

	return unix.Bind(fd, sa)
}
