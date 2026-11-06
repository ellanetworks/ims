//go:build !linux

package sockopt

import (
	"errors"
	"syscall"
)

func Unsent(_ syscall.RawConn) (int, error) { return 0, errors.ErrUnsupported }
