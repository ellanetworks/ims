package netnstest

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const env = "IMS_NETNS_TEST"

var skip string

func Main(m *testing.M) {
	if os.Getenv(env) == "" {
		cmd := exec.CommandContext(context.Background(), os.Args[0], os.Args[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

		cmd.Env = append(os.Environ(), env+"=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
			UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
			GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		}

		err := cmd.Run()
		if err == nil {
			os.Exit(0)
		}

		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(max(exit.ExitCode(), 1))
		}

		skip = fmt.Sprintf("no user namespace (%v): allow unprivileged user namespaces or run as root", err)
	} else if out, err := exec.CommandContext(context.Background(), "ip", "link", "set", "lo", "up").CombinedOutput(); err != nil {
		skip = fmt.Sprintf("bring up the loopback interface: %v: %s", err, out)
	}

	os.Exit(m.Run())
}

func Unavailable(t testing.TB, reason string) {
	t.Helper()

	if os.Getenv("CI") != "" {
		t.Fatal(reason)
	}

	t.Skip(reason)
}

type Netns struct {
	tid  int
	work chan func()
}

func New(t testing.TB) *Netns {
	t.Helper()

	if skip != "" {
		Unavailable(t, skip)
	}

	if _, err := exec.LookPath("ip"); err != nil {
		Unavailable(t, "no ip command (iproute2)")
	}

	n := &Netns{work: make(chan func())}
	ready := make(chan error)

	go func() {
		runtime.LockOSThread()

		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			ready <- err
			return
		}

		n.tid = unix.Gettid()

		ready <- nil

		for f := range n.work {
			f()
		}
	}()

	if err := <-ready; err != nil {
		Unavailable(t, fmt.Sprintf("unshare network namespace: %v", err))
	}

	t.Cleanup(func() { close(n.work) })

	n.IP(t, "link", "set", "lo", "up")

	return n
}

// Current is the network namespace the test binary runs in, the one Main made
// for it, so that it can be linked to others.
func Current(t testing.TB) *Netns {
	t.Helper()

	if skip != "" {
		Unavailable(t, skip)
	}

	n := &Netns{work: make(chan func())}
	ready := make(chan struct{})

	go func() {
		runtime.LockOSThread()

		n.tid = unix.Gettid()

		close(ready)

		for f := range n.work {
			f()
		}
	}()

	<-ready

	t.Cleanup(func() { close(n.work) })

	return n
}

func (n *Netns) Do(f func()) {
	done := make(chan struct{})

	n.work <- func() {
		defer close(done)

		f()
	}

	<-done
}

func (n *Netns) IP(t testing.TB, args ...string) {
	t.Helper()

	if _, err := n.Command("ip", args...); err != nil {
		t.Fatal(err)
	}
}

func (n *Netns) Command(name string, args ...string) ([]byte, error) {
	var (
		out []byte
		err error
	)

	n.Do(func() { out, err = exec.CommandContext(context.Background(), name, args...).CombinedOutput() })

	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}

	return out, nil
}

func Link(t testing.TB, a, b *Netns, aAddrs, bAddrs []netip.Prefix) {
	t.Helper()

	a.IP(t, "link", "add", "veth0", "type", "veth", "peer", "name", "veth1", "netns", fmt.Sprint(b.tid))

	for _, side := range []struct {
		n     *Netns
		dev   string
		addrs []netip.Prefix
	}{{a, "veth0", aAddrs}, {b, "veth1", bAddrs}} {
		for _, p := range side.addrs {
			args := []string{"addr", "add", p.String(), "dev", side.dev}
			if p.Addr().Is6() {
				args = append(args, "nodad")
			}

			side.n.IP(t, args...)
		}

		side.n.IP(t, "link", "set", side.dev, "up")
	}
}
