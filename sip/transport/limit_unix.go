//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package transport

import (
	"math"
	"syscall"
)

func openFileLimit() int {
	var r syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &r); err != nil {
		return 0
	}

	return int(min(r.Cur, math.MaxInt32))
}
