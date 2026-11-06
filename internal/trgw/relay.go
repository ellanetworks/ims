package trgw

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/mcptt/rtp"
)

const (
	phoneLeg = iota
	trunkLeg
	legs
)

var legNames = [legs]string{phoneLeg: "phone", trunkLeg: "trunk"}

const bufferSize = 2048

const rtpVersion = 2

var (
	ErrFamily    = errors.New("trgw: no binding in the address family of the remote")
	ErrForbidden = errors.New("trgw: remote address not allowed")
)

// SIPconnect 2.0 section 14.10
type Direction uint8

const (
	SendRecv Direction = iota
	SendOnly
	RecvOnly
	Inactive
)

func (d Direction) String() string {
	switch d {
	case SendRecv:
		return "sendrecv"
	case SendOnly:
		return "sendonly"
	case RecvOnly:
		return "recvonly"
	case Inactive:
		return "inactive"
	}

	return fmt.Sprintf("Direction(%d)", uint8(d))
}

func (d Direction) sends() bool { return d == SendRecv || d == SendOnly }

func (d Direction) receives() bool { return d == SendRecv || d == RecvOnly }

// RFC 5761 section 5.1.1
type Remote struct {
	RTP       netip.AddrPort
	RTCP      netip.AddrPort
	Direction Direction
	Mux       bool
}

func (rm Remote) Known() bool {
	return rm.RTP.IsValid() && !rm.RTP.Addr().IsUnspecified()
}

const prevGrace = 5 * time.Second

type remote struct {
	Remote
	known     bool
	set       int64
	prev      *Remote
	prevUntil int64
	before    *remote
}

func (rs *remote) live(now int64) *Remote {
	if rs.prev == nil || rs.prevUntil != 0 && now >= rs.prevUntil {
		return nil
	}

	return rs.prev
}

func (rs *remote) heard(now int64) bool {
	return rs != nil && (rs.known || rs.live(now) != nil)
}

func (rs *remote) rtpFrom(a netip.AddrPort, now int64) (ok, current bool) {
	switch prev := rs.live(now); {
	case rs.known && a == rs.RTP:
		return true, true
	case prev != nil && a == prev.RTP:
		return true, false
	}

	return false, false
}

func (rs *remote) rtcpFrom(a netip.AddrPort, now int64) bool {
	prev := rs.live(now)
	return rs.known && a == rs.RTCP || prev != nil && a == prev.RTCP
}

func (rs *remote) muxed(current bool) bool {
	if current {
		return rs.Mux
	}

	return rs.prev != nil && rs.prev.Mux
}

func (rs *remote) settled() *remote {
	c := *rs
	c.prev, c.prevUntil = nil, 0

	return &c
}

type Relay struct {
	gw        *Gateway
	trunk     string
	metrics   *trunkMetrics
	legs      [legs]*Leg
	onTimeout func(*Relay)
	timeout   time.Duration
	wg        sync.WaitGroup
	done      chan struct{}

	mu       sync.Mutex
	timer    *time.Timer
	answered int64
	closed   bool
	err      error
}

const (
	ipv4 = iota
	ipv6
	families
)

func familyOf(a netip.Addr) int {
	return index(a.Unmap().Is4())
}

func index(is4 bool) int {
	if is4 {
		return ipv4
	}

	return ipv6
}

type Binding struct {
	LocalRTP       netip.AddrPort
	LocalRTCP      netip.AddrPort
	AdvertisedRTP  netip.AddrPort
	AdvertisedRTCP netip.AddrPort
}

type binding struct {
	pool      *portPool
	rtp       *net.UDPConn
	rtcp      *net.UDPConn
	localRTP  netip.AddrPort
	localRTCP netip.AddrPort
	received  atomic.Bool
	once      sync.Once
}

func (b *binding) public() Binding {
	return Binding{
		LocalRTP: b.localRTP, LocalRTCP: b.localRTCP,
		AdvertisedRTP:  netip.AddrPortFrom(b.pool.advertise, b.localRTP.Port()),
		AdvertisedRTCP: netip.AddrPortFrom(b.pool.advertise, b.localRTCP.Port()),
	}
}

func (b *binding) close() {
	b.once.Do(func() {
		_ = b.rtp.Close()
		_ = b.rtcp.Close()

		b.pool.release(b.localRTP.Port())
	})
}

type bindingSet struct {
	b       [families]*binding
	primary int
	keep    int
}

func (s *bindingSet) primaryBinding() *binding {
	if b := s.b[s.primary]; b != nil {
		return b
	}

	return s.b[1-s.primary]
}

// SIPconnect 2.0 section 14.2
type Leg struct {
	relay        *Relay
	side         int
	mu           sync.Mutex
	binds        atomic.Pointer[bindingSet]
	remote       atomic.Pointer[remote]
	police       atomic.Pointer[bucket]
	lastReceived atomic.Int64
}

func newLeg(r *Relay, side int) *Leg {
	l := &Leg{relay: r, side: side}
	l.binds.Store(&bindingSet{keep: -1})

	return l
}

func (r *Relay) Phone() *Leg { return r.legs[phoneLeg] }

func (r *Relay) Trunk() *Leg { return r.legs[trunkLeg] }

func (r *Relay) Done() <-chan struct{} { return r.done }

func (r *Relay) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.err
}

func (r *Relay) Close() error {
	r.close(ErrClosed)
	return nil
}

func (r *Relay) Answered() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed || r.timer != nil || r.timeout == 0 {
		return
	}

	r.answered = time.Now().UnixNano()
	r.timer = time.AfterFunc(r.timeout, r.checkMedia)
}

func (r *Relay) checkMedia() {
	now := time.Now().UnixNano()
	next := r.timeout

	for i, l := range r.legs {
		rm, to := l.remote.Load(), r.legs[1-i].remote.Load()
		if rm == nil || !rm.known || rm.Direction != SendRecv || to == nil || !to.known || !to.Direction.receives() {
			continue
		}

		idle := time.Duration(now - max(l.lastReceived.Load(), rm.set, r.answered))
		if idle >= r.timeout {
			if r.close(ErrMediaTimeout) && r.onTimeout != nil {
				r.onTimeout(r)
			}

			return
		}

		next = min(next, r.timeout-idle)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.closed {
		r.timer.Reset(next)
	}
}

func (r *Relay) close(reason error) bool {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		<-r.done

		return false
	}

	r.closed = true
	r.err = reason

	if r.timer != nil {
		r.timer.Stop()
	}
	r.mu.Unlock()

	for _, l := range r.legs {
		if l != nil {
			l.closeAll()
		}
	}

	r.wg.Wait()

	if r.metrics != nil {
		r.gw.metrics.close(r.trunk)
	}

	r.gw.forget(r)
	close(r.done)

	return true
}

func (r *Relay) serve(l *Leg, b *binding) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return ErrClosed
	}

	r.wg.Add(2)

	go r.relayRTP(l, b)
	go r.relayRTCP(l, b)

	return nil
}

func (r *Relay) start() {
	for _, l := range r.legs {
		for _, b := range l.binds.Load().b {
			if b != nil {
				r.wg.Add(2)

				go r.relayRTP(l, b)
				go r.relayRTCP(l, b)
			}
		}
	}
}

func (l *Leg) closeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, b := range l.binds.Load().b {
		if b != nil {
			b.close()
		}
	}
}

// TS 23.334 clause 5.21
// TS 29.162 clause 9.1.3
func (l *Leg) Open(a netip.Addr) (Binding, error) {
	p, err := l.relay.gw.pool(a)
	if err != nil {
		return Binding{}, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	set := *l.binds.Load()
	f := familyOf(p.addr)

	if b := set.b[f]; b != nil {
		return b.public(), nil
	}

	rtp, rtcp, err := p.open()
	if err != nil {
		return Binding{}, err
	}

	b := newBinding(p, rtp, rtcp)

	if err := l.relay.serve(l, b); err != nil {
		b.close()
		return Binding{}, err
	}

	if set.primaryBinding() == nil {
		set.primary = f
	}

	set.b[f] = b
	l.binds.Store(&set)

	return b.public(), nil
}

func newBinding(p *portPool, rtp, rtcp *net.UDPConn) *binding {
	return &binding{pool: p, rtp: rtp, rtcp: rtcp, localRTP: localAddr(rtp), localRTCP: localAddr(rtcp)}
}

// TS 23.334 clause 5.21
// TS 29.162 clause 9.1.3
// RFC 3264 section 8.3.1
func (l *Leg) Keep(is4 bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	set := *l.binds.Load()
	f := index(is4)

	other := set.b[1-f]
	if set.b[f] == nil || other == nil {
		return
	}

	set.primary, set.keep = f, f

	if !other.received.Load() || set.b[f].received.Load() {
		other.close()

		set.b[1-f], set.keep = nil, -1
	}

	l.binds.Store(&set)
}

func (l *Leg) Drop(is4 bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	set := *l.binds.Load()
	f := index(is4)

	if set.b[f] == nil || set.b[1-f] == nil || set.keep == f {
		return
	}

	set.b[f].close()
	set.b[f] = nil
	set.primary, set.keep = 1-f, -1
	l.binds.Store(&set)
}

func (l *Leg) arrived(b *binding) {
	if !b.received.Load() {
		b.received.Store(true)
	}

	set := l.binds.Load()
	if set.keep < 0 || set.b[set.keep] != b {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	cur := *l.binds.Load()
	if cur.keep < 0 || cur.b[cur.keep] != b {
		return
	}

	if other := cur.b[1-cur.keep]; other != nil {
		other.close()

		cur.b[1-cur.keep] = nil
	}

	cur.keep = -1
	l.binds.Store(&cur)
}

func (l *Leg) bound(a netip.Addr) *binding {
	return l.binds.Load().b[familyOf(a)]
}

func (l *Leg) Bound(is4 bool) (Binding, bool) {
	b := l.binds.Load().b[index(is4)]
	if b == nil {
		return Binding{}, false
	}

	return b.public(), true
}

func (l *Leg) Primary() (Binding, bool) {
	b := l.binds.Load().primaryBinding()
	if b == nil {
		return Binding{}, false
	}

	return b.public(), true
}

func (l *Leg) LocalRTP() netip.AddrPort {
	b, _ := l.Primary()
	return b.LocalRTP
}

func (l *Leg) LocalRTCP() netip.AddrPort {
	b, _ := l.Primary()
	return b.LocalRTCP
}

func (l *Leg) AdvertisedRTP() netip.AddrPort {
	b, _ := l.Primary()
	return b.AdvertisedRTP
}

func (l *Leg) AdvertisedRTCP() netip.AddrPort {
	b, _ := l.Primary()
	return b.AdvertisedRTCP
}

func (l *Leg) LastReceived() time.Time {
	if t := l.lastReceived.Load(); t != 0 {
		return time.Unix(0, t)
	}

	return time.Time{}
}

func localAddr(c *net.UDPConn) netip.AddrPort {
	a := c.LocalAddr().(*net.UDPAddr).AddrPort()
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
}

func (l *Leg) Remote() Remote {
	if rm := l.remote.Load(); rm != nil {
		return rm.Remote
	}

	return Remote{}
}

// RFC 3550 section 11
// RFC 5761 section 5.1.1
// RFC 8858 section 4.2
func (l *Leg) normalize(rm Remote) (Remote, error) {
	if rm.Direction > Inactive {
		return Remote{}, fmt.Errorf("trgw: direction %d", rm.Direction)
	}

	if !rm.RTP.IsValid() {
		if rm.RTCP.IsValid() {
			return Remote{}, errors.New("trgw: RTCP address without an RTP address")
		}

		return rm, nil
	}

	rm.RTP = netip.AddrPortFrom(rm.RTP.Addr().Unmap(), rm.RTP.Port())

	switch {
	case rm.Mux:
		rm.RTCP = rm.RTP
	case rm.RTCP.IsValid():
		rm.RTCP = netip.AddrPortFrom(rm.RTCP.Addr().Unmap(), rm.RTCP.Port())
	case rm.RTP.Port() == 0 || rm.RTP.Port() == 65535:
		return Remote{}, fmt.Errorf("trgw: no RTCP port follows RTP port %d", rm.RTP.Port())
	default:
		rm.RTCP = netip.AddrPortFrom(rm.RTP.Addr(), rm.RTP.Port()+1)
	}

	if !rm.Mux && rm.RTP == rm.RTCP {
		return Remote{}, fmt.Errorf("trgw: RTP and RTCP share %s without multiplexing", rm.RTP)
	}

	if rm.Known() {
		for _, a := range []netip.AddrPort{rm.RTP, rm.RTCP} {
			if a.Port() == 0 {
				return Remote{}, fmt.Errorf("trgw: remote %s has no port", a)
			}

			if err := l.reaches(a.Addr()); err != nil {
				return Remote{}, err
			}
		}
	}

	return rm, nil
}

// RFC 3264 section 8.3.1
// RFC 3264 section 8.4
func (l *Leg) SetRemote(rm Remote) error {
	rm, err := l.normalize(rm)
	if err != nil {
		return err
	}

	now := time.Now().UnixNano()
	known := rm.Known()
	next := &remote{Remote: rm, known: known, set: now}

	if old := l.remote.Load(); old != nil {
		if old.Remote == rm {
			return nil
		}

		snapshot := *old
		snapshot.before = nil
		next.before = &snapshot

		switch prev := old.live(now); {
		case !rm.RTP.IsValid():
		case old.known && (!known || old.RTP != rm.RTP || old.RTCP != rm.RTCP):
			p := old.Remote
			next.prev = &p
		case prev != nil && (!known || prev.RTP != rm.RTP || prev.RTCP != rm.RTCP):
			next.prev, next.prevUntil = prev, old.prevUntil
		}
	}

	l.remote.Store(next)

	return nil
}

// RFC 3264 section 8.3.1
func (l *Leg) Commit() {
	until := time.Now().Add(prevGrace).UnixNano()

	for {
		cur := l.remote.Load()
		if cur == nil || cur.before == nil && (cur.prev == nil || cur.prevUntil != 0) {
			return
		}

		next := *cur
		next.before = nil

		if next.prev != nil && next.prevUntil == 0 {
			next.prevUntil = until
		}

		if l.remote.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// RFC 3264 section 8.3.1
func (l *Leg) Revert(rm Remote) error {
	rm, err := l.normalize(rm)
	if err != nil {
		return err
	}

	for {
		cur := l.remote.Load()

		next := &remote{Remote: rm, known: rm.Known(), set: time.Now().UnixNano()}
		if cur != nil && cur.before != nil && cur.before.Remote == rm {
			next = cur.before
		}

		if l.remote.CompareAndSwap(cur, next) {
			return nil
		}
	}
}

func (l *Leg) reaches(a netip.Addr) error {
	b := l.bound(a)
	if b == nil {
		return fmt.Errorf("%w: %s", ErrFamily, a)
	}

	if !l.relay.gw.allowed(b.pool.addr, a) {
		return fmt.Errorf("%w: %s", ErrForbidden, a)
	}

	return nil
}

// RFC 1122 section 3.2.1.3
func (g *Gateway) allowed(local, a netip.Addr) bool {
	if local.IsLoopback() {
		return a.IsLoopback()
	}

	if a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() ||
		a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) || a.Is4() && a.As4()[0] == 0 {
		return false
	}

	for _, own := range g.addrs {
		if a == own.Bind || a == own.Advertise {
			return false
		}
	}

	if g.host == nil {
		return true
	}

	for _, p := range g.host.prefixes(time.Now()) {
		if a == p.Addr() || isBroadcast(p, a) {
			return false
		}
	}

	return true
}

// TS 29.162 clause 10.2.9
// TS 29.235 clause A.7.1.2.2.8.2
func (l *Leg) Police(bitsPerSecond uint64) {
	if bitsPerSecond == 0 {
		l.police.Store(nil)
		return
	}

	if cur := l.police.Load(); cur != nil && cur.bps == bitsPerSecond {
		return
	}

	l.police.Store(newBucket(bitsPerSecond, time.Now()))
}

func (l *Leg) admit(b *binding, n int, now time.Time) bool {
	p := l.police.Load()
	if p == nil {
		return true
	}

	overhead := udpHeader + ipv4Header
	if !b.pool.addr.Is4() {
		overhead = udpHeader + ipv6Header
	}

	return p.admit(n+overhead, now)
}

// RFC 3550 section A.1
// RFC 5761 section 4
// SIPconnect 2.0 section 14.2
// SIPconnect 2.0 section 14.9
func (r *Relay) relayRTP(src *Leg, b *binding) {
	defer r.wg.Done()

	dst := r.legs[1-src.side]
	m := &r.metrics.legs[src.side]
	buf := make([]byte, bufferSize+1)

	var loss lossCounter

	for {
		n, from, err := b.rtp.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}

			continue
		}

		now := time.Now()

		rs := src.remote.Load()
		if !rs.heard(now.UnixNano()) {
			m.dropped[dropNoRemote].Inc()
			continue
		}

		ok, current := rs.rtpFrom(netip.AddrPortFrom(from.Addr().Unmap(), from.Port()), now.UnixNano())
		if !ok {
			m.dropped[dropUnexpectedSource].Inc()
			continue
		}

		if n > bufferSize {
			m.dropped[dropMalformed].Inc()
			continue
		}

		if rs.muxed(current) && rtp.IsRTCP(buf[:n]) {
			r.forwardRTCP(m, src, b, dst, buf[:n])
			continue
		}

		p, err := rtp.Parse(buf[:n])
		if err != nil {
			m.dropped[dropMalformed].Inc()
			continue
		}

		if current && rs.prev != nil {
			src.remote.CompareAndSwap(rs, rs.settled())
		}

		src.arrived(b)
		src.lastReceived.Store(now.UnixNano())

		if lost := loss.update(p.SSRC, p.Sequence); lost > 0 {
			m.lost.Add(float64(lost))
		}

		if !src.admit(b, n, now) {
			m.dropped[dropPoliced].Inc()
			continue
		}

		rd := dst.remote.Load()
		if rd == nil || !rd.known {
			m.dropped[dropNoDestination].Inc()
			continue
		}

		if !rs.Direction.sends() || !rd.Direction.receives() {
			m.dropped[dropDirection].Inc()
			continue
		}

		db := dst.bound(rd.RTP.Addr())
		if db == nil {
			m.dropped[dropSendError].Inc()
			continue
		}

		if _, err := db.rtp.WriteToUDPAddrPort(buf[:n], rd.RTP); err != nil {
			m.dropped[dropSendError].Inc()
			continue
		}

		m.rtp.Inc()
	}
}

// SIPconnect 2.0 section 14.2
func (r *Relay) relayRTCP(src *Leg, b *binding) {
	defer r.wg.Done()

	dst := r.legs[1-src.side]
	m := &r.metrics.legs[src.side]
	buf := make([]byte, bufferSize+1)

	for {
		n, from, err := b.rtcp.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}

			continue
		}

		now := time.Now().UnixNano()

		rs := src.remote.Load()
		if !rs.heard(now) {
			m.dropped[dropNoRemote].Inc()
			continue
		}

		if !rs.rtcpFrom(netip.AddrPortFrom(from.Addr().Unmap(), from.Port()), now) {
			m.dropped[dropUnexpectedSource].Inc()
			continue
		}

		if n > bufferSize {
			m.dropped[dropMalformed].Inc()
			continue
		}

		r.forwardRTCP(m, src, b, dst, buf[:n])
	}
}

// RFC 5761 section 5.1.1
func (r *Relay) forwardRTCP(m *legMetrics, src *Leg, b *binding, dst *Leg, pkt []byte) {
	if !validRTCP(pkt) {
		m.dropped[dropMalformed].Inc()
		return
	}

	src.arrived(b)
	src.lastReceived.Store(time.Now().UnixNano())

	rd := dst.remote.Load()
	if rd == nil || !rd.known {
		m.dropped[dropNoDestination].Inc()
		return
	}

	db := dst.bound(rd.RTP.Addr())
	if db == nil {
		m.dropped[dropSendError].Inc()
		return
	}

	conn, to := db.rtcp, rd.RTCP
	if rd.Mux {
		conn = db.rtp
	}

	if _, err := conn.WriteToUDPAddrPort(pkt, to); err != nil {
		m.dropped[dropSendError].Inc()
		return
	}

	m.rtcp.Inc()
}

// RFC 3550 section A.2
// RFC 5761 section 4
func validRTCP(b []byte) bool {
	if !rtp.IsRTCP(b) {
		return false
	}

	off := 0

	for off+4 <= len(b) && b[off]>>6 == rtpVersion {
		off += 4 * (int(binary.BigEndian.Uint16(b[off+2:])) + 1)
	}

	return off == len(b)
}
