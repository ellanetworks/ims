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

	"github.com/ellanetworks/ims/internal/tracing"
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

var (
	errNoMedia  = errors.New("ibcf: no TrGW address for the media")
	errNoStream = errors.New("ibcf: no audio stream the TrGW relays")
)

type origin struct {
	id      string
	version uint64
	sent    []byte
}

// RFC 3264 section 8
type dialogMedia struct {
	remote  [2]*trgw.Remote
	origins [2]origin
	touched uint64
}

type stream struct {
	index int
	relay *trgw.Relay
	refs  int
	kept  bool
}

type pending struct {
	dialog *ua.Session
	side   int
	prev   *trgw.Remote
	stream *stream
}

// TS 24.229 clause 6.7.1.1
// TS 24.229 clause 6.7.1.4
// RFC 3264 section 8
// SIPconnect 2.0 section 14.1
type media struct {
	c    *call
	cfg  MediaConfig
	call *b2bua.Call

	mu        sync.Mutex
	stream    *stream
	base      *dialogMedia
	dialogs   map[*ua.Session]*dialogMedia
	active    *ua.Session
	confirmed bool
	touches   uint64
	answered  bool
	closed    bool
}

func newMedia(c *call, cfg MediaConfig, bc *b2bua.Call) *media {
	return &media{c: c, cfg: cfg, call: bc, base: &dialogMedia{}, dialogs: map[*ua.Session]*dialogMedia{}}
}

func (c *call) side(l *b2bua.Leg) int {
	if c.towardsTrunk(l) {
		return trunkSide
	}

	return phoneSide
}

// SIPconnect 2.0 section 5
func (m *media) Offer(x *b2bua.Exchange) ([]byte, error) {
	s, err := sdp.Parse(x.Offer)
	if err != nil {
		return nil, err
	}

	from, to := m.c.side(x.From), m.c.side(x.To)

	m.mu.Lock()
	defer m.mu.Unlock()

	i := m.pick(s)
	if i < 0 {
		return nil, errNoStream
	}

	st, err := m.open(i, s, from)
	if err != nil {
		return nil, err
	}

	st.refs++

	p := &pending{dialog: x.Dialog, side: from, prev: m.state(x.Dialog).remote[from], stream: st}

	if err := m.set(x.Dialog, from, remoteOf(s, i)); err != nil {
		m.release(st)
		return nil, err
	}

	x.Value = p

	return m.rewrite(s, x.Dialog, to)
}

func (m *media) Answer(x *b2bua.Exchange, dialog *ua.Session, body []byte) ([]byte, error) {
	s, err := sdp.Parse(body)
	if err != nil {
		return nil, err
	}

	from, to := m.c.side(x.To), m.c.side(x.From)

	m.mu.Lock()
	defer m.mu.Unlock()

	if st := m.stream; st != nil && st.index < len(s.Media) && relayable(s.Media[st.index]) {
		if err := m.set(dialog, from, remoteOf(s, st.index)); err != nil {
			return nil, err
		}
	}

	return m.rewrite(s, dialog, to)
}

// RFC 3264 section 8
func (m *media) End(x *b2bua.Exchange, dialog *ua.Session) {
	p, ok := x.Value.(*pending)
	if !ok {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	case dialog != nil:
		p.stream.kept = true
	case x.Initial && p.dialog == nil:
		m.base, m.dialogs, m.active, m.confirmed = &dialogMedia{}, map[*ua.Session]*dialogMedia{}, nil, false
	default:
		if d := m.known(p.dialog); d != nil {
			d.remote[p.side] = p.prev

			if p.dialog == m.active {
				m.apply(d)
			}
		}
	}

	m.release(p.stream)
}

// RFC 3261 section 13.2.2.4
func (m *media) Dialog(dialog *ua.Session, state ua.State) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch state {
	case ua.Confirmed:
		d := m.state(dialog)
		m.dialogs = map[*ua.Session]*dialogMedia{dialog: d}
		m.confirmed = true
		m.activate(dialog, d)
	case ua.Terminated:
		if m.confirmed {
			return
		}

		delete(m.dialogs, dialog)

		if m.active != dialog {
			return
		}

		var latest *ua.Session

		for s, d := range m.dialogs {
			if latest == nil || d.touched > m.dialogs[latest].touched {
				latest = s
			}
		}

		m.activate(latest, m.state(latest))
	}
}

func (m *media) known(dialog *ua.Session) *dialogMedia {
	if dialog == nil {
		return m.base
	}

	return m.dialogs[dialog]
}

func (m *media) state(dialog *ua.Session) *dialogMedia {
	if d := m.known(dialog); d != nil {
		return d
	}

	d := *m.base
	m.dialogs[dialog] = &d

	return &d
}

func (m *media) set(dialog *ua.Session, side int, rm trgw.Remote) error {
	if m.confirmed && dialog != m.active {
		return nil
	}

	d := m.state(dialog)
	m.activate(dialog, d)

	if err := mediaLeg(m.stream.relay, side).SetRemote(rm); err != nil {
		return err
	}

	m.touches++
	d.remote[side], d.touched = &rm, m.touches

	return nil
}

func (m *media) activate(dialog *ua.Session, d *dialogMedia) {
	if m.active == dialog {
		return
	}

	m.active = dialog
	m.apply(d)
}

func (m *media) apply(d *dialogMedia) {
	if m.stream == nil {
		return
	}

	for side, rm := range d.remote {
		if rm == nil {
			rm = &trgw.Remote{}
		}

		_ = mediaLeg(m.stream.relay, side).SetRemote(*rm)
	}
}

func (m *media) pick(s *sdp.Session) int {
	if m.stream == nil {
		return slices.IndexFunc(s.Media, relayable)
	}

	if i := m.stream.index; i < len(s.Media) && relayable(s.Media[i]) {
		return i
	}

	return -1
}

// SIPconnect 2.0 section 5
// SIPconnect 2.0 section 14.2
func relayable(md *sdp.Media) bool {
	d, err := md.Desc()

	return err == nil && d.Port != 0 && strings.EqualFold(d.Type, sdp.Audio) &&
		(strings.EqualFold(d.Proto, "RTP/AVP") || strings.EqualFold(d.Proto, "RTP/AVPF"))
}

func mediaLeg(r *trgw.Relay, side int) *trgw.Leg {
	if side == trunkSide {
		return r.Trunk()
	}

	return r.Phone()
}

func (m *media) open(i int, s *sdp.Session, from int) (*stream, error) {
	if m.closed {
		return nil, trgw.ErrClosed
	}

	if m.stream != nil {
		return m.stream, nil
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

	if m.answered {
		r.Answered()
	}

	m.stream = &stream{index: i, relay: r}

	return m.stream, nil
}

func (m *media) release(st *stream) {
	st.refs--

	if st.refs > 0 || st.kept || m.stream != st {
		return
	}

	m.stream = nil

	_ = st.relay.Close()
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

	if rtp.Addr.IsUnspecified() {
		rm.Direction = held(rm.Direction)
		s.Media[i].SetDirection(sdpDirection(rm.Direction))

		return rm
	}

	if rtcp, err := s.RTCPEndpoint(i); err == nil {
		rm.RTCP = netip.AddrPortFrom(rtcp.Addr, rtcp.Port)
	}

	return rm
}

// RFC 3264 section 8.4
func held(d trgw.Direction) trgw.Direction {
	switch d {
	case trgw.SendRecv:
		return trgw.SendOnly
	case trgw.RecvOnly:
		return trgw.Inactive
	}

	return d
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

func sdpDirection(d trgw.Direction) sdp.Direction {
	switch d {
	case trgw.SendOnly:
		return sdp.SendOnly
	case trgw.RecvOnly:
		return sdp.RecvOnly
	case trgw.Inactive:
		return sdp.Inactive
	}

	return sdp.SendRecv
}

var dropped = []string{"rtcp", "rtcp-mux", "candidate", "remote-candidates", "end-of-candidates", "ice-ufrag", "ice-pwd", "ice-options", "ice-lite", "ice-mismatch"}

// TS 24.229 clause 6.7.1.2.1
// TS 24.229 clause 6.7.1.4
// RFC 3264 section 6
// RFC 3605 section 2.1
// RFC 4566 section 5.2
func (m *media) rewrite(s *sdp.Session, dialog *ua.Session, to int) ([]byte, error) {
	var addr netip.Addr

	for i, md := range s.Media {
		for _, name := range dropped {
			md.DelAttr(name)
		}

		md.Del('c')

		if m.stream == nil || i != m.stream.index || md.Port() == 0 {
			md.Disable()
			continue
		}

		l := mediaLeg(m.stream.relay, to)
		rtp, rtcp := l.AdvertisedRTP(), l.AdvertisedRTCP()

		d, _ := md.Desc()
		d.Port = rtp.Port()
		md.SetDesc(d)

		if rtcp.Port() != rtp.Port()+1 {
			md.AddAttr("rtcp", strconv.Itoa(int(rtcp.Port())))
		}

		addr = rtp.Addr()
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

	return m.stamp(s, dialog, to, addr)
}

// RFC 3264 section 8
// RFC 4566 section 5.2
// SIPconnect 2.0 section 14.1
func (m *media) stamp(s *sdp.Session, dialog *ua.Session, to int, addr netip.Addr) ([]byte, error) {
	o := &m.state(dialog).origins[to]
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

	if m.stream != nil {
		m.stream.relay.Answered()
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

		m.c.released()

		ctx, span := m.c.b.detached.Start(m.c.b.life, "media timeout", m.c.cause)
		defer tracing.End(span, nil)

		reason, _ := sip.NewReason("SIP", 408, "Media timeout")
		bc.End(ctx, ua.Reply{Header: ua.Reasons(reason).Header})
	})
}

func (m *media) close() {
	m.mu.Lock()
	st := m.stream
	m.stream, m.closed = nil, true
	m.mu.Unlock()

	if st != nil {
		_ = st.relay.Close()
	}
}

func newSessionID() uint32 {
	return rand.Uint32()
}
