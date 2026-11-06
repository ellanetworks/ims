//go:build linux

package trgw

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// RFC 4594 section 2.3
// RFC 4594 section 4.1
const dscpEF = 46

func markEF(network, _ string, c syscall.RawConn) error {
	var err error

	cerr := c.Control(func(fd uintptr) {
		if network == "udp6" {
			err = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_TCLASS, dscpEF<<2)
			return
		}

		err = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_TOS, dscpEF<<2)
	})
	if cerr != nil {
		return cerr
	}

	return err
}
