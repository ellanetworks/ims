package ibcf

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ellanetworks/ims/internal/trgw"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
)

type MediaConfig struct {
	Gateway *trgw.Gateway
	Phone   []netip.Addr
	Trunk   []netip.Addr
}

func (m MediaConfig) bind(addrs []netip.Addr, family netip.Addr) netip.Addr {
	for _, a := range addrs {
		if a.Is4() == family.Unmap().Is4() {
			return a
		}
	}

	if len(addrs) > 0 {
		return addrs[0]
	}

	return netip.Addr{}
}

const (
	phoneSide = iota
	trunkSide
)

var errNoMedia = errors.New("ibcf: no TrGW address for the media")

type origin struct {
	id      string
	version uint64
	sent    []byte
}

type saved struct {
	leg    *trgw.Leg
	remote trgw.Remote
}

// TS 24.229 clause 6.7.1.4
// TS 24.229 clause 6.7.2.1
// RFC 3264 section 8
// SIPconnect 2.0 section 14.1
type media struct {
	c    *call
	cfg  MediaConfig
	call *b2bua.Call

	mu       sync.Mutex
	relays   []*trgw.Relay
	origins  [2]origin
	answered bool
	closed   bool
}

func (c *call) side(l *b2bua.Leg) int {
	if c.towardsTrunk(l) {
		return trunkSide
	}

	return phoneSide
}

func (m *media) Offer(x *b2bua.Exchange) ([]byte, error) {
	s, err := sdp.Parse(x.Offer)
	if err != nil {
		return nil, err
	}

	from, to := m.c.side(x.From), m.c.side(x.To)

	var undo []saved

	for i, md := range s.Media {
		if !relayable(md) {
			md.Disable()
			continue
		}

		r, err := m.relay(i, s, from)
		if err != nil {
			m.restore(undo)
			return nil, err
		}

		in := mediaLeg(r, from)
		undo = append(undo, saved{leg: in, remote: in.Remote()})

		if err := in.SetRemote(remoteOf(s, i)); err != nil {
			m.restore(undo)
			return nil, err
		}
	}

	x.Value = undo

	return m.rewrite(s, to)
}

func (m *media) Answer(x *b2bua.Exchange, _ *ua.Session, body []byte) ([]byte, error) {
	s, err := sdp.Parse(body)
	if err != nil {
		return nil, err
	}

	from, to := m.c.side(x.To), m.c.side(x.From)

	m.mu.Lock()
	relays := slices.Clone(m.relays)
	m.mu.Unlock()

	for i, md := range s.Media {
		if i >= len(relays) || relays[i] == nil || !relayable(md) {
			md.Disable()
			continue
		}

		if err := mediaLeg(relays[i], from).SetRemote(remoteOf(s, i)); err != nil {
			return nil, err
		}
	}

	return m.rewrite(s, to)
}

func (m *media) End(x *b2bua.Exchange, s *ua.Session) {
	if s != nil {
		return
	}

	if undo, ok := x.Value.([]saved); ok {
		m.restore(undo)
	}
}

func (m *media) restore(undo []saved) {
	for _, u := range slices.Backward(undo) {
		_ = u.leg.SetRemote(u.remote)
	}
}

// SIPconnect 2.0 section 5
func relayable(md *sdp.Media) bool {
	d, err := md.Desc()

	return err == nil && d.Port != 0 && strings.EqualFold(d.Type, sdp.Audio) && d.IsRTP()
}

func mediaLeg(r *trgw.Relay, side int) *trgw.Leg {
	if side == trunkSide {
		return r.Trunk()
	}

	return r.Phone()
}

func (m *media) relay(i int, s *sdp.Session, from int) (*trgw.Relay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, trgw.ErrClosed
	}

	for len(m.relays) <= i {
		m.relays = append(m.relays, nil)
	}

	if r := m.relays[i]; r != nil {
		return r, nil
	}

	end, err := s.RTPEndpoint(i)
	if err != nil {
		return nil, err
	}

	signal := m.c.trunkFamily()
	phone, trunk := m.cfg.bind(m.cfg.Phone, netip.IPv4Unspecified()), m.cfg.bind(m.cfg.Trunk, signal)

	if from == phoneSide {
		phone = m.cfg.bind(m.cfg.Phone, end.Addr)
	} else {
		trunk = m.cfg.bind(m.cfg.Trunk, end.Addr)
	}

	if !phone.IsValid() || !trunk.IsValid() {
		return nil, errNoMedia
	}

	t, _ := m.c.owner()

	r, err := m.cfg.Gateway.Allocate(context.Background(), trgw.Allocation{
		TrunkID: t.ID, PhoneAddress: phone, TrunkAddress: trunk, OnMediaTimeout: m.timedOut,
	})
	if err != nil {
		return nil, err
	}

	m.relays[i] = r

	if m.answered {
		r.Answered()
	}

	return r, nil
}

// RFC 3264 section 5.1
// RFC 3264 section 8.4
// RFC 4566 section 5.7
func remoteOf(s *sdp.Session, i int) trgw.Remote {
	rm := trgw.Remote{Direction: direction(s.MediaDirection(i))}

	rtp, err := s.RTPEndpoint(i)
	if err != nil {
		return rm
	}

	rm.RTP = netip.AddrPortFrom(rtp.Addr, rtp.Port)

	if rtcp, err := s.RTCPEndpoint(i); err == nil && !rtp.Addr.IsUnspecified() {
		rm.RTCP = netip.AddrPortFrom(rtcp.Addr, rtcp.Port)
	}

	return rm
}

func direction(d sdp.Direction) trgw.Direction {
	switch d {
	case sdp.SendOnly:
		return trgw.SendOnly
	case sdp.RecvOnly:
		return trgw.RecvOnly
	case sdp.Inactive:
		return trgw.Inactive
	}

	return trgw.SendRecv
}

var dropped = []string{"rtcp", "rtcp-mux", "candidate", "remote-candidates", "end-of-candidates", "ice-ufrag", "ice-pwd", "ice-options", "ice-lite", "ice-mismatch"}

// TS 24.229 clause 6.7.1.4
// RFC 3605 section 2.1
// RFC 4566 section 5.2
func (m *media) rewrite(s *sdp.Session, to int) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var addr netip.Addr

	for i, md := range s.Media {
		for _, name := range dropped {
			md.DelAttr(name)
		}

		if md.Port() == 0 || i >= len(m.relays) || m.relays[i] == nil {
			md.Disable()
			continue
		}

		l := mediaLeg(m.relays[i], to)
		rtp, rtcp := l.AdvertisedRTP(), l.AdvertisedRTCP()

		d, _ := md.Desc()
		d.Port = rtp.Port()
		md.SetDesc(d)
		md.Del('c')

		if rtcp.Port() != rtp.Port()+1 {
			md.AddAttr("rtcp", strconv.Itoa(int(rtcp.Port())))
		}

		if !addr.IsValid() {
			addr = rtp.Addr()
		} else if addr != rtp.Addr() {
			md.Set('c', sdp.NewConnection(rtp.Addr()).String())
		}
	}

	for _, name := range dropped {
		s.DelAttr(name)
	}

	if !addr.IsValid() {
		addr = m.cfg.bind(m.cfg.Phone, netip.IPv4Unspecified())
		if to == trunkSide {
			addr = m.cfg.bind(m.cfg.Trunk, m.c.trunkFamily())
		}
	}

	if !addr.IsValid() {
		return nil, errNoMedia
	}

	s.SetConnection(sdp.NewConnection(addr))

	return m.stamp(s, to, addr)
}

// RFC 4566 section 5.2
// SIPconnect 2.0 section 14.1
func (m *media) stamp(s *sdp.Session, to int, addr netip.Addr) ([]byte, error) {
	o := &m.origins[to]
	if o.id == "" {
		o.id = strconv.FormatUint(uint64(newSessionID()), 10)
	}

	family := sdp.IP4
	if !addr.Is4() {
		family = sdp.IP6
	}

	origin := sdp.Origin{Username: "-", SessionID: o.id, NetType: sdp.NetTypeIN, AddrType: family, Address: addr.String()}

	origin.SessionVersion = strconv.FormatUint(o.version, 10)
	s.SetOrigin(origin)
	body := s.Bytes()

	if o.sent != nil && bytes.Equal(body, o.sent) {
		return body, nil
	}

	if o.sent != nil {
		o.version++
	}

	origin.SessionVersion = strconv.FormatUint(o.version, 10)
	s.SetOrigin(origin)
	o.sent = s.Bytes()

	return o.sent, nil
}

func (m *media) answer() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.answered = true

	for _, r := range m.relays {
		if r != nil {
			r.Answered()
		}
	}
}

func (m *media) timedOut(*trgw.Relay) {
	bc := m.call
	if bc == nil {
		return
	}

	bc.Strand().Post(func() {
		if bc.Ended() {
			return
		}

		reason, _ := sip.NewReason("SIP", 408, "Media timeout")
		bc.End(context.Background(), ua.Reply{Header: ua.Reasons(reason).Header})
	})
}

func (m *media) close() {
	m.mu.Lock()
	relays := m.relays
	m.relays, m.closed = nil, true
	m.mu.Unlock()

	for _, r := range relays {
		if r != nil {
			_ = r.Close()
		}
	}
}

func newSessionID() uint32 {
	return rand.Uint32()
}
