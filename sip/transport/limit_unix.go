//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package transport

import (
	"math"
	"syscall"
)

// openFileLimit is the process's limit of open files, 0 if unknown.
func openFileLimit() int {
	var r syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &r); err != nil {
		return 0
	}

	return int(min(r.Cur, math.MaxInt32))
}
