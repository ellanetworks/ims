//go:build linux

package transport_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transport"
)

const isolatedEnv = "SIP_TRANSPORT_ISOLATED"

// isolated runs the test in a process of its own, root in a user and network namespace of its own: for the sockets it
// destroys (SOCK_DESTROY needs CAP_NET_ADMIN) and the limits it lowers. It reports whether the test runs here: false
// in the parent, once the child passed. Without user namespaces, the test is skipped, and fails when CI is set.
func isolated(t *testing.T) bool {
	t.Helper()

	if os.Getenv(isolatedEnv) != "" {
		if out, err := exec.CommandContext(t.Context(), "ip", "link", "set", "lo", "up").CombinedOutput(); err != nil {
			t.Fatalf("bring up the loopback interface: %v: %s", err, out)
		}

		return true
	}

	run := "^" + strings.ReplaceAll(t.Name(), "/", "$/^") + "$"
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run="+run, "-test.count=1")
	// Go listens with Multipath TCP where the kernel has it, whose listeners SOCK_DESTROY does not destroy; Android's
	// kernels do not.
	cmd.Env = append(os.Environ(), isolatedEnv+"=1", "GODEBUG=multipathtcp=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}

	out, err := cmd.CombinedOutput()

	var exit *exec.ExitError

	switch {
	case errors.As(err, &exit):
		t.Fatalf("%s", out)
	case err != nil && os.Getenv("CI") != "":
		t.Fatalf("no user namespace: %v", err)
	case err != nil:
		t.Skipf("no user namespace (%v): allow unprivileged user namespaces or run as root", err)
	}

	return false
}

// destroy has the kernel destroy the sockets of a protocol bound to port, as Android does with those of a network
// that went away (SOCK_DESTROY).
func destroy(t *testing.T, proto string, port uint16) {
	t.Helper()

	if _, err := exec.LookPath("ss"); err != nil {
		t.Skip("no ss command (iproute2)")
	}

	out, err := exec.CommandContext(t.Context(), "ss", "-K", "-a", "--"+proto, "sport", "=", fmt.Sprintf(":%d", port)).CombinedOutput()
	if err != nil {
		t.Fatalf("ss -K: %v: %s", err, out)
	}
}

// A listener whose UDP socket or TCP listener the kernel destroyed fails as a whole (RFC 3261 §18.2.1), reported with
// its error: a TCP listener no longer listening fails accept with EINVAL, a UDP socket its next read with ECONNABORTED.
func TestDestroyedListener(t *testing.T) {
	for proto, want := range map[string]syscall.Errno{"tcp": syscall.EINVAL, "udp": syscall.ECONNABORTED} {
		t.Run(proto, func(t *testing.T) {
			if !isolated(t) {
				return
			}

			cfg := transport.Config{}
			failed := failures(&cfg)
			tr, _ := siptest.NewTransport(t, cfg)
			local := siptest.Listen(t, tr, lo)

			destroy(t, proto, local.Port())

			if f := awaitFailure(t, failed); f.local != local || !errors.Is(f.err, want) {
				t.Fatalf("failure of %s: %v; want %s, %v", f.local, f.err, local, want)
			}

			req := siptest.NewRequest("OPTIONS", "sip:a@127.0.0.1", sip.UDP, local)
			req.Flow = sip.Flow{Transport: sip.UDP, Local: local, Remote: netip.MustParseAddrPort("127.0.0.1:9")}

			if err := tr.Send(context.Background(), req); !errors.Is(err, transport.ErrNoListener) {
				t.Fatalf("Send from the failed listener: err = %v, want %v", err, transport.ErrNoListener)
			}
		})
	}
}

// Out of file descriptors, accept fails for the moment: retried after a wait doubled from 5 ms, the listener kept,
// accepting the connection once descriptors are back.
func TestAcceptBacksOff(t *testing.T) {
	if !isolated(t) {
		return
	}

	var logs syncBuffer

	cfg := transport.Config{Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	failed := failures(&cfg)
	tr, _ := siptest.NewTransport(t, cfg)
	local := siptest.Listen(t, tr, lo)

	client, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = syscall.Close(client) }()

	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}

	next, err := syscall.Open("/dev/null", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}

	_ = syscall.Close(next)

	// No descriptor left for the connection accept takes.
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: uint64(next), Max: limit.Max}); err != nil {
		t.Fatal(err)
	}

	if err := syscall.Connect(client, &syscall.SockaddrInet4{Port: int(local.Port()), Addr: lo.As4()}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(siptest.Timeout)
	for !strings.Contains(logs.String(), "retry=20ms") {
		if time.Now().After(deadline) {
			t.Fatalf("accept not retried after 5, 10, 20 ms: %s", logs.String())
		}

		time.Sleep(time.Millisecond)
	}

	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"retry=5ms", "retry=10ms", "too many open files"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs without %q: %s", want, logs.String())
		}
	}

	for !strings.Contains(logs.String(), "SIP connection accepted") {
		if time.Now().After(deadline) {
			t.Fatalf("connection not accepted once descriptors are back: %s", logs.String())
		}

		time.Sleep(10 * time.Millisecond)
	}

	select {
	case f := <-failed:
		t.Fatalf("listener failed: %s: %v", f.local, f.err)
	default:
	}
}
