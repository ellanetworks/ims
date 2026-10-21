package transport

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// The listening socket's own errors end its listener; the others are retried, after a wait doubled from 5 ms to at
// most a second, as net/http's Server.
func TestAcceptErrors(t *testing.T) {
	accept := func(errno syscall.Errno) error {
		return &net.OpError{Op: "accept", Net: "tcp4", Err: os.NewSyscallError("accept4", errno)}
	}

	for _, errno := range []syscall.Errno{syscall.EINVAL, syscall.EBADF, syscall.ENOTSOCK} {
		if !listenerBroken(accept(errno)) {
			t.Errorf("%v: listener kept", errno)
		}
	}

	transient := []syscall.Errno{
		syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM, syscall.ECONNABORTED, syscall.EPROTO, syscall.ENETDOWN,
		syscall.EHOSTUNREACH,
	}

	for _, errno := range transient {
		if listenerBroken(accept(errno)) {
			t.Errorf("%v: listener dropped", errno)
		}
	}

	if listenerBroken(fmt.Errorf("accept: %w", os.ErrDeadlineExceeded)) {
		t.Error("timeout: listener dropped")
	}

	var waits []time.Duration

	for d := time.Duration(0); len(waits) < 10; {
		d = acceptBackoff(d)
		waits = append(waits, d)
	}

	if waits[0] != 5*time.Millisecond || waits[1] != 10*time.Millisecond || waits[len(waits)-1] != time.Second {
		t.Fatalf("backoff %v, want from 5ms doubled to 1s", waits)
	}
}
