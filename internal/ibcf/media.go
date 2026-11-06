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

func inFamily(addrs []netip.Addr, is4 bool) netip.Addr {
	for _, a := range addrs {
		if a.Unmap().Is4() == is4 {
			return a
		}
	}

	return netip.Addr{}
}

func perFamily(addrs []netip.Addr) []netip.Addr {
	var out []netip.Addr

	for _, a := range addrs {
		if !slices.ContainsFunc(out, func(b netip.Addr) bool { return b.Unmap().Is4() == a.Unmap().Is4() }) {
			out = append(out, a)
		}
	}

	return out
}

const (
	phoneSide = iota
	trunkSide
)

var (
	errNoMedia  = errors.New("ibcf: no TrGW address for the media")
	errNoStream = errors.New("ibcf: no audio stream the TrGW relays")
	errFamily   = errors.New("ibcf: no TrGW address in the address family of the media")
)

type origin struct {
	id      string
	version uint64
	sent    []byte
}

// RFC 3264 section 8
type dialogMedia struct {
	remote  [2]*trgw.Remote
	police  [2]uint64
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
	police uint64
	stream *stream
	mux    bool
	remove bool
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

// TS 29.162 clause 9.1.1.1
// TS 29.162 clause 9.1.3
// RFC 3264 section 8.2
// RFC 3264 section 8.3.1
func (m *media) Offer(x *b2bua.Exchange) ([]byte, error) {
	s, err := sdp.Parse(x.Offer)
	if err != nil {
		return nil, err
	}

	from, to := m.c.side(x.From), m.c.side(x.To)

	m.mu.Lock()
	defer m.mu.Unlock()

	if st := m.stream; st != nil && st.index < len(s.Media) && s.Media[st.index].Port() == 0 {
		st.refs++
		x.Value = &pending{dialog: x.Dialog, side: from, prev: m.state(x.Dialog).remote[from], stream: st, remove: true}

		return m.rewrite(s, x.Dialog, to, -1, false)
	}

	i := m.pick(s)
	if i < 0 {
		return nil, errNoStream
	}

	rm := remoteOf(s, i)
	added := m.stream != nil && rm.Known() && !m.bound(from, rm)

	st, err := m.open(i, from, to, rm)
	if err != nil {
		return nil, m.refuse(err)
	}

	st.refs++

	d := m.state(x.Dialog)
	p := &pending{
		dialog: x.Dialog, side: from, prev: d.remote[from], police: d.police[to], stream: st,
		mux: m.mux(x.Dialog, to) && !conflictsWithRTCP(s.Media[i]),
	}

	if err := m.set(x.Dialog, from, rm); err != nil {
		if added && m.stream == st {
			mediaLeg(st.relay, from).Drop(rm.RTP.Addr().Unmap().Is4())
		}

		m.release(st)

		return nil, m.refuse(err)
	}

	x.Value = p

	return m.rewrite(s, x.Dialog, to, i, p.mux)
}

func (m *media) bound(side int, rm trgw.Remote) bool {
	_, ok := mediaLeg(m.stream.relay, side).Bound(rm.RTP.Addr().Unmap().Is4())
	return ok
}

// RFC 5761 section 4
func conflictsWithRTCP(md *sdp.Media) bool {
	d, err := md.Desc()
	if err != nil {
		return true
	}

	return slices.ContainsFunc(d.Formats, func(f string) bool {
		pt, err := strconv.Atoi(f)
		return err == nil && pt >= 64 && pt <= 95
	})
}

// TS 29.162 clause 9.1.1.2
// RFC 5761 section 5.1.1
func (m *media) Answer(x *b2bua.Exchange, dialog *ua.Session, body []byte) ([]byte, error) {
	s, err := sdp.Parse(body)
	if err != nil {
		return nil, unusable(x, err)
	}

	from, to := m.c.side(x.To), m.c.side(x.From)
	p, _ := x.Value.(*pending)

	m.mu.Lock()
	defer m.mu.Unlock()

	if p != nil && p.remove {
		return m.rewrite(s, dialog, to, -1, false)
	}

	idx := -1

	if st := m.stream; st != nil && st.index < len(s.Media) && relayable(s.Media[st.index]) {
		rm := remoteOf(s, st.index)
		rm.Mux = rm.Mux && p != nil && p.mux

		if err := m.set(dialog, from, rm); err != nil {
			return nil, unusable(x, err)
		}

		idx = st.index
	}

	return m.rewrite(s, dialog, to, idx, m.mux(dialog, to))
}

// RFC 3261 section 21.5.1
// RFC 3261 section 21.5.3
// RFC 5057 section 5.1
func unusable(x *b2bua.Exchange, err error) error {
	code := 500
	if x.Initial {
		code = 502
	}

	return &b2bua.Rejection{Reply: ua.Reply{Code: code}, Err: err}
}

// RFC 3264 section 8
// TS 29.162 clause 9.1.3
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

		if p.remove && m.stream == p.stream {
			m.free()
			break
		}

		m.settle(dialog)
	case x.Initial && p.dialog == nil:
		m.base, m.dialogs, m.active, m.confirmed = &dialogMedia{}, map[*ua.Session]*dialogMedia{}, nil, false
	default:
		if d := m.known(p.dialog); d != nil && !p.remove {
			d.remote[p.side], d.police[1-p.side] = p.prev, p.police

			if p.dialog == m.active {
				m.revert(p.side, d)
			}

			if m.stream != nil && p.prev != nil && p.prev.Known() && (!m.confirmed || p.dialog == m.active) {
				mediaLeg(m.stream.relay, p.side).Keep(p.prev.RTP.Addr().Unmap().Is4())
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

		l := mediaLeg(m.stream.relay, side)
		_ = l.SetRemote(*rm)
		l.Commit()
		l.Police(d.police[side])
	}
}

// RFC 3264 section 8.3.1
func (m *media) revert(side int, d *dialogMedia) {
	if m.stream == nil {
		return
	}

	rm := d.remote[side]
	if rm == nil {
		rm = &trgw.Remote{}
	}

	_ = mediaLeg(m.stream.relay, side).Revert(*rm)
	mediaLeg(m.stream.relay, 1-side).Police(d.police[1-side])
}

// TS 23.334 clause 5.21
// RFC 3264 section 8.3.1
func (m *media) settle(dialog *ua.Session) {
	if m.stream == nil || m.confirmed && dialog != m.active {
		return
	}

	d := m.known(dialog)
	if d == nil {
		return
	}

	for side, rm := range d.remote {
		l := mediaLeg(m.stream.relay, side)
		l.Commit()

		if rm != nil && rm.Known() {
			l.Keep(rm.RTP.Addr().Unmap().Is4())
		}
	}
}

// TS 29.162 clause 9.1.3
func (m *media) free() {
	st := m.stream
	m.stream = nil

	reset := func(d *dialogMedia) {
		d.remote, d.police = [2]*trgw.Remote{}, [2]uint64{}
	}

	reset(m.base)

	for _, d := range m.dialogs {
		reset(d)
	}

	_ = st.relay.Close()
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

// SIPconnect 2.0 section 14.2
// SIPconnect 2.0 section 14.3
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

// SIPconnect 2.0 section 15
func (m *media) local(side int, rm trgw.Remote) (netip.Addr, error) {
	known := rm.Known()
	is4 := rm.RTP.Addr().Unmap().Is4()

	if side == phoneSide {
		if !known {
			if len(m.cfg.Phone) == 0 {
				return netip.Addr{}, errNoMedia
			}

			return m.cfg.Phone[0], nil
		}

		if a := inFamily(m.cfg.Phone, is4); a.IsValid() {
			return a, nil
		}

		return netip.Addr{}, errFamily
	}

	signal := m.c.trunkFamily().Unmap()

	switch {
	case signal.IsValid() && known && signal.Is4() != is4:
		return netip.Addr{}, errFamily
	case signal.IsValid() && slices.Contains(m.cfg.Trunk, signal):
		return signal, nil
	case signal.IsValid():
		is4 = signal.Is4()
	case !known:
		if len(m.cfg.Trunk) == 0 {
			return netip.Addr{}, errNoMedia
		}

		return m.cfg.Trunk[0], nil
	}

	if a := inFamily(m.cfg.Trunk, is4); a.IsValid() {
		return a, nil
	}

	return netip.Addr{}, errFamily
}

// TS 24.229 clause 6.7.1.4.1
// TS 29.162 clause 10.2.22
// SIPconnect 2.0 section 15
func (m *media) offered(side int) ([]netip.Addr, error) {
	if side == phoneSide {
		addrs := perFamily(m.cfg.Phone)
		if len(addrs) == 0 {
			return nil, errNoMedia
		}

		return addrs, nil
	}

	a, err := m.local(trunkSide, trgw.Remote{})
	if err != nil {
		return nil, err
	}

	return []netip.Addr{a}, nil
}

// TS 29.162 clause 9.1.1.1
// TS 29.162 clause 9.1.3
func (m *media) open(i, from, to int, rm trgw.Remote) (*stream, error) {
	if m.closed {
		return nil, trgw.ErrClosed
	}

	own, err := m.local(from, rm)
	if err != nil {
		return nil, err
	}

	if m.stream != nil {
		if rm.Known() {
			if _, err := mediaLeg(m.stream.relay, from).Open(own); err != nil {
				return nil, err
			}
		}

		return m.stream, nil
	}

	towards, err := m.offered(to)
	if err != nil {
		return nil, err
	}

	addrs := [2]netip.Addr{}
	addrs[from], addrs[to] = own, towards[0]

	t, _ := m.c.owner()

	r, err := m.cfg.Gateway.Allocate(context.Background(), trgw.Allocation{
		TrunkID: t.ID, PhoneAddress: addrs[phoneSide], TrunkAddress: addrs[trunkSide], OnMediaTimeout: m.timedOut,
	})
	if err != nil {
		return nil, err
	}

	for _, a := range towards[1:] {
		if _, err := mediaLeg(r, to).Open(a); err != nil {
			_ = r.Close()
			return nil, err
		}
	}

	if m.answered {
		r.Answered()
	}

	m.stream = &stream{index: i, relay: r}

	return m.stream, nil
}

// RFC 3261 section 20.43
// RFC 3261 section 21.4.26
// RFC 3261 section 21.5.4
func (m *media) refuse(err error) error {
	switch {
	case errors.Is(err, trgw.ErrNoPorts):
		return &b2bua.Rejection{Reply: ua.Reply{Code: 503}, Err: err}
	case errors.Is(err, errFamily), errors.Is(err, trgw.ErrFamily):
		rp := ua.Reply{Code: 488}

		if w, werr := sip.NewWarning(sip.WarnIncompatibleAddressFormats, m.c.b.cfg.HomeDomain); werr == nil {
			rp.Header.Add("Warning", w.String())
		}

		return &b2bua.Rejection{Reply: rp, Err: err}
	}

	return err
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
// RFC 5761 section 5.1.1
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

	rm.Mux = s.Media[i].RTCPMux()

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

// GSMA IR.65 section 5.2.5.6
// RFC 3312 section 11
// RFC 5761 section 5.1.1
// RFC 6947 section 4.2.2
// RFC 8858 section 4.3
var dropped = []string{
	"rtcp", "rtcp-mux", "rtcp-mux-only", "candidate", "remote-candidates", "end-of-candidates", "ice-ufrag", "ice-pwd",
	"ice-options", "ice-lite", "ice-mismatch", sdp.Current, sdp.Desired, sdp.Confirmed, "altc",
}

func (m *media) mux(dialog *ua.Session, side int) bool {
	rm := m.state(dialog).remote[side]
	return rm != nil && rm.Mux
}

// RFC 6947 section 4.1
func (m *media) advertised(dialog *ua.Session, to int) (trgw.Binding, *trgw.Binding) {
	l := mediaLeg(m.stream.relay, to)

	if rm := m.state(dialog).remote[to]; rm != nil && rm.Known() {
		if b, ok := l.Bound(rm.RTP.Addr().Unmap().Is4()); ok {
			return b, nil
		}
	}

	b, _ := l.Primary()

	if alt, ok := l.Bound(!b.AdvertisedRTP.Addr().Is4()); ok && to == phoneSide {
		return b, &alt
	}

	return b, nil
}

// RFC 6947 section 4.1
func altc(n int, b trgw.Binding) string {
	family := sdp.IP4
	if !b.AdvertisedRTP.Addr().Is4() {
		family = sdp.IP6
	}

	return strconv.Itoa(n) + " " + family + " " + b.AdvertisedRTP.Addr().String() + " " + strconv.Itoa(int(b.AdvertisedRTP.Port()))
}

// TS 24.229 clause 6.7.1.2.1
// TS 24.229 clause 6.7.1.4.1
// TS 29.162 clause 9.1.5
// RFC 3264 section 5
// RFC 3264 section 6
// RFC 3264 section 8.2
// RFC 3605 section 2.1
// RFC 4566 section 5.7
// RFC 4566 section 5.14
func (m *media) rewrite(s *sdp.Session, dialog *ua.Session, to, idx int, mux bool) ([]byte, error) {
	var (
		addr netip.Addr
		src  netip.Addr
	)

	if idx >= 0 {
		if e, err := s.RTPEndpoint(idx); err == nil {
			src = e.Addr.Unmap()
		}
	}

	for i, md := range s.Media {
		for _, name := range dropped {
			md.DelAttr(name)
		}

		md.Del('c')

		if m.stream == nil || i != idx || md.Port() == 0 {
			md.Disable()
			continue
		}

		b, alt := m.advertised(dialog, to)
		rtp, rtcp := b.AdvertisedRTP, b.AdvertisedRTCP

		d, _ := md.Desc()
		d.Port, d.NumPorts = rtp.Port(), 0
		md.SetDesc(d)

		if rtcp.Port() != rtp.Port()+1 {
			md.AddAttr("rtcp", strconv.Itoa(int(rtcp.Port())))
		}

		if mux {
			md.AddAttr("rtcp-mux", "")
		}

		if alt != nil {
			md.AddAttr("altc", altc(1, b))
			md.AddAttr("altc", altc(2, *alt))
		}

		addr = rtp.Addr()

		if src.IsValid() && src.Is4() != addr.Is4() {
			interwork(s, md, src.Is4(), addr.Is4())
		}

		m.police(dialog, to, md)
	}

	for _, name := range dropped {
		s.DelAttr(name)
	}

	if !addr.IsValid() {
		addr = m.fallback(to)
	}

	if !addr.IsValid() {
		return nil, errNoMedia
	}

	s.SetConnection(sdp.NewConnection(addr))

	return m.stamp(s, dialog, to, addr)
}

func (m *media) fallback(side int) netip.Addr {
	if addrs, err := m.offered(side); err == nil {
		if a, ok := m.cfg.Gateway.Advertised(addrs[0]); ok {
			return a
		}

		return addrs[0]
	}

	return netip.Addr{}
}

// TS 29.162 clause 10.2.9
func (m *media) police(dialog *ua.Session, side int, md *sdp.Media) {
	rate := policed(md)
	d := m.state(dialog)
	d.police[side] = rate

	if !m.confirmed || dialog == m.active {
		mediaLeg(m.stream.relay, side).Police(rate)
	}
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
