package media

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
	"github.com/ellanetworks/ims/mcptt/rtp"
)

type Config struct {
	Network          Network
	Clock            interface{ Now() time.Time }
	Addr, Advertise  netip.Addr
	MinPort, MaxPort uint16
	ControlPort      uint16
	ReportInterval   time.Duration
	Logger           *slog.Logger
}

type Relay struct {
	cfg     Config
	control Conn

	ports sync.Mutex
	used  map[uint16]bool
	next  int

	ids    sync.RWMutex
	floor  map[uint32]*Leg
	peers  map[netip.AddrPort]*Leg
	audio  map[uint32]bool
	legs   map[*Leg]bool
	closed bool

	stats Stats
}

type Stats struct {
	Packets, Octets, Forwarded, Dropped atomic.Uint64
}

func New(cfg Config) (*Relay, error) {
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}

	if !cfg.Advertise.IsValid() {
		cfg.Advertise = cfg.Addr
	}

	if cfg.ReportInterval <= 0 {
		cfg.ReportInterval = 200 * time.Millisecond
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	if cfg.MinPort%2 != 0 || cfg.MaxPort < cfg.MinPort+1 {
		return nil, fmt.Errorf("media: port range %d-%d", cfg.MinPort, cfg.MaxPort)
	}

	r := &Relay{
		cfg: cfg, used: map[uint16]bool{}, floor: map[uint32]*Leg{}, peers: map[netip.AddrPort]*Leg{},
		audio: map[uint32]bool{}, legs: map[*Leg]bool{}, next: int(cfg.MinPort),
	}

	c, err := cfg.Network.Listen(netip.AddrPortFrom(cfg.Addr, cfg.ControlPort), r.receiveControl)
	if err != nil {
		return nil, fmt.Errorf("media: control port: %w", err)
	}

	r.control = c

	return r, nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (r *Relay) Stats() *Stats { return &r.stats }

func (r *Relay) Close() error {
	r.ids.Lock()
	r.closed = true
	legs := make([]*Leg, 0, len(r.legs))

	for l := range r.legs {
		legs = append(legs, l)
	}
	r.ids.Unlock()

	for _, l := range legs {
		l.Close()
	}

	return r.control.Close()
}

func (r *Relay) advertised(a netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(r.cfg.Advertise, a.Port())
}

var ErrNoPort = errors.New("media: no port left")

// RFC 3550 section 11
func (r *Relay) listen(even bool, hs ...func([]byte, netip.AddrPort)) ([]Conn, error) {
	r.ports.Lock()
	defer r.ports.Unlock()

	lo, hi := int(r.cfg.MinPort), int(r.cfg.MaxPort)
	span := hi - lo + 1
	start := r.next

	for i := range span {
		p := lo + (start-lo+i)%span
		if even && p%2 != 0 || p+len(hs)-1 > hi || r.taken(p, len(hs)) {
			continue
		}

		r.mark(p, len(hs), true)
		r.next = p + len(hs)

		r.ports.Unlock()
		cs, err := r.open(uint16(p), hs)
		r.ports.Lock()

		if err == nil {
			return cs, nil
		}

		r.mark(p, len(hs), false)
	}

	return nil, ErrNoPort
}

func (r *Relay) taken(p, n int) bool {
	for j := range n {
		if r.used[uint16(p+j)] {
			return true
		}
	}

	return false
}

func (r *Relay) mark(p, n int, used bool) {
	for j := range n {
		if used {
			r.used[uint16(p+j)] = true
		} else {
			delete(r.used, uint16(p+j))
		}
	}
}

func (r *Relay) open(port uint16, hs []func([]byte, netip.AddrPort)) ([]Conn, error) {
	var cs []Conn

	for i, h := range hs {
		c, err := r.cfg.Network.Listen(netip.AddrPortFrom(r.cfg.Addr, port+uint16(i)), h)
		if err != nil {
			for _, c := range cs {
				_ = c.Close()
			}

			return nil, err
		}

		cs = append(cs, c)
	}

	return cs, nil
}

func (r *Relay) release(cs ...Conn) {
	ports := make([]uint16, 0, 3)

	for _, c := range cs {
		if c != nil {
			ports = append(ports, c.LocalAddr().Port())
			_ = c.Close()
		}
	}

	r.ports.Lock()
	defer r.ports.Unlock()

	for _, p := range ports {
		delete(r.used, p)
	}
}

// RFC 3550 section 8.1
func assign(taken func(uint32) bool) uint32 {
	for {
		if ssrc := rand.Uint32(); !taken(ssrc) {
			return ssrc
		}
	}
}

// TS 24.380 clause 6.3.4.4.2
// TS 24.380 clause 14.2.7
// TS 24.380 clause 14.3.8
func (r *Relay) register(l *Leg) error {
	r.ids.Lock()
	defer r.ids.Unlock()

	if r.closed {
		return ErrClosed
	}

	l.audioSSRC = assign(func(s uint32) bool { return r.audio[s] })
	r.audio[l.audioSSRC] = true

	if l.cfg.SharedControl {
		l.floorSSRC = assign(func(s uint32) bool { return r.floor[s] != nil })
		r.floor[l.floorSSRC] = l
	}

	r.legs[l] = true

	return nil
}

func (r *Relay) unregister(l *Leg) {
	r.ids.Lock()
	defer r.ids.Unlock()

	delete(r.audio, l.audioSSRC)
	delete(r.legs, l)

	if r.floor[l.floorSSRC] == l {
		delete(r.floor, l.floorSSRC)
	}

	for a, o := range r.peers {
		if o == l {
			delete(r.peers, a)
		}
	}
}

func (r *Relay) byPeer(l *Leg, peer netip.AddrPort, on bool) {
	r.ids.Lock()
	defer r.ids.Unlock()

	for a, o := range r.peers {
		if o == l {
			delete(r.peers, a)
		}
	}

	if on {
		r.peers[peer] = l
	}
}

// TS 24.380 clause 4.3.3.1
// TS 24.380 clause 8.1.2
func (r *Relay) receiveControl(b []byte, from netip.AddrPort) {
	ps, err := rtp.ParseRTCP(b)
	if err != nil {
		r.stats.Dropped.Add(1)
	}

	for _, p := range ps {
		app, ok := p.(*rtp.App)
		if !ok || app.Name != floorctl.Name {
			continue
		}

		r.ids.RLock()

		l := r.floor[app.SSRC]
		if l == nil || !l.multiplexed.Load() {
			l = r.peers[from]
		}

		r.ids.RUnlock()

		if l == nil {
			r.dropFloor(app, from, "no leg")
			continue
		}

		l.receiveFloor(app, from)
	}
}

func (r *Relay) dropFloor(app *rtp.App, from netip.AddrPort, why string) {
	r.stats.Dropped.Add(1)
	r.cfg.Logger.Debug("floor control message dropped", slog.String("reason", why), slog.String("from", from.String()),
		slog.Uint64("ssrc", uint64(app.SSRC)), slog.Uint64("subtype", uint64(app.Subtype)))
}
