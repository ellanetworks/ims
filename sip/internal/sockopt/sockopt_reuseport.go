//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package sockopt

import (
	"errors"
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
