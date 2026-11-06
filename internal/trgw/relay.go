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

type Remote struct {
	RTP       netip.AddrPort
	RTCP      netip.AddrPort
	Direction Direction
}

type remote struct {
	Remote
	known bool
	set   int64
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

// SIPconnect 2.0 section 14.2
type Leg struct {
	side         int
	pool         *portPool
	rtp          *net.UDPConn
	rtcp         *net.UDPConn
	localRTP     netip.AddrPort
	localRTCP    netip.AddrPort
	remote       atomic.Pointer[remote]
	lastReceived atomic.Int64
}

func newLeg(side int, pool *portPool, rtp, rtcp *net.UDPConn) *Leg {
	return &Leg{side: side, pool: pool, rtp: rtp, rtcp: rtcp, localRTP: localAddr(rtp), localRTCP: localAddr(rtcp)}
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

func (r *Relay) start(timeout time.Duration) {
	r.timeout = timeout

	for i, l := range r.legs {
		other := r.legs[1-i]

		r.wg.Add(2)

		go r.relayRTP(l, other)
		go r.relayRTCP(l, other)
	}
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
		_ = l.rtp.Close()
		_ = l.rtcp.Close()
	}

	r.wg.Wait()

	for _, l := range r.legs {
		l.pool.release(l.localRTP.Port())
	}

	r.gw.metrics.close(r.trunk)
	r.gw.forget(r)
	close(r.done)

	return true
}

func (l *Leg) close() {
	_ = l.rtp.Close()
	_ = l.rtcp.Close()
	l.pool.release(l.localRTP.Port())
}

func (l *Leg) LocalRTP() netip.AddrPort { return l.localRTP }

func (l *Leg) LocalRTCP() netip.AddrPort { return l.localRTCP }

func (l *Leg) AdvertisedRTP() netip.AddrPort {
	return netip.AddrPortFrom(l.pool.advertise, l.localRTP.Port())
}

func (l *Leg) AdvertisedRTCP() netip.AddrPort {
	return netip.AddrPortFrom(l.pool.advertise, l.localRTCP.Port())
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
func (l *Leg) SetRemote(rm Remote) error {
	if rm.Direction > Inactive {
		return fmt.Errorf("trgw: direction %d", rm.Direction)
	}

	if !rm.RTP.IsValid() {
		if rm.RTCP.IsValid() {
			return errors.New("trgw: RTCP address without an RTP address")
		}

		l.remote.Store(&remote{Remote: rm, set: time.Now().UnixNano()})

		return nil
	}

	rm.RTP = netip.AddrPortFrom(rm.RTP.Addr().Unmap(), rm.RTP.Port())

	if rm.RTCP.IsValid() {
		rm.RTCP = netip.AddrPortFrom(rm.RTCP.Addr().Unmap(), rm.RTCP.Port())
	} else {
		if rm.RTP.Port() == 0 || rm.RTP.Port() == 65535 {
			return fmt.Errorf("trgw: no RTCP port follows RTP port %d", rm.RTP.Port())
		}

		rm.RTCP = netip.AddrPortFrom(rm.RTP.Addr(), rm.RTP.Port()+1)
	}

	if rm.RTP == rm.RTCP {
		return fmt.Errorf("trgw: RTP and RTCP share %s", rm.RTP)
	}

	known := !rm.RTP.Addr().IsUnspecified()

	if known {
		for _, a := range []netip.AddrPort{rm.RTP, rm.RTCP} {
			if a.Port() == 0 {
				return fmt.Errorf("trgw: remote %s has no port", a)
			}

			if !l.reaches(a.Addr()) {
				return fmt.Errorf("trgw: %s can't send to %s", l.localRTP.Addr(), a.Addr())
			}
		}
	}

	l.remote.Store(&remote{Remote: rm, known: known, set: time.Now().UnixNano()})

	return nil
}

func (l *Leg) reaches(a netip.Addr) bool {
	return a.Is4() == l.pool.addr.Is4()
}

// RFC 3550 section A.1
// SIPconnect 2.0 section 14.2
// SIPconnect 2.0 section 14.9
func (r *Relay) relayRTP(src, dst *Leg) {
	defer r.wg.Done()

	m := &r.metrics.legs[src.side]
	buf := make([]byte, bufferSize+1)

	var loss lossCounter

	for {
		n, from, err := src.rtp.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}

			continue
		}

		rs := src.remote.Load()
		if rs == nil || !rs.known {
			m.dropped[dropNoRemote].Inc()
			continue
		}

		if netip.AddrPortFrom(from.Addr().Unmap(), from.Port()) != rs.RTP {
			m.dropped[dropUnexpectedSource].Inc()
			continue
		}

		if n > bufferSize || rtp.IsRTCP(buf[:n]) {
			m.dropped[dropMalformed].Inc()
			continue
		}

		p, err := rtp.Parse(buf[:n])
		if err != nil {
			m.dropped[dropMalformed].Inc()
			continue
		}

		src.lastReceived.Store(time.Now().UnixNano())

		if lost := loss.update(p.SSRC, p.Sequence); lost > 0 {
			m.lost.Add(float64(lost))
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

		if _, err := dst.rtp.WriteToUDPAddrPort(buf[:n], rd.RTP); err != nil {
			m.dropped[dropSendError].Inc()
			continue
		}

		m.rtp.Inc()
	}
}

// SIPconnect 2.0 section 14.2
func (r *Relay) relayRTCP(src, dst *Leg) {
	defer r.wg.Done()

	m := &r.metrics.legs[src.side]
	buf := make([]byte, bufferSize+1)

	for {
		n, from, err := src.rtcp.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}

			continue
		}

		rs := src.remote.Load()
		if rs == nil || !rs.known {
			m.dropped[dropNoRemote].Inc()
			continue
		}

		if netip.AddrPortFrom(from.Addr().Unmap(), from.Port()) != rs.RTCP {
			m.dropped[dropUnexpectedSource].Inc()
			continue
		}

		if n > bufferSize || !validRTCP(buf[:n]) {
			m.dropped[dropMalformed].Inc()
			continue
		}

		src.lastReceived.Store(time.Now().UnixNano())

		rd := dst.remote.Load()
		if rd == nil || !rd.known {
			m.dropped[dropNoDestination].Inc()
			continue
		}

		if _, err := dst.rtcp.WriteToUDPAddrPort(buf[:n], rd.RTCP); err != nil {
			m.dropped[dropSendError].Inc()
			continue
		}

		m.rtcp.Inc()
	}
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
