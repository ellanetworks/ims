//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package sockopt

import "syscall"

func ReusePort(_, _ string, _ syscall.RawConn) error { return nil }
