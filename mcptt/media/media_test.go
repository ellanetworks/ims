package media_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/floorctl"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/mcptt/media/mediatest"
	"github.com/ellanetworks/ims/mcptt/rtp"
	"github.com/ellanetworks/ims/sip/siptest"
)

var server = netip.MustParseAddr("10.1.0.1")

type env struct {
	t   *testing.T
	n   *siptest.Net
	net *mediatest.Net
	r   *media.Relay
}

func newEnv(t *testing.T, opts ...func(*media.Config)) *env {
	t.Helper()

	n := siptest.NewNet(t)
	mn := mediatest.New(n.Scheduler())

	cfg := media.Config{Network: mn, Clock: n.Clock(), Addr: server, MinPort: 20000, MaxPort: 20099, ControlPort: 19999}
	for _, o := range opts {
		o(&cfg)
	}

	r, err := media.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = r.Close() })

	return &env{t: t, n: n, net: mn, r: r}
}

// client is a participant's endpoint: its audio, RTCP and control sockets, and what its leg reported.
type client struct {
	audio, rtcp, control *mediatest.Peer
	leg                  *media.Leg
	media                atomic.Int32

	mu    sync.Mutex
	floor []floorctl.Message
}

func (e *env) client(c *media.Call, i int, cfg media.LegConfig) *client {
	e.t.Helper()

	cl := &client{}
	host := netip.AddrFrom4([4]byte{10, 2, 0, byte(i)})

	var err error

	for _, p := range []struct {
		peer **mediatest.Peer
		port uint16
	}{{&cl.audio, 4000}, {&cl.rtcp, 4001}, {&cl.control, 4002}} {
		if *p.peer, err = e.net.Peer(netip.AddrPortFrom(host, p.port)); err != nil {
			e.t.Fatal(err)
		}
	}

	cfg.OnMedia = func() { cl.media.Add(1) }
	cfg.OnFloor = func(m floorctl.Message) {
		cl.mu.Lock()
		defer cl.mu.Unlock()

		cl.floor = append(cl.floor, m)
	}

	if cl.leg, err = c.AddLeg(cfg); err != nil {
		e.t.Fatal(err)
	}

	cl.leg.SetRemote(media.Remote{Audio: cl.audio.LocalAddr(), Control: cl.control.LocalAddr()})

	return cl
}

func packet(t testing.TB, ssrc uint32, seq uint16, payload ...byte) []byte {
	t.Helper()

	p := rtp.Packet{PayloadType: 96, Sequence: seq, SSRC: ssrc, Payload: payload}

	b, err := p.Append(nil)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// TS 24.380 §6.3.4.4.5 step 4: the talker's media reaches every other leg and not the talker, in the Audio SSRC
// assigned the talker's leg (§6.2.4.4.2, §8.2.3.16) whichever it sent, in each receiver's payload type (RFC 3264
// §5.1), the rest unchanged; the others' is dropped and reported; the talker's of another payload type dropped, not
// reported; media is reported at most once per report interval.
func TestDistribution(t *testing.T) {
	e := newEnv(t)
	c := e.r.NewCall()
	a := e.client(c, 1, media.LegConfig{PayloadType: 96})
	b := e.client(c, 2, media.LegConfig{PayloadType: 96})
	d := e.client(c, 3, media.LegConfig{PayloadType: 118})
	as, bs := a.leg.AudioSSRC(), b.leg.AudioSSRC()

	if as == bs {
		t.Fatal("Audio SSRCs not unique")
	}

	c.Forward(a.leg, true)

	// A talker of an SSRC of its own (before Rel-18), the marker set.
	sent := rtp.Packet{Marker: true, PayloadType: 96, Sequence: 1, Timestamp: 320, SSRC: 0xabcd, Payload: []byte{1, 2, 3}}
	p, _ := sent.Append(nil)
	_ = a.audio.WriteTo(p, a.leg.Audio())

	e.n.Quiesce()

	for name, cl := range map[string]*client{"b": b, "d": d} {
		got := cl.audio.Take()
		if len(got) != 1 || got[0].From != cl.leg.Audio() {
			t.Fatalf("%s received %+v", name, got)
		}

		want := sent
		want.SSRC, want.PayloadType = as, map[string]uint8{"b": 96, "d": 118}[name]

		if r, err := rtp.Parse(got[0].B); err != nil || !reflect.DeepEqual(r, want) {
			t.Fatalf("%s received %+v, %v, want %+v", name, r, err, want)
		}
	}

	if got := a.audio.Take(); len(got) != 0 {
		t.Fatalf("the talker received %d", len(got))
	}

	e.n.Advance(200 * time.Millisecond)

	// b without permission: dropped, reported; a of a payload type it did not negotiate: dropped, not reported.
	_ = b.audio.WriteTo(packet(t, bs, 1, 9), b.leg.Audio())

	other := rtp.Packet{PayloadType: 101, Sequence: 2, SSRC: as, Payload: []byte{9}}
	op, _ := other.Append(nil)
	_ = a.audio.WriteTo(op, a.leg.Audio())

	e.n.Quiesce()

	if len(a.audio.Take())+len(d.audio.Take())+len(b.audio.Take()) != 0 || b.media.Load() != 1 || a.media.Load() != 1 {
		t.Fatalf("dropped media forwarded, or reported wrong: b %d, a %d", b.media.Load(), a.media.Load())
	}

	// Reports at most once per interval; packets without payload not reported.
	for i := range 5 {
		_ = a.audio.WriteTo(packet(t, as, uint16(3+i), 1), a.leg.Audio())
	}

	_ = a.audio.WriteTo(packet(t, as, 8), a.leg.Audio())

	e.n.Quiesce()

	if a.media.Load() != 2 {
		t.Fatalf("reported %d", a.media.Load())
	}

	e.n.Advance(200 * time.Millisecond)

	_ = a.audio.WriteTo(packet(t, as, 9, 1), a.leg.Audio())

	e.n.Quiesce()

	if a.media.Load() != 3 || len(b.audio.Take()) != 7 {
		t.Errorf("reported %d", a.media.Load())
	}

	c.Forward(a.leg, false)
	_ = a.audio.WriteTo(packet(t, as, 10, 1), a.leg.Audio())

	e.n.Quiesce()

	if len(b.audio.Take()) != 0 {
		t.Error("forwarded after the floor")
	}

	st := e.r.Stats()
	if st.Packets.Load() != 11 || st.Forwarded.Load() != 16 || st.Dropped.Load() != 3 {
		t.Errorf("stats %d packets, %d forwarded, %d dropped", st.Packets.Load(), st.Forwarded.Load(), st.Dropped.Load())
	}
}

// RFC 3264 §5.1: the SDP's address tells nothing of the source of the media: the talker's media from another
// address forwarded; malformed media dropped.
func TestSource(t *testing.T) {
	e := newEnv(t)
	c := e.r.NewCall()
	a, b := e.client(c, 1, media.LegConfig{PayloadType: 96}), e.client(c, 2, media.LegConfig{PayloadType: 96})
	c.Forward(a.leg, true)

	_ = b.rtcp.WriteTo(packet(t, a.leg.AudioSSRC(), 1, 1), a.leg.Audio())
	_ = a.audio.WriteTo([]byte{0x40, 0, 0}, a.leg.Audio())

	e.n.Quiesce()

	if len(b.audio.Take()) != 1 || e.r.Stats().Dropped.Load() != 1 {
		t.Errorf("dropped %d", e.r.Stats().Dropped.Load())
	}
}

// RFC 3550 §7.2, RFC 5761: the relay, a translator, forwards the RTCP of each leg to the others unchanged, on their
// RTCP port or multiplexed.
func TestRTCP(t *testing.T) {
	e := newEnv(t)
	c := e.r.NewCall()
	a := e.client(c, 1, media.LegConfig{})
	b := e.client(c, 2, media.LegConfig{Mux: true})
	d := e.client(c, 3, media.LegConfig{})
	c.Forward(a.leg, true)

	sr, err := rtp.AppendRTCP(nil, &rtp.SenderReport{SSRC: a.leg.AudioSSRC()})
	if err != nil {
		t.Fatal(err)
	}

	_ = a.rtcp.WriteTo(sr, a.leg.RTCP())

	e.n.Quiesce()

	if got := b.audio.Take(); len(got) != 1 || string(got[0].B) != string(sr) {
		t.Errorf("multiplexed receiver: %+v", got)
	}

	if got := d.rtcp.Take(); len(got) != 1 || got[0].From != d.leg.RTCP() {
		t.Errorf("receiver: %+v", got)
	}

	rr, _ := rtp.AppendRTCP(nil, &rtp.ReceiverReport{SSRC: b.leg.AudioSSRC()})
	_ = b.audio.WriteTo(rr, b.leg.Audio())

	e.n.Quiesce()

	if len(a.rtcp.Take()) != 1 || len(d.rtcp.Take()) != 1 || len(b.audio.Take()) != 0 {
		t.Error("receiver report not forwarded to the others")
	}

	_ = d.rtcp.WriteTo([]byte{0x80, 201, 0, 9}, d.leg.RTCP())

	e.n.Quiesce()

	if len(a.rtcp.Take())+len(b.audio.Take()) != 0 {
		t.Error("malformed RTCP forwarded")
	}
}

// TS 24.380 §4.3.3.1, §8.1, §14.2.7: floor control on the shared port, by the SSRC the server assigned, or on a port of
// the leg; one APP packet a message, more in a datagram; messages of an unknown subtype ignored (§8.1.4); §14.4: a peer
// offered mc_floor_ssrc that answers without it found by its address.
func TestFloorControl(t *testing.T) {
	e := newEnv(t)
	c := e.r.NewCall()
	shared := e.client(c, 1, media.LegConfig{SharedControl: true})
	own := e.client(c, 2, media.LegConfig{})
	single := e.client(c, 3, media.LegConfig{SharedControl: true})
	single.leg.SetMultiplexed(false)

	ssrc, ok := shared.leg.FloorSSRC()
	if !ok || shared.leg.Control().Port() != 19999 || own.leg.Control().Port() == 19999 {
		t.Fatalf("control %s, %s", shared.leg.Control(), own.leg.Control())
	}

	if _, ok := own.leg.FloorSSRC(); ok {
		t.Fatal("an SSRC for a leg on its own port")
	}

	request := func(ssrc uint32) []byte {
		app, err := (&floorctl.Message{Type: floorctl.Request, SSRC: ssrc}).App()
		if err != nil {
			t.Fatal(err)
		}

		unknown := &rtp.App{Subtype: 0b01011, SSRC: ssrc, Name: floorctl.Name}

		b, err := rtp.AppendRTCP(nil, app, unknown)
		if err != nil {
			t.Fatal(err)
		}

		return b
	}

	_ = shared.control.WriteTo(request(ssrc), shared.leg.Control())
	_ = shared.control.WriteTo(request(ssrc+1), shared.leg.Control())
	_ = own.control.WriteTo(request(77), own.leg.Control())
	_ = single.control.WriteTo(request(78), single.leg.Control())

	e.n.Quiesce()

	for name, cl := range map[string]*client{"shared": shared, "own": own, "single": single} {
		if len(cl.floor) != 1 || cl.floor[0].Type != floorctl.Request {
			t.Errorf("%s: %+v", name, cl.floor)
		}
	}

	if err := own.leg.SendFloor(floorctl.Message{Type: floorctl.Idle, SSRC: 5, SequenceNumber: new(uint16)}); err != nil {
		t.Fatal(err)
	}

	e.n.Quiesce()

	got := own.control.Take()
	if len(got) != 1 || got[0].From != own.leg.Control() {
		t.Fatalf("%+v", got)
	}

	ps, err := rtp.ParseRTCP(got[0].B)
	if err != nil {
		t.Fatal(err)
	}

	if m, err := floorctl.Decode(ps[0].(*rtp.App)); err != nil || m.Type != floorctl.Idle || m.SSRC != 5 {
		t.Errorf("%+v, %v", m, err)
	}
}

// Floor control on the shared port of no leg open, of an SSRC and a source the relay does not know or of a leg
// released, is dropped: counted, and logged at debug.
func TestFloorDropped(t *testing.T) {
	var logs bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := newEnv(t, func(c *media.Config) { c.Logger = logger })
	c := e.r.NewCall()
	cl := e.client(c, 1, media.LegConfig{SharedControl: true})
	ssrc, _ := cl.leg.FloorSSRC()
	to := cl.leg.Control()

	request := func(ssrc uint32) []byte {
		app, err := (&floorctl.Message{Type: floorctl.Request, SSRC: ssrc}).App()
		if err != nil {
			t.Fatal(err)
		}

		b, err := rtp.AppendRTCP(nil, app)
		if err != nil {
			t.Fatal(err)
		}

		return b
	}

	_ = cl.control.WriteTo(request(ssrc+1), to)

	e.n.Quiesce()

	cl.leg.Close()
	_ = cl.control.WriteTo(request(ssrc), to)

	e.n.Quiesce()

	if len(cl.floor) != 0 || e.r.Stats().Dropped.Load() != 2 {
		t.Errorf("%d delivered, %d dropped", len(cl.floor), e.r.Stats().Dropped.Load())
	}

	if n := strings.Count(logs.String(), "floor control message dropped"); n != 2 {
		t.Errorf("%d logged: %s", n, logs.String())
	}
}

// The ports of a leg: audio even, RTCP the next unless multiplexed; a control port any; the search on from the last
// ports taken; the range exhausted; the advertised address.
func TestPorts(t *testing.T) {
	n := siptest.NewNet(t)

	r, err := media.New(media.Config{
		Network: mediatest.New(n.Scheduler()), Addr: netip.IPv4Unspecified(), Advertise: server,
		MinPort: 30000, MaxPort: 30005, ControlPort: 29999,
	})
	if err != nil {
		t.Fatal(err)
	}

	c := r.NewCall()

	a, err := c.AddLeg(media.LegConfig{Mux: true})
	if err != nil || a.Audio() != netip.AddrPortFrom(server, 30000) || a.Control().Port() != 30001 || a.RTCP() != a.Audio() {
		t.Fatalf("%v, %v", a, err)
	}

	b, err := c.AddLeg(media.LegConfig{SharedControl: true})
	if err != nil || b.Audio().Port() != 30002 || b.RTCP().Port() != 30003 || b.Control() != netip.AddrPortFrom(server, 29999) {
		t.Fatalf("%v, %v", b, err)
	}

	if _, err := c.AddLeg(media.LegConfig{}); !errors.Is(err, media.ErrNoPort) {
		t.Fatalf("%v", err)
	}

	a.Close()

	if d, err := c.AddLeg(media.LegConfig{SharedControl: true}); err != nil || d.Audio().Port() != 30000 {
		t.Errorf("%v, %v", d, err)
	}

	c.Close()

	if _, err := c.AddLeg(media.LegConfig{SharedControl: true}); !errors.Is(err, media.ErrClosed) {
		t.Errorf("%v", err)
	}

	if _, err := media.New(media.Config{Network: mediatest.New(n.Scheduler()), MinPort: 1, MaxPort: 9}); err == nil {
		t.Error("odd first port")
	}
}

// A leg added after the relay closed gets ErrClosed, its ports free.
func TestAddLegClosed(t *testing.T) {
	n := siptest.NewNet(t)
	m := mediatest.New(n.Scheduler())

	r, err := media.New(media.Config{Network: m, Addr: server, MinPort: 30000, MaxPort: 30005, ControlPort: 29999})
	if err != nil {
		t.Fatal(err)
	}

	c := r.NewCall()

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	if l, err := c.AddLeg(media.LegConfig{}); !errors.Is(err, media.ErrClosed) {
		t.Fatalf("%v, %v", l, err)
	}

	for p := uint16(30000); p <= 30005; p++ {
		conn, err := m.Listen(netip.AddrPortFrom(server, p), func([]byte, netip.AddrPort) {})
		if err != nil {
			t.Fatalf("port %d: %v", p, err)
		}

		_ = conn.Close()
	}
}

// A port taken outside the relay is skipped, and the ports reserved with it are free again.
func TestPortsTakenOutside(t *testing.T) {
	n := siptest.NewNet(t)
	m := mediatest.New(n.Scheduler())

	r, err := media.New(media.Config{Network: m, Addr: server, MinPort: 30000, MaxPort: 30003, ControlPort: 29999})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = r.Close() })

	outside, err := m.Listen(netip.AddrPortFrom(server, 30001), func([]byte, netip.AddrPort) {})
	if err != nil {
		t.Fatal(err)
	}

	c := r.NewCall()

	a, err := c.AddLeg(media.LegConfig{SharedControl: true})
	if err != nil || a.Audio().Port() != 30002 || a.RTCP().Port() != 30003 {
		t.Fatalf("%v, %v", a, err)
	}

	_ = outside.Close()

	if b, err := c.AddLeg(media.LegConfig{SharedControl: true}); err != nil || b.Audio().Port() != 30000 || b.RTCP().Port() != 30001 {
		t.Errorf("%v, %v", b, err)
	}
}

// The first leg's ports in the range of a lower bound that is not a multiple of its span.
func TestPortRange(t *testing.T) {
	for _, rg := range [][2]uint16{{20000, 20100}, {32768, 60999}, {45002, 45999}, {20000, 20999}} {
		n := siptest.NewNet(t)

		r, err := media.New(media.Config{
			Network: mediatest.New(n.Scheduler()), Addr: server, MinPort: rg[0], MaxPort: rg[1], ControlPort: 19999,
		})
		if err != nil {
			t.Fatal(err)
		}

		c := r.NewCall()

		for range 3 {
			l, err := c.AddLeg(media.LegConfig{})
			if err != nil {
				t.Fatal(err)
			}

			for _, p := range []uint16{l.Audio().Port(), l.RTCP().Port(), l.Control().Port()} {
				if p < rg[0] || p > rg[1] {
					t.Errorf("range %d-%d: port %d", rg[0], rg[1], p)
				}
			}
		}

		_ = r.Close()
	}
}

// RFC 3264 §5.1, §6.1: media not sent to a peer that does not receive, nor taken from one that does not send, nor
// reported then; RTCP whatever the direction.
func TestDirection(t *testing.T) {
	e := newEnv(t)
	c := e.r.NewCall()
	a := e.client(c, 1, media.LegConfig{PayloadType: 96})
	b := e.client(c, 2, media.LegConfig{PayloadType: 96})
	d := e.client(c, 3, media.LegConfig{PayloadType: 96})
	direct := func(cl *client, dir media.Direction) {
		cl.leg.SetRemote(media.Remote{Audio: cl.audio.LocalAddr(), Control: cl.control.LocalAddr(), Direction: dir})
	}

	direct(b, media.RecvOnly)
	direct(d, media.Inactive)
	c.Forward(a.leg, true)

	_ = a.audio.WriteTo(packet(t, a.leg.AudioSSRC(), 1, 1), a.leg.Audio())

	e.n.Quiesce()

	if len(b.audio.Take())+len(d.audio.Take()) != 0 {
		t.Fatal("media sent to a peer that does not receive")
	}

	direct(b, media.SendRecv)
	direct(a, media.SendOnly)
	e.n.Advance(time.Second)

	reported := a.media.Load()
	_ = a.audio.WriteTo(packet(t, a.leg.AudioSSRC(), 2, 1), a.leg.Audio())

	sr, _ := rtp.AppendRTCP(nil, &rtp.SenderReport{SSRC: a.leg.AudioSSRC()})
	_ = a.rtcp.WriteTo(sr, a.leg.RTCP())

	e.n.Quiesce()

	if len(b.audio.Take()) != 0 || a.media.Load() != reported {
		t.Fatal("media taken from a peer that does not send")
	}

	if len(b.rtcp.Take()) != 1 || len(d.rtcp.Take()) != 1 {
		t.Error("RTCP not forwarded")
	}
}

// RFC 3550 §7.2: the RTCP of a talker of an SSRC of its own (before Rel-18) translated as its RTP is: its sender
// report and SDES to the others in the assigned SSRC; the others' reports on it, in the assigned SSRC, to it in its
// own, to the others unchanged.
func TestRTCPTranslation(t *testing.T) {
	e := newEnv(t)
	c := e.r.NewCall()
	a := e.client(c, 1, media.LegConfig{PayloadType: 96})
	b := e.client(c, 2, media.LegConfig{PayloadType: 96})
	d := e.client(c, 3, media.LegConfig{PayloadType: 96})
	as := a.leg.AudioSSRC()
	c.Forward(a.leg, true)

	_ = a.audio.WriteTo(packet(t, 0xabcd, 1, 1), a.leg.Audio())

	e.n.Quiesce()
	b.audio.Take()
	d.audio.Take()

	cname := []rtp.SDESItem{{Type: rtp.SDESCNAME, Text: "a@x"}}
	sr, _ := rtp.AppendRTCP(nil, &rtp.SenderReport{SSRC: 0xabcd, PacketCount: 1},
		&rtp.SourceDescription{Chunks: []rtp.SDESChunk{{Source: 0xabcd, Items: cname}}})
	_ = a.rtcp.WriteTo(sr, a.leg.RTCP())

	e.n.Quiesce()

	want, _ := rtp.AppendRTCP(nil, &rtp.SenderReport{SSRC: as, PacketCount: 1},
		&rtp.SourceDescription{Chunks: []rtp.SDESChunk{{Source: as, Items: cname}}})

	for name, cl := range map[string]*client{"b": b, "d": d} {
		if got := cl.rtcp.Take(); len(got) != 1 || string(got[0].B) != string(want) {
			t.Errorf("%s received %+v", name, got)
		}
	}

	rr := func(ssrc uint32) []byte {
		p, _ := rtp.AppendRTCP(nil, &rtp.ReceiverReport{SSRC: b.leg.AudioSSRC(), Reports: []rtp.ReceptionReport{{SSRC: ssrc, Jitter: 3}}})
		return p
	}

	_ = b.rtcp.WriteTo(rr(as), b.leg.RTCP())

	e.n.Quiesce()

	if got := a.rtcp.Take(); len(got) != 1 || string(got[0].B) != string(rr(0xabcd)) {
		t.Errorf("a received %+v", got)
	}

	if got := d.rtcp.Take(); len(got) != 1 || string(got[0].B) != string(rr(as)) {
		t.Errorf("d received %+v", got)
	}
}
