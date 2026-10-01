//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package sockopt

import (
	"net/netip"
	"syscall"
)

func ReusePort(_, _ string, _ syscall.RawConn) error { return nil }

func CheckNoListener(_ netip.AddrPort) error { return nil }
