//go:build linux

package transport

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"golang.org/x/sys/unix"
)

type discard struct{}

func (discard) HandleMessage(sip.Message) {}

func (discard) HandleParseError(*sip.ParseError) {}

func TestPersistentConnectionUserTimeout(t *testing.T) {
	tr := New(Config{Handler: discard{}})

	t.Cleanup(func() { _ = tr.Close() })

	lo := netip.MustParseAddr("127.0.0.1")

	local, err := tr.Listen(t.Context(), netip.AddrPortFrom(lo, 0))
	if err != nil {
		t.Fatal(err)
	}

	var lc net.ListenConfig

	l, err := lc.Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = l.Close() })

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}

			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	f := sip.Flow{Transport: sip.TCP, Local: local, Remote: netip.MustParseAddrPort(l.Addr().String())}

	stop, err := tr.Persist(func(context.Context) ([]sip.Flow, error) { return []sip.Flow{f}, nil },
		KeepAlive{Interval: time.Minute, PongTimeout: 7 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(stop)

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		var tcp *net.TCPConn

		tr.mu.Lock()
		if c := tr.conns[f]; c != nil {
			tcp = c.tcp
		}
		tr.mu.Unlock()

		if tcp != nil {
			rc, err := tcp.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}

			var (
				ms   int
				gerr error
			)

			_ = rc.Control(func(fd uintptr) { ms, gerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT) })

			if gerr == nil && ms == 100000 {
				return
			}
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("no TCP user timeout on the persistent connection")
}
