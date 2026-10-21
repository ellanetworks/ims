package transport

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// The socket's own errors end its listener, ECONNABORTED too of a UDP socket, which Linux sets only on destroying it;
// the others are retried, after a wait doubled from 5 ms to at most a second, as net/http's Server.
func TestSocketErrors(t *testing.T) {
	opErr := func(op string, errno syscall.Errno) error {
		return &net.OpError{Op: op, Net: "tcp4", Err: os.NewSyscallError(op, errno)}
	}

	for _, errno := range []syscall.Errno{syscall.EINVAL, syscall.EBADF, syscall.ENOTSOCK} {
		if !socketFailed(opErr("accept4", errno)) || !udpFailed(opErr("recvfrom", errno)) {
			t.Errorf("%v: listener kept", errno)
		}
	}

	transient := []syscall.Errno{
		syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM, syscall.ECONNABORTED, syscall.EPROTO, syscall.ENETDOWN,
		syscall.EHOSTUNREACH,
	}

	for _, errno := range transient {
		if socketFailed(opErr("accept4", errno)) {
			t.Errorf("accept %v: listener dropped", errno)
		}
	}

	if !udpFailed(opErr("recvfrom", syscall.ECONNABORTED)) {
		t.Error("UDP socket destroyed: listener kept")
	}

	for _, errno := range []syscall.Errno{syscall.ENOBUFS, syscall.ENOMEM, syscall.ECONNREFUSED} {
		if udpFailed(opErr("recvfrom", errno)) {
			t.Errorf("read %v: listener dropped", errno)
		}
	}

	if socketFailed(fmt.Errorf("accept: %w", os.ErrDeadlineExceeded)) {
		t.Error("timeout: listener dropped")
	}

	var waits []time.Duration

	for d := time.Duration(0); len(waits) < 10; {
		d = retryBackoff(d)
		waits = append(waits, d)
	}

	if waits[0] != 5*time.Millisecond || waits[1] != 10*time.Millisecond || waits[len(waits)-1] != time.Second {
		t.Fatalf("backoff %v, want from 5ms doubled to 1s", waits)
	}
}
