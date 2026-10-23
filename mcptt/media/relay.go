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

// Config is the configuration of a relay.
type Config struct {
	Network Network
	// Clock is the time of the media reports; time.Now without one.
	Clock interface{ Now() time.Time }
	// Addr is the address the relay's sockets bind; Advertise the one of the SDP c= lines, Addr if invalid.
	Addr, Advertise netip.Addr
	// MinPort, MaxPort are the range of the ports of the legs: audio on an even port, its RTCP on the next (RFC
	// 3550 §11) unless multiplexed (RFC 5761), floor control on a port of its own when not on the shared one.
	MinPort, MaxPort uint16
	// ControlPort is the port the control channels share (TS 24.380 §4.3.3.1); one the system chooses if zero.
	ControlPort uint16
	// ReportInterval is the least time between two reports of the media of a leg; 200 ms if zero: well below T1, at
	// least floor.MinT1, and T103 (TS 24.380 §11.1).
	ReportInterval time.Duration
	// Logger logs, at debug, the floor control messages the relay drops; none without one.
	Logger *slog.Logger
}

// Relay is the media plane of the server: its ports, the shared control port, and the calls on them.
type Relay struct {
	cfg     Config
	control Conn

	// ports serializes the allocation of ports; next is where the next search starts, MinPort at first and never
	// below, so that a port freed is taken again last.
	ports sync.Mutex
	used  map[uint16]bool
	next  int

	// ids guards the identifiers the legs are found by and the SSRCs the relay assigned, which every packet on the
	// shared control port reads.
	ids sync.RWMutex
	// floor are the legs on the shared control port by the SSRC the server assigned each in mc_floor_ssrc, peers
	// by the address of their control channel, of peers that do not multiplex (§14.4).
	floor map[uint32]*Leg
	peers map[netip.AddrPort]*Leg
	// audio are the Audio SSRCs assigned (§6.3.4.4.2 1f).
	audio map[uint32]bool
	// legs are the legs open, closed with the relay; closed, that the relay is, so that no leg is added.
	legs   map[*Leg]bool
	closed bool

	stats Stats
}

// Stats are the counters of a relay, since it started.
type Stats struct {
	// Packets and Octets received; Forwarded, the packets sent to the receivers; Dropped, those received and
	// not forwarded: of a leg without permission, of a payload type other than its leg's, malformed, or floor
	// control of no leg open (of a call released, or an SSRC and a source the relay does not know).
	Packets, Octets, Forwarded, Dropped atomic.Uint64
}

// New starts a relay: its shared control port open.
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

// Stats are the counters of r.
func (r *Relay) Stats() *Stats { return &r.stats }

// Close closes the shared control port and the legs, and returns once their readers returned (Conn.Close): a leg
// added after it gets ErrClosed.
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

// advertised is a local address of the relay as the SDP has it.
func (r *Relay) advertised(a netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(r.cfg.Advertise, a.Port())
}

// ErrNoPort is the error of a range without the ports a leg needs.
var ErrNoPort = errors.New("media: no port left")

// listen opens sockets on consecutive ports of the range, one per handler: an even first port for audio (RFC 3550
// §11), any for a control channel. The ports are reserved under the lock and the sockets opened outside it, which the
// allocations of the other calls wait for.
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

		// Taken outside the relay: the next ones.
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

// release closes the sockets cs, then frees their ports: closed outside the lock, which the allocations of the other
// calls wait for.
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

// assign is a new random SSRC, not in taken (RFC 3550 §8.1).
func assign(taken func(uint32) bool) uint32 {
	for {
		if ssrc := rand.Uint32(); !taken(ssrc) {
			return ssrc
		}
	}
}

// register records the identifiers of l: its Audio SSRC, unique among the relay's (TS 24.380 §6.3.4.4.2 1f), and
// on the shared control port the SSRC of its control channel, unique on it (§14.2.7, §14.3.8); ErrClosed once r is.
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

// byPeer has l found by the address of its peer's control channel on the shared port.
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

// receiveControl is a datagram on the shared control port: its floor control messages, each to the leg whose SSRC
// is in its header (TS 24.380 §4.3.3.1, §8.1.2), or to the leg of its source, of a peer that does not multiplex.
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

// dropFloor counts and logs a floor control message the relay drops, of from, for why.
func (r *Relay) dropFloor(app *rtp.App, from netip.AddrPort, why string) {
	r.stats.Dropped.Add(1)
	r.cfg.Logger.Debug("floor control message dropped", slog.String("reason", why), slog.String("from", from.String()),
		slog.Uint64("ssrc", uint64(app.SSRC)), slog.Uint64("subtype", uint64(app.Subtype)))
}
