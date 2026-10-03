package listen

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"syscall"
	"testing"
)

var (
	loopback = netip.MustParseAddr("127.0.0.1")
	lc       net.ListenConfig
)

func TestPairSharesThePort(t *testing.T) {
	for range 100 {
		tl, udp, err := Pair(t.Context(), netip.AddrPortFrom(loopback, 0))
		if err != nil {
			t.Fatal(err)
		}

		tp := tl.Addr().(*net.TCPAddr).AddrPort()
		up := udp.LocalAddr().(*net.UDPAddr).AddrPort()

		if tp.Port() == 0 || tp.Port() != up.Port() {
			t.Fatalf("TCP on %s, UDP on %s", tp, up)
		}

		_ = tl.Close()
		_ = udp.Close()
	}
}

func TestPairAvoidsHeldUDPPorts(t *testing.T) {
	var held []net.PacketConn

	t.Cleanup(func() {
		for _, c := range held {
			_ = c.Close()
		}
	})

	for range 1000 {
		c, err := lc.ListenPacket(t.Context(), "udp4", netip.AddrPortFrom(loopback, 0).String())
		if err != nil {
			t.Fatal(err)
		}

		held = append(held, c)
	}

	before := openFiles(t)

	for range 200 {
		tl, udp, err := Pair(t.Context(), netip.AddrPortFrom(loopback, 0))
		if err != nil {
			t.Fatal(err)
		}

		_ = tl.Close()
		_ = udp.Close()
	}

	if after := openFiles(t); after != before {
		t.Fatalf("%d files open after, %d before", after, before)
	}
}

func openFiles(t *testing.T) int {
	t.Helper()

	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip(err)
	}

	return len(fds)
}

func TestPairOnAPortWhoseUDPIsHeld(t *testing.T) {
	c, err := lc.ListenPacket(t.Context(), "udp4", netip.AddrPortFrom(loopback, 0).String())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = c.Close() }()

	ap := c.LocalAddr().(*net.UDPAddr).AddrPort()

	if _, _, err := Pair(t.Context(), ap); !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("Pair(%s) = %v, want EADDRINUSE", ap, err)
	}

	l, err := lc.Listen(t.Context(), "tcp4", ap.String())
	if err != nil {
		t.Fatalf("TCP on %s after the failed Pair: %v", ap, err)
	}

	_ = l.Close()
}
