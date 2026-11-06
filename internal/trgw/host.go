package trgw

import (
	"encoding/binary"
	"net"
	"net/netip"
	"sync"
	"time"
)

const hostRefresh = 10 * time.Second

type hostAddrs struct {
	list func() ([]netip.Prefix, error)

	mu      sync.Mutex
	fetched time.Time
	cached  []netip.Prefix
}

func newHostAddrs(list func() ([]netip.Prefix, error)) *hostAddrs {
	return &hostAddrs{list: list}
}

func (h *hostAddrs) prefixes(now time.Time) []netip.Prefix {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.fetched.IsZero() && now.Sub(h.fetched) < hostRefresh {
		return h.cached
	}

	if p, err := h.list(); err == nil {
		h.cached, h.fetched = p, now
	}

	return h.cached
}

func interfacePrefixes() ([]netip.Prefix, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}

	out := make([]netip.Prefix, 0, len(addrs))

	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}

		ip, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}

		ones, _ := n.Mask.Size()
		ip = ip.Unmap()

		if ip.Is4() && ones > 32 {
			ones -= 96
		}

		out = append(out, netip.PrefixFrom(ip, ones))
	}

	return out, nil
}

// RFC 1122 section 3.2.1.3
func isBroadcast(p netip.Prefix, a netip.Addr) bool {
	if !p.Addr().Is4() || !a.Is4() || p.Bits() < 0 || p.Bits() > 30 || !p.Contains(a) {
		return false
	}

	host := uint32(1)<<(32-p.Bits()) - 1
	v := a.As4()

	return binary.BigEndian.Uint32(v[:])&host == host
}
