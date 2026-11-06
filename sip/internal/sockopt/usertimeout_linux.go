//go:build linux

package sockopt

import (
	"errors"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func UserTimeout(c syscall.RawConn, d time.Duration) error {
	var serr error

	err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(d.Milliseconds()))
	})

	return errors.Join(err, serr)
}
