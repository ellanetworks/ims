//go:build !linux

package sockopt

import (
	"syscall"
	"time"
)

func UserTimeout(_ syscall.RawConn, _ time.Duration) error { return nil }
