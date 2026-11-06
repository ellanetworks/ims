//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package sockopt

import (
	"errors"
	"io"
	"net/netip"
	"syscall"
)

func ReusePort(_, _ string, _ syscall.RawConn) error { return nil }

func CheckNoListener(_ netip.AddrPort) error { return nil }

func Reserve(_ netip.AddrPort) (netip.AddrPort, io.Closer, error) {
	return netip.AddrPort{}, nil, errors.ErrUnsupported
}
