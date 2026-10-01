package transport

import (
	"fmt"
	"net/netip"
	"strconv"

	"github.com/ellanetworks/ims/sip"
)

func stampVia(r *sip.Request) error {
	via, err := r.Header.TopVia()
	if err != nil {
		return err
	}

	src := r.Flow.Remote
	_, rport := via.Params.Get("rport")
	changed := false

	if a, ok := via.Addr(); rport || !ok || a.Unmap() != src.Addr() || via.Params.Has("received") {
		via.Params.Set("received", src.Addr().String())

		changed = true
	}

	if rport {
		via.Params.Set("rport", strconv.Itoa(int(src.Port())))

		changed = true
	}

	if !changed {
		return nil
	}

	return r.Header.SetTopVia(via)
}

func udpDestination(via sip.Via) (netip.AddrPort, error) {
	a, err := viaAddr(via)
	if err != nil {
		return netip.AddrPort{}, err
	}

	port := via.Port
	if p, ok := via.RPort(); ok && p != 0 {
		port = p
	}

	return netip.AddrPortFrom(a, defaultPortIfZero(port)), nil
}

func tcpDestination(via sip.Via) (netip.AddrPort, error) {
	a, err := viaAddr(via)
	if err != nil {
		return netip.AddrPort{}, err
	}

	return netip.AddrPortFrom(a, defaultPortIfZero(via.Port)), nil
}

func viaAddr(via sip.Via) (netip.Addr, error) {
	if r := via.Received(); r != "" {
		a, ok := sip.HostAddr(r)
		if !ok {
			return netip.Addr{}, fmt.Errorf("top Via received %q: not an IP address", r)
		}

		return a.Unmap(), nil
	}

	a, ok := via.Addr()
	if !ok {
		return netip.Addr{}, fmt.Errorf("top Via sent-by %q: not an IP address", via.Host)
	}

	return a.Unmap(), nil
}

func defaultPortIfZero(p uint16) uint16 {
	if p == 0 {
		return sip.DefaultPort
	}

	return p
}
