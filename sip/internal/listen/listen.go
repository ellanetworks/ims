package listen

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"

	"github.com/ellanetworks/ims/sip/internal/sockopt"
)

const attempts = 64

func Pair(ctx context.Context, local netip.AddrPort) (*net.TCPListener, *net.UDPConn, error) {
	var held []net.Listener

	defer func() {
		for _, l := range held {
			_ = l.Close()
		}
	}()

	for {
		tl, err := tcp(ctx, local)
		if err != nil {
			return nil, nil, err
		}

		bound := tl.Addr().(*net.TCPAddr).AddrPort()
		bound = netip.AddrPortFrom(bound.Addr().Unmap(), bound.Port())

		var lc net.ListenConfig

		pc, err := lc.ListenPacket(ctx, network("udp", local.Addr()), bound.String())
		if err == nil {
			return tl, pc.(*net.UDPConn), nil
		}

		if local.Port() != 0 || !errors.Is(err, syscall.EADDRINUSE) || len(held) == attempts-1 {
			_ = tl.Close()
			return nil, nil, err
		}

		held = append(held, tl)
	}
}

func tcp(ctx context.Context, local netip.AddrPort) (*net.TCPListener, error) {
	lc := net.ListenConfig{Control: sockopt.ReusePort}

	l, err := lc.Listen(ctx, network("tcp", local.Addr()), local.String())
	if err != nil {
		return nil, err
	}

	return l.(*net.TCPListener), nil
}

func network(prefix string, a netip.Addr) string {
	if a.Is4() || a.Is4In6() {
		return prefix + "4"
	}

	return prefix + "6"
}
