package trgw

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

const (
	DefaultMinPort = 10000
	DefaultMaxPort = 19999
	minPortFloor   = 1024
)

var (
	ErrClosed       = errors.New("trgw: closed")
	ErrMediaTimeout = errors.New("trgw: no media")
)

type Address struct {
	Bind      netip.Addr
	Advertise netip.Addr
}

type options struct {
	addrs        []Address
	minPort      uint16
	maxPort      uint16
	metrics      *Metrics
	mediaTimeout time.Duration
}

type Option func(*options)

func WithAddresses(addrs ...Address) Option {
	return func(o *options) { o.addrs = append(o.addrs, addrs...) }
}

func WithPortRange(minPort, maxPort uint16) Option {
	return func(o *options) { o.minPort, o.maxPort = minPort, maxPort }
}

func WithMetrics(m *Metrics) Option {
	return func(o *options) { o.metrics = m }
}

func WithMediaTimeout(d time.Duration) Option {
	return func(o *options) { o.mediaTimeout = d }
}

type Gateway struct {
	addrs        []Address
	pools        map[netip.Addr]*portPool
	metrics      *Metrics
	mediaTimeout time.Duration

	mu     sync.Mutex
	relays map[*Relay]struct{}
	closed bool
}

// RFC 3550 section 11
func New(opts ...Option) (*Gateway, error) {
	o := options{minPort: DefaultMinPort, maxPort: DefaultMaxPort}
	for _, opt := range opts {
		opt(&o)
	}

	switch {
	case len(o.addrs) == 0:
		return nil, errors.New("trgw: no bind address")
	case o.minPort < minPortFloor:
		return nil, fmt.Errorf("trgw: port range starts at %d, below %d", o.minPort, minPortFloor)
	case o.minPort%2 != 0:
		return nil, fmt.Errorf("trgw: port range starts at odd port %d", o.minPort)
	case o.maxPort <= o.minPort:
		return nil, fmt.Errorf("trgw: port range %d-%d is empty", o.minPort, o.maxPort)
	case o.mediaTimeout < 0:
		return nil, fmt.Errorf("trgw: media timeout %s", o.mediaTimeout)
	}

	if o.metrics == nil {
		o.metrics = NewMetrics()
	}

	g := &Gateway{
		pools: map[netip.Addr]*portPool{}, metrics: o.metrics, mediaTimeout: o.mediaTimeout,
		relays: map[*Relay]struct{}{},
	}

	for _, a := range o.addrs {
		a, err := normalize(a)
		if err != nil {
			return nil, err
		}

		if _, ok := g.pools[a.Bind]; ok {
			return nil, fmt.Errorf("trgw: bind address %s given twice", a.Bind)
		}

		g.addrs = append(g.addrs, a)
		g.pools[a.Bind] = newPortPool(a, o.minPort, o.maxPort)
	}

	return g, nil
}

func normalize(a Address) (Address, error) {
	if !a.Bind.IsValid() || a.Bind.IsUnspecified() {
		return Address{}, fmt.Errorf("trgw: bind address %s is not a unicast address", a.Bind)
	}

	a.Bind = a.Bind.Unmap()

	if !a.Advertise.IsValid() {
		a.Advertise = a.Bind
	}

	a.Advertise = a.Advertise.Unmap()

	if a.Advertise.IsUnspecified() {
		return Address{}, fmt.Errorf("trgw: advertised address %s is unspecified", a.Advertise)
	}

	if a.Advertise.Is4() != a.Bind.Is4() {
		return Address{}, fmt.Errorf("trgw: advertised address %s and bind address %s differ in family",
			a.Advertise, a.Bind)
	}

	return a, nil
}

func (g *Gateway) Addresses() []Address {
	return append([]Address(nil), g.addrs...)
}

type Allocation struct {
	TrunkID        string
	PhoneAddress   netip.Addr
	TrunkAddress   netip.Addr
	OnMediaTimeout func(*Relay)
}

func (g *Gateway) pool(a netip.Addr) (*portPool, error) {
	if !a.IsValid() {
		return g.pools[g.addrs[0].Bind], nil
	}

	p := g.pools[a.Unmap()]
	if p == nil {
		return nil, fmt.Errorf("trgw: %s is not a bind address", a)
	}

	return p, nil
}

func (g *Gateway) Allocate(ctx context.Context, a Allocation) (*Relay, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	pools := [legs]*portPool{}

	for i, addr := range [legs]netip.Addr{a.PhoneAddress, a.TrunkAddress} {
		p, err := g.pool(addr)
		if err != nil {
			return nil, err
		}

		pools[i] = p
	}

	if g.isClosed() {
		return nil, ErrClosed
	}

	r := &Relay{gw: g, metrics: g.metrics.trunk(a.TrunkID), onTimeout: a.OnMediaTimeout, done: make(chan struct{})}

	for i, p := range pools {
		rtp, rtcp, err := p.open()
		if err == nil {
			err = ctx.Err()
		}

		if err != nil {
			if rtp != nil {
				_ = rtp.Close()
				_ = rtcp.Close()

				p.release(localAddr(rtp).Port())
			}

			for _, l := range r.legs[:i] {
				l.close()
			}

			return nil, err
		}

		r.legs[i] = newLeg(i, p, rtp, rtcp)
	}

	r.metrics.relays.Inc()
	r.start(g.mediaTimeout)

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		r.close(ErrClosed)

		return nil, ErrClosed
	}

	g.relays[r] = struct{}{}
	g.mu.Unlock()

	return r, nil
}

func (g *Gateway) isClosed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.closed
}

func (g *Gateway) Close() error {
	g.mu.Lock()
	g.closed = true
	relays := make([]*Relay, 0, len(g.relays))

	for r := range g.relays {
		relays = append(relays, r)
	}
	g.mu.Unlock()

	for _, r := range relays {
		r.close(ErrClosed)
	}

	return nil
}

func (g *Gateway) Relays() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return len(g.relays)
}

func (g *Gateway) forget(r *Relay) {
	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.relays, r)
}
