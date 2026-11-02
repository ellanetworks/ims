package media

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/ellanetworks/ims/mcptt/floorctl"
	"github.com/ellanetworks/ims/mcptt/rtp"
)

// TS 24.380 clause 6.3.4
// TS 24.380 clause 6.3.4.4.5
// RFC 3550 section 7.1
// RFC 3550 section 7.2
// RFC 3264 section 5.1
// RFC 3264 section 6.1
type Call struct {
	r *Relay

	mu      sync.Mutex
	closed  bool
	legs    []*Leg
	talkers []*Leg
	dist    atomic.Pointer[distribution]
}

type distribution struct {
	legs, talkers []*Leg
}

func (d *distribution) talking(l *Leg) bool { return slices.Contains(d.talkers, l) }

func (r *Relay) NewCall() *Call {
	c := &Call{r: r}
	c.dist.Store(&distribution{})

	return c
}

// TS 24.380 clause 4.3
// TS 24.380 clause 14
type LegConfig struct {
	Mux           bool
	SharedControl bool
	OnFloor       func(floorctl.Message)
	PayloadType   uint8
	OnMedia       func()
}

type Leg struct {
	call *Call
	cfg  LegConfig

	audio, rtcp, control Conn
	audioSSRC, floorSSRC uint32

	multiplexed atomic.Bool
	own         atomic.Uint64
	remote      atomic.Pointer[Remote]
	reported    atomic.Int64
	closed      atomic.Bool
}

type Remote struct {
	Audio, RTCP, Control netip.AddrPort
	Direction            Direction
}

// RFC 3264 section 5.1
// RFC 3264 section 6.1
type Direction uint8

const (
	SendRecv Direction = iota
	SendOnly
	RecvOnly
	Inactive
)

func (d Direction) sends() bool { return d == SendRecv || d == SendOnly }

func (d Direction) receives() bool { return d == SendRecv || d == RecvOnly }

var ErrClosed = errors.New("media: closed")

func (c *Call) AddLeg(cfg LegConfig) (*Leg, error) {
	l := &Leg{call: c, cfg: cfg}
	r := c.r

	hs := []func([]byte, netip.AddrPort){l.receiveAudio}
	if !cfg.Mux {
		hs = append(hs, l.receiveRTCP)
	}

	cs, err := r.listen(true, hs...)
	if err != nil {
		return nil, err
	}

	l.audio = cs[0]
	if !cfg.Mux {
		l.rtcp = cs[1]
	}

	if !cfg.SharedControl {
		cc, err := r.listen(false, l.receiveControl)
		if err != nil {
			r.release(l.audio, l.rtcp)
			return nil, err
		}

		l.control = cc[0]
	}

	l.multiplexed.Store(cfg.SharedControl)

	if err := r.register(l); err != nil {
		r.release(l.audio, l.rtcp, l.control)
		return nil, err
	}

	c.mu.Lock()

	if c.closed {
		c.mu.Unlock()
		l.Close()

		return nil, ErrClosed
	}

	c.legs = append(c.legs, l)
	c.publish()
	c.mu.Unlock()

	return l, nil
}

func (c *Call) publish() {
	c.dist.Store(&distribution{legs: slices.Clone(c.legs), talkers: slices.Clone(c.talkers)})
}

// TS 24.380 clause 6.3.4
// TS 24.380 clause 6.3.5
func (c *Call) Forward(l *Leg, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	i := slices.Index(c.talkers, l)

	switch {
	case on && i < 0 && !l.closed.Load():
		c.talkers = append(c.talkers, l)
	case !on && i >= 0:
		c.talkers = slices.Delete(c.talkers, i, i+1)
	default:
		return
	}

	c.publish()
}

func (c *Call) Close() {
	c.mu.Lock()
	c.closed = true
	legs := slices.Clone(c.legs)
	c.mu.Unlock()

	for _, l := range legs {
		l.Close()
	}
}

func (l *Leg) Audio() netip.AddrPort { return l.call.r.advertised(l.audio.LocalAddr()) }

// RFC 5761
func (l *Leg) RTCP() netip.AddrPort {
	if l.rtcp == nil {
		return l.Audio()
	}

	return l.call.r.advertised(l.rtcp.LocalAddr())
}

func (l *Leg) Control() netip.AddrPort {
	if l.control != nil {
		return l.call.r.advertised(l.control.LocalAddr())
	}

	return l.call.r.advertised(l.call.r.control.LocalAddr())
}

// TS 24.380 clause 6.3.4.4.2
// TS 24.380 clause 14.3.6
func (l *Leg) AudioSSRC() uint32 { return l.audioSSRC }

// TS 24.380 clause 14.2.7
// TS 24.380 clause 14.3.8
func (l *Leg) FloorSSRC() (uint32, bool) { return l.floorSSRC, l.cfg.SharedControl }

func (l *Leg) SetRemote(r Remote) {
	if r.RTCP == (netip.AddrPort{}) && !l.cfg.Mux {
		r.RTCP = netip.AddrPortFrom(r.Audio.Addr(), r.Audio.Port()+1)
	}

	l.remote.Store(&r)

	if l.cfg.SharedControl && !l.multiplexed.Load() {
		l.call.r.byPeer(l, r.Control, true)
	}
}

// TS 24.380 clause 14.4
func (l *Leg) SetMultiplexed(on bool) {
	if !l.cfg.SharedControl {
		return
	}

	l.multiplexed.Store(on)

	if rem := l.remote.Load(); rem != nil {
		l.call.r.byPeer(l, rem.Control, !on)
	}
}

var ErrNoRemote = errors.New("media: peer address not known")

// TS 24.380 clause 8.1.1
func (l *Leg) SendFloor(msg floorctl.Message) error {
	rem := l.remote.Load()
	if rem == nil || l.closed.Load() {
		return ErrNoRemote
	}

	app, err := msg.App()
	if err != nil {
		return err
	}

	b, err := rtp.AppendRTCP(nil, app)
	if err != nil {
		return err
	}

	c := l.control
	if c == nil {
		c = l.call.r.control
	}

	return c.WriteTo(b, rem.Control)
}

func (l *Leg) Close() {
	if l.closed.Swap(true) {
		return
	}

	c, r := l.call, l.call.r

	c.mu.Lock()
	c.legs = slices.DeleteFunc(c.legs, func(o *Leg) bool { return o == l })
	c.talkers = slices.DeleteFunc(c.talkers, func(o *Leg) bool { return o == l })
	c.publish()
	c.mu.Unlock()

	r.unregister(l)
	r.release(l.audio, l.rtcp, l.control)
}

// RFC 5761 section 4
func (l *Leg) receiveAudio(b []byte, _ netip.AddrPort) {
	st := &l.call.r.stats
	st.Packets.Add(1)
	st.Octets.Add(uint64(len(b)))

	if l.cfg.Mux && rtp.IsRTCP(b) {
		l.forwardRTCP(b)
		return
	}

	p, err := rtp.Parse(b)
	if err != nil {
		st.Dropped.Add(1)
		return
	}

	if v := uint64(p.SSRC) | 1<<32; l.own.Load() != v {
		l.own.Store(v)
	}

	d := l.call.dist.Load()
	if !d.talking(l) {
		st.Dropped.Add(1)

		if len(p.Payload) > 0 {
			l.report()
		}

		return
	}

	if rem := l.remote.Load(); p.PayloadType != l.cfg.PayloadType || rem == nil || !rem.Direction.receives() {
		st.Dropped.Add(1)
		return
	}

	if len(p.Payload) > 0 {
		l.report()
	}

	binary.BigEndian.PutUint32(b[8:12], l.audioSSRC)

	for _, o := range d.legs {
		if o == l {
			continue
		}

		orem := o.remote.Load()
		if orem == nil || !orem.Direction.sends() {
			continue
		}

		b[1] = b[1]&0x80 | o.cfg.PayloadType

		if o.audio.WriteTo(b, orem.Audio) == nil {
			st.Forwarded.Add(1)
		}
	}
}

func (l *Leg) receiveRTCP(b []byte, _ netip.AddrPort) {
	st := &l.call.r.stats
	st.Packets.Add(1)
	st.Octets.Add(uint64(len(b)))

	l.forwardRTCP(b)
}

// RFC 3550 section 7.2
// RFC 3550 section 7.1
func (l *Leg) forwardRTCP(b []byte) {
	st := &l.call.r.stats

	var sb, rb [16]int

	senders, sources, err := rtcpSSRCs(b, sb[:0], rb[:0])
	if err != nil {
		st.Dropped.Add(1)
		return
	}

	if own, ok := l.ownSSRC(); ok {
		translate(b, senders, own, l.audioSSRC)
	}

	for _, o := range l.call.dist.Load().legs {
		orem := o.remote.Load()
		if o == l || orem == nil {
			continue
		}

		c, to := o.rtcp, orem.RTCP
		if o.cfg.Mux {
			c, to = o.audio, orem.Audio
		}

		own, ok := o.ownSSRC()
		if ok {
			translate(b, sources, o.audioSSRC, own)
		}

		if c.WriteTo(b, to) == nil {
			st.Forwarded.Add(1)
		}

		if ok {
			translate(b, sources, own, o.audioSSRC)
		}
	}
}

func (l *Leg) ownSSRC() (uint32, bool) {
	v := l.own.Load()

	return uint32(v), v != 0 && uint32(v) != l.audioSSRC
}

func translate(b []byte, offs []int, from, to uint32) {
	for _, off := range offs {
		if binary.BigEndian.Uint32(b[off:]) == from {
			binary.BigEndian.PutUint32(b[off:], to)
		}
	}
}

func (l *Leg) report() {
	if l.cfg.OnMedia == nil {
		return
	}

	now := l.call.r.cfg.Clock.Now().UnixNano()
	last := l.reported.Load()

	if now-last < int64(l.call.r.cfg.ReportInterval) || !l.reported.CompareAndSwap(last, now) {
		return
	}

	l.cfg.OnMedia()
}

func (l *Leg) receiveControl(b []byte, from netip.AddrPort) {
	ps, err := rtp.ParseRTCP(b)
	if err != nil {
		l.call.r.stats.Dropped.Add(1)
	}

	for _, p := range ps {
		if app, ok := p.(*rtp.App); ok && app.Name == floorctl.Name {
			l.receiveFloor(app, from)
		}
	}
}

func (l *Leg) receiveFloor(app *rtp.App, from netip.AddrPort) {
	if l.closed.Load() {
		l.call.r.dropFloor(app, from, "leg closed")
		return
	}

	msg, err := floorctl.Decode(app)
	if err != nil && !errors.Is(err, floorctl.ErrField) {
		l.call.r.dropFloor(app, from, err.Error())
		return
	}

	if l.cfg.OnFloor != nil {
		l.cfg.OnFloor(msg)
	}
}
