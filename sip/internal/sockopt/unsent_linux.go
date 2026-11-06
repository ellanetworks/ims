//go:build linux

package sockopt

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

const tcpClose = 7

func Unsent(c syscall.RawConn) (int, error) {
	var (
		n    int
		serr error
	)

	err := c.Control(func(fd uintptr) {
		var info *unix.TCPInfo

		info, serr = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
		if serr != nil || info.State == tcpClose {
			return
		}

		n, serr = unix.IoctlGetInt(int(fd), unix.SIOCOUTQ)
	})

	return n, errors.Join(err, serr)
}
