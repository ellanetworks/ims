package trgw

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
)

var ErrNoPorts = errors.New("trgw: no free port pair")

type portPool struct {
	addr      netip.Addr
	advertise netip.Addr
	network   string

	mu   sync.Mutex
	free []uint16
}

// RFC 3550 section 11
func newPortPool(a Address, lo, hi uint16) *portPool {
	network := "udp6"
	if a.Bind.Is4() {
		network = "udp4"
	}

	p := &portPool{addr: a.Bind, advertise: a.Advertise, network: network}

	for port := int(lo); port+1 <= int(hi); port += 2 {
		p.free = append(p.free, uint16(port))
	}

	return p
}

func (p *portPool) take() (uint16, int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.free) == 0 {
		return 0, 0, false
	}

	port := p.free[0]
	p.free = p.free[1:]

	return port, len(p.free) + 1, true
}

// RFC 3550 section 11
func (p *portPool) open() (*net.UDPConn, *net.UDPConn, error) {
	var last error

	for tries, budget := 0, 1; tries < budget; tries++ {
		port, n, ok := p.take()
		if !ok {
			break
		}

		if tries == 0 {
			budget = n
		}

		rtp, rtcp, err := p.bind(port)
		if err == nil {
			return rtp, rtcp, nil
		}

		p.release(port)

		last = err
	}

	if last != nil {
		return nil, nil, fmt.Errorf("%w on %s: %w", ErrNoPorts, p.addr, last)
	}

	return nil, nil, fmt.Errorf("%w on %s", ErrNoPorts, p.addr)
}

func (p *portPool) bind(port uint16) (*net.UDPConn, *net.UDPConn, error) {
	rtp, err := p.listen(port)
	if err != nil {
		return nil, nil, err
	}

	rtcp, err := p.listen(port + 1)
	if err != nil {
		_ = rtp.Close()
		return nil, nil, err
	}

	return rtp, rtcp, nil
}

// TS 29.162 clause 10.2.7
// TS 29.235 clause A.7.1.2.2.4
func (p *portPool) listen(port uint16) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: markEF}

	c, err := lc.ListenPacket(context.Background(), p.network, netip.AddrPortFrom(p.addr, port).String())
	if err != nil {
		return nil, err
	}

	return c.(*net.UDPConn), nil
}

func (p *portPool) release(port uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.free = append(p.free, port)
}

func (p *portPool) available() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.free)
}
