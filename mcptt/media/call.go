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

// Call is the media distributor of a call (TS 24.380 §6.3.4): the media of its talkers, the legs the floor control
// server has forward, sent to every other leg (§6.3.4.4.5 step 4: "from the permitted MCPTT client", "according to
// local policy"), in the directions negotiated (RFC 3264 §5.1, §6.1). It is an RTP translator (RFC 3550 §7.1): a
// talker's media is forwarded in the SSRC the server assigned its leg, which receivers associate it by (§6.2.4.4.2,
// §8.2.3.16), whichever SSRC the talker sent, and in the payload type each receiver negotiated (RFC 3264 §5.1); the
// RTCP of each leg is forwarded to the others with its SSRCs translated to match (RFC 3550 §7.2).
type Call struct {
	r *Relay

	mu     sync.Mutex
	closed bool
	legs   []*Leg
	// talkers are the legs whose media is forwarded: one in basic operation, more with dual floor or multi-talker.
	talkers []*Leg
	// dist is the distribution the readers forward by, rebuilt on each change.
	dist atomic.Pointer[distribution]
}

// distribution is an immutable snapshot of a call's legs and talkers.
type distribution struct {
	legs, talkers []*Leg
}

func (d *distribution) talking(l *Leg) bool { return slices.Contains(d.talkers, l) }

// NewCall is a call without legs.
func (r *Relay) NewCall() *Call {
	c := &Call{r: r}
	c.dist.Store(&distribution{})

	return c
}

// LegConfig is the media of a participant, as negotiated (TS 24.380 §4.3, §14).
type LegConfig struct {
	// Mux is RTP and RTCP on one port (RFC 5761).
	Mux bool
	// SharedControl is the leg's control channel on the shared port, by an SSRC assigned it (§14.2.7, §14.3.8): of
	// the leg of a peer that offered mc_floor_ssrc, or of a peer offered it, until its answer tells (SetMultiplexed).
	// A control port of its own otherwise.
	SharedControl bool
	// OnFloor is a floor control message of the peer (§8.2), malformed fields left out (§8.1.4).
	OnFloor func(floorctl.Message)
	// PayloadType is the payload type of the call's codec the peer negotiated: of the media it sends, and of the
	// media sent to it (RFC 3264 §5.1).
	PayloadType uint8
	// OnMedia is RTP media with payload from the peer, reported at most once per report interval: of a leg
	// without permission, which the floor control server revokes (§6.3.5.4.6), or forwarded.
	OnMedia func()
}

// Leg is the media of a participant of a call: its audio, its RTCP, its floor control channel. Its callbacks run on
// the readers of its sockets: they post to the call's strand and return.
type Leg struct {
	call *Call
	cfg  LegConfig

	audio, rtcp, control Conn
	audioSSRC, floorSSRC uint32

	// multiplexed is a peer on the shared control port that uses the SSRC assigned it, not its address.
	multiplexed atomic.Bool
	// own is the SSRC of the peer's RTP, 1<<32 set once known: the one its RTCP describes itself by, and the
	// one the reports on its media are to name to it.
	own      atomic.Uint64
	remote   atomic.Pointer[Remote]
	reported atomic.Int64
	closed   atomic.Bool
}

// Remote is the media of the peer of a leg, of its SDP: the addresses the leg sends to, its audio, its RTCP (the
// audio's port + 1 if zero, RFC 3550 §11), its control channel; and the direction of its audio. Media is received
// from any source: the SDP tells nothing of it (RFC 3264 §5.1).
type Remote struct {
	Audio, RTCP, Control netip.AddrPort
	Direction            Direction
}

// Direction is the direction of a leg's audio as negotiated, of the relay's side: the reverse of the peer's (RFC 3264
// §5.1, §6.1). RTCP flows whatever the direction (§5.1).
type Direction uint8

const (
	// SendRecv, the zero value: media to the peer and from it.
	SendRecv Direction = iota
	// SendOnly: media to the peer only, of a recvonly peer.
	SendOnly
	// RecvOnly: media from the peer only, of a sendonly peer.
	RecvOnly
	// Inactive: neither.
	Inactive
)

// sends reports whether the relay sends media to the peer.
func (d Direction) sends() bool { return d == SendRecv || d == SendOnly }

// receives reports whether the relay takes media from the peer.
func (d Direction) receives() bool { return d == SendRecv || d == RecvOnly }

// ErrClosed is the error of a leg added to a call or a relay closed.
var ErrClosed = errors.New("media: closed")

// AddLeg adds a leg to c, its ports open, its SSRCs assigned.
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

// publish stores the distribution of c's legs and talkers; c.mu held.
func (c *Call) publish() {
	c.dist.Store(&distribution{legs: slices.Clone(c.legs), talkers: slices.Clone(c.talkers)})
}

// Forward has the media of l forwarded to the other legs, or no longer (TS 24.380 §6.3.4, §6.3.5).
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

// Close closes the legs of c, and those added after.
func (c *Call) Close() {
	c.mu.Lock()
	c.closed = true
	legs := slices.Clone(c.legs)
	c.mu.Unlock()

	for _, l := range legs {
		l.Close()
	}
}

// Audio is the address of the leg's audio, for its SDP.
func (l *Leg) Audio() netip.AddrPort { return l.call.r.advertised(l.audio.LocalAddr()) }

// RTCP is the address of the leg's RTCP, for its SDP: the audio's when multiplexed (RFC 5761).
func (l *Leg) RTCP() netip.AddrPort {
	if l.rtcp == nil {
		return l.Audio()
	}

	return l.call.r.advertised(l.rtcp.LocalAddr())
}

// Control is the address of the leg's control channel, for its SDP.
func (l *Leg) Control() netip.AddrPort {
	if l.control != nil {
		return l.call.r.advertised(l.control.LocalAddr())
	}

	return l.call.r.advertised(l.call.r.control.LocalAddr())
}

// AudioSSRC is the SSRC the relay assigned the leg's media, unique among the relay's: the Audio SSRC of Granted
// Participant (§6.3.4.4.2 1f) and the mc_ssrc of an answer (§14.3.6), which the leg's media is forwarded in. A
// client before Rel-18, which keeps its own SSRC [PM 2], is heard all the same.
func (l *Leg) AudioSSRC() uint32 { return l.audioSSRC }

// FloorSSRC is the SSRC the server assigned the peer in mc_floor_ssrc, for its messages (§14.2.7, §14.3.8): of a
// leg on the shared control port.
func (l *Leg) FloorSSRC() (uint32, bool) { return l.floorSSRC, l.cfg.SharedControl }

// SetRemote sets the media of the peer, of its SDP.
func (l *Leg) SetRemote(r Remote) {
	if r.RTCP == (netip.AddrPort{}) && !l.cfg.Mux {
		r.RTCP = netip.AddrPortFrom(r.Audio.Addr(), r.Audio.Port()+1)
	}

	l.remote.Store(&r)

	if l.cfg.SharedControl && !l.multiplexed.Load() {
		l.call.r.byPeer(l, r.Control, true)
	}
}

// SetMultiplexed is the answer of a peer the leg's SDP offered mc_floor_ssrc, on the shared control port (§14.4):
// with mc_floor_ssrc, the peer multiplexes, its messages found by the SSRC assigned it; without, by their source,
// its control channel's address of the SDP (§8.1.2: its messages then carry an SSRC of its own).
func (l *Leg) SetMultiplexed(on bool) {
	if !l.cfg.SharedControl {
		return
	}

	l.multiplexed.Store(on)

	if rem := l.remote.Load(); rem != nil {
		l.call.r.byPeer(l, rem.Control, !on)
	}
}

// ErrNoRemote is the error of a message to a peer whose addresses are not known yet.
var ErrNoRemote = errors.New("media: peer address not known")

// SendFloor sends msg to the peer's control channel, one APP packet (TS 24.380 §8.1.1).
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

// Close closes the leg's sockets and removes it from its call.
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

// receiveAudio is a datagram on the audio port: RTP, or RTCP when multiplexed (RFC 5761 §4).
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
		// Without permission: not forwarded (§6.3.5.4.6, §6.3.5.7.2), reported for the Floor Revoke.
		st.Dropped.Add(1)

		if len(p.Payload) > 0 {
			l.report()
		}

		return
	}

	if rem := l.remote.Load(); p.PayloadType != l.cfg.PayloadType || rem == nil || !rem.Direction.receives() {
		// Not the format the leg negotiated, or of a peer that negotiated not to send (RFC 3264 §5.1, §6.1):
		// dropped, and not reported, as media the floor control server would keep the floor for (T1, T2).
		st.Dropped.Add(1)
		return
	}

	if len(p.Payload) > 0 {
		l.report()
	}

	// The talker by its leg (§6.3.4.4.5), in the Audio SSRC the server assigned it (§6.3.4.4.2 1f), and in each
	// receiver's payload type, the marker bit kept. b is the reader's: rewritten in place, written before the
	// next datagram.
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

// receiveRTCP is a datagram on the RTCP port.
func (l *Leg) receiveRTCP(b []byte, _ netip.AddrPort) {
	st := &l.call.r.stats
	st.Packets.Add(1)
	st.Octets.Add(uint64(len(b)))

	l.forwardRTCP(b)
}

// forwardRTCP forwards the RTCP of the leg to the others, as a translator does (RFC 3550 §7.2): the talker's sender
// reports and every leg's reception reports, SDES and BYE. A translator keeps the SSRCs (§7.1), and one that
// transforms the data "MUST make corresponding transformations in the SR and RR information so that it still
// reflects the characteristics of the data" (§7.2): the relay, which forwards a leg's RTP in the SSRC assigned it,
// translates the leg's own SSRC to the assigned one where the leg names itself (SR and RR sender, SDES chunks, BYE,
// feedback sender), so that receivers find the SDES CNAME and the sender report of the stream they receive; and,
// to each leg, the assigned SSRC back to its own where a report names the source reported on (report blocks,
// feedback media source), the stream that leg sent. Sender information is kept, as the payloads are (§7.2). b is the
// reader's: rewritten in place, written before the next datagram, without allocating for up to 16 SSRC fields.
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

// ownSSRC is the SSRC of the peer's RTP, if it sent any of another than the one assigned.
func (l *Leg) ownSSRC() (uint32, bool) {
	v := l.own.Load()

	return uint32(v), v != 0 && uint32(v) != l.audioSSRC
}

// translate replaces from by to in the SSRC fields of b at offs.
func translate(b []byte, offs []int, from, to uint32) {
	for _, off := range offs {
		if binary.BigEndian.Uint32(b[off:]) == from {
			binary.BigEndian.PutUint32(b[off:], to)
		}
	}
}

// report reports media to the call, at most once per report interval.
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

// receiveControl is a datagram on the leg's own control port.
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

// receiveFloor is a floor control message for the leg, of from.
func (l *Leg) receiveFloor(app *rtp.App, from netip.AddrPort) {
	if l.closed.Load() {
		l.call.r.dropFloor(app, from, "leg closed")
		return
	}

	msg, err := floorctl.Decode(app)
	if err != nil && !errors.Is(err, floorctl.ErrField) {
		// An unknown message is ignored whole (§8.1.4).
		l.call.r.dropFloor(app, from, err.Error())
		return
	}

	if l.cfg.OnFloor != nil {
		l.cfg.OnFloor(msg)
	}
}
