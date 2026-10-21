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

// Pair listens on TCP and UDP at local, control running on each socket before it is bound, if not nil.
func Pair(ctx context.Context, local netip.AddrPort, control func(network, address string, c syscall.RawConn) error) (*net.TCPListener, *net.UDPConn, error) {
	var held []net.Listener

	defer func() {
		for _, l := range held {
			_ = l.Close()
		}
	}()

	for {
		tl, err := tcp(ctx, local, control)
		if err != nil {
			return nil, nil, err
		}

		bound := tl.Addr().(*net.TCPAddr).AddrPort()
		bound = netip.AddrPortFrom(bound.Addr().Unmap(), bound.Port())

		lc := net.ListenConfig{Control: control}

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

func tcp(ctx context.Context, local netip.AddrPort, control func(network, address string, c syscall.RawConn) error) (*net.TCPListener, error) {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		if err := sockopt.ReusePort(network, address, c); err != nil {
			return err
		}

		if control != nil {
			return control(network, address, c)
		}

		return nil
	}}

	// Plain TCP, not Multipath TCP, Go's default for listeners on Linux: SIP between fixed nodes gains nothing from it;
	// the P-CSCF's SAs are bound to the UE's address and ports (TS 33.203 §7.1), which subflows from other addresses
	// fall outside; and a destroyed MPTCP listener (SOCK_DESTROY) is not seen to fail.
	lc.SetMultipathTCP(false)

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
