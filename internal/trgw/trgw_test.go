package trgw

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/rtp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const testTrunk = "telnyx"

var loopback = netip.MustParseAddr("127.0.0.1")

func newGateway(t testing.TB, pairs int, opts ...Option) (*Gateway, *Metrics) {
	t.Helper()

	m := NewMetrics()
	lo := uint16(10000 + 2*rand.IntN(4000))
	opts = append([]Option{WithAddresses(Address{Bind: loopback}), WithPortRange(lo, lo+uint16(2*pairs-1)), WithMetrics(m)}, opts...)

	g, err := New(opts...)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = g.Close() })

	return g, m
}

func allocate(t testing.TB, g *Gateway) *Relay {
	t.Helper()

	r, err := g.Allocate(context.Background(), Allocation{TrunkID: testTrunk})
	if err != nil {
		t.Fatal(err)
	}

	return r
}

type endpoint struct {
	rtp, rtcp *net.UDPConn
}

func newEndpoint(t testing.TB, addr netip.Addr) *endpoint {
	t.Helper()

	e := &endpoint{rtp: listen(t, addr), rtcp: listen(t, addr)}

	return e
}

func listen(t testing.TB, addr netip.Addr) *net.UDPConn {
	t.Helper()

	c, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(addr, 0)))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return c
}

func (e *endpoint) remote(d Direction) Remote {
	return Remote{RTP: addrOf(e.rtp), RTCP: addrOf(e.rtcp), Direction: d}
}

func addrOf(c *net.UDPConn) netip.AddrPort {
	a := c.LocalAddr().(*net.UDPAddr).AddrPort()
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
}

func setRemote(t testing.TB, l *Leg, rm Remote) {
	t.Helper()

	if err := l.SetRemote(rm); err != nil {
		t.Fatal(err)
	}
}

func rtpPacket(t testing.TB, pt uint8, seq uint16, payload []byte) []byte {
	t.Helper()

	p := rtp.Packet{PayloadType: pt, Sequence: seq, Timestamp: uint32(seq) * 160, SSRC: 0x11223344, Payload: payload}

	b, err := p.Append(nil)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func rtcpPacket(t testing.TB) []byte {
	t.Helper()

	b, err := rtp.AppendRTCP(nil, &rtp.ReceiverReport{SSRC: 0x55667788})
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func send(t testing.TB, c *net.UDPConn, b []byte, to netip.AddrPort) {
	t.Helper()

	if _, err := c.WriteToUDPAddrPort(b, to); err != nil {
		t.Fatal(err)
	}
}

func receive(t testing.TB, c *net.UDPConn) ([]byte, netip.AddrPort) {
	t.Helper()

	buf := make([]byte, bufferSize)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))

	n, from, err := c.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatalf("receive on %s: %v", addrOf(c), err)
	}

	return buf[:n], netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
}

func nothing(t testing.TB, c *net.UDPConn) {
	t.Helper()

	buf := make([]byte, bufferSize)
	_ = c.SetReadDeadline(time.Now().Add(150 * time.Millisecond))

	if n, from, err := c.ReadFromUDPAddrPort(buf); err == nil {
		t.Fatalf("%s received %d octets from %s", addrOf(c), n, from)
	}
}

func relayed(t testing.TB, from *endpoint, to *endpoint, src, dst *Leg, b []byte) {
	t.Helper()

	send(t, from.rtp, b, src.LocalRTP())

	got, sender := receive(t, to.rtp)
	if !bytes.Equal(got, b) {
		t.Fatalf("relayed %x, want %x", got, b)
	}

	if sender != dst.LocalRTP() {
		t.Fatalf("relayed from %s, want %s", sender, dst.LocalRTP())
	}
}

func waitMetric(t testing.TB, get func() float64, want float64) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for {
		got := get()
		if got == want {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("metric is %v, want %v", got, want)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func dropped(m *Metrics, leg string, r dropReason) func() float64 {
	return func() float64 {
		return testutil.ToFloat64(m.dropped.WithLabelValues(testTrunk, leg, dropReasonNames[r]))
	}
}

func TestTwoWayRelay(t *testing.T) {
	g, m := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk := newEndpoint(t, loopback), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	for seq := range uint16(3) {
		relayed(t, phone, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 104, seq, []byte{0xf0, byte(seq)}))
		relayed(t, trunk, phone, r.Trunk(), r.Phone(), rtpPacket(t, 104, 100+seq, []byte{0xf1, byte(seq)}))
	}

	relayed(t, phone, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 105, 3, []byte{0x01, 0x8a, 0x03, 0x20}))

	waitMetric(t, func() float64 { return testutil.ToFloat64(m.relayed.WithLabelValues(testTrunk, "phone", "rtp")) }, 4)
	waitMetric(t, func() float64 { return testutil.ToFloat64(m.relayed.WithLabelValues(testTrunk, "trunk", "rtp")) }, 3)
	waitMetric(t, func() float64 { return testutil.ToFloat64(m.relays.WithLabelValues(testTrunk)) }, 1)
}

func TestRTCPRelay(t *testing.T) {
	g, m := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk := newEndpoint(t, loopback), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	if r.Phone().LocalRTCP().Port() != r.Phone().LocalRTP().Port()+1 || r.Phone().LocalRTP().Port()%2 != 0 {
		t.Fatalf("local ports %s %s", r.Phone().LocalRTP(), r.Phone().LocalRTCP())
	}

	rr := rtcpPacket(t)

	for _, c := range []struct {
		from, to *endpoint
		src, dst *Leg
	}{{phone, trunk, r.Phone(), r.Trunk()}, {trunk, phone, r.Trunk(), r.Phone()}} {
		send(t, c.from.rtcp, rr, c.src.LocalRTCP())

		got, sender := receive(t, c.to.rtcp)
		if !bytes.Equal(got, rr) {
			t.Fatalf("relayed %x, want %x", got, rr)
		}

		if sender != c.dst.LocalRTCP() {
			t.Fatalf("relayed from %s, want %s", sender, c.dst.LocalRTCP())
		}
	}

	send(t, phone.rtcp, []byte{0x80, 0x68, 0, 1, 0, 0, 0, 0}, r.Phone().LocalRTCP())
	nothing(t, trunk.rtcp)
	waitMetric(t, dropped(m, "phone", dropMalformed), 1)
}

func TestDefaultRTCPPort(t *testing.T) {
	g, _ := newGateway(t, 8)
	r := allocate(t, g)

	setRemote(t, r.Phone(), Remote{RTP: netip.MustParseAddrPort("127.0.0.1:49120")})

	if got, want := r.Phone().Remote().RTCP, netip.MustParseAddrPort("127.0.0.1:49121"); got != want {
		t.Fatalf("RTCP remote %s, want %s", got, want)
	}

	for _, rm := range []Remote{
		{RTP: netip.MustParseAddrPort("127.0.0.1:65535")},
		{RTP: netip.MustParseAddrPort("127.0.0.1:4000"), RTCP: netip.MustParseAddrPort("127.0.0.1:4000")},
		{RTP: netip.MustParseAddrPort("[::1]:4000")},
		{RTCP: netip.MustParseAddrPort("127.0.0.1:4001")},
		{RTP: netip.MustParseAddrPort("127.0.0.1:4000"), Direction: Inactive + 1},
	} {
		if err := r.Phone().SetRemote(rm); err == nil {
			t.Errorf("SetRemote(%+v) succeeded", rm)
		}
	}
}

func TestUnexpectedSourceDropped(t *testing.T) {
	g, m := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk, stranger := newEndpoint(t, loopback), newEndpoint(t, loopback), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	send(t, stranger.rtp, rtpPacket(t, 104, 1, []byte{1}), r.Phone().LocalRTP())
	send(t, stranger.rtcp, rtcpPacket(t), r.Trunk().LocalRTCP())
	send(t, phone.rtcp, rtpPacket(t, 104, 1, []byte{1}), r.Phone().LocalRTP())

	nothing(t, trunk.rtp)
	nothing(t, phone.rtcp)
	waitMetric(t, dropped(m, "phone", dropUnexpectedSource), 2)
	waitMetric(t, dropped(m, "trunk", dropUnexpectedSource), 1)

	relayed(t, phone, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 104, 2, []byte{2}))
}

func TestRemoteChange(t *testing.T) {
	g, m := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk, moved := newEndpoint(t, loopback), newEndpoint(t, loopback), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))
	relayed(t, trunk, phone, r.Trunk(), r.Phone(), rtpPacket(t, 104, 1, []byte{1}))

	setRemote(t, r.Trunk(), moved.remote(SendRecv))

	send(t, trunk.rtp, rtpPacket(t, 104, 2, []byte{2}), r.Trunk().LocalRTP())
	nothing(t, phone.rtp)
	waitMetric(t, dropped(m, "trunk", dropUnexpectedSource), 1)

	relayed(t, moved, phone, r.Trunk(), r.Phone(), rtpPacket(t, 104, 3, []byte{3}))
	relayed(t, phone, moved, r.Phone(), r.Trunk(), rtpPacket(t, 104, 4, []byte{4}))
	nothing(t, trunk.rtp)
}

func TestDirection(t *testing.T) {
	g, m := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk := newEndpoint(t, loopback), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), phone.remote(SendOnly))
	setRemote(t, r.Trunk(), trunk.remote(RecvOnly))

	relayed(t, phone, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 104, 1, []byte{1}))
	send(t, trunk.rtp, rtpPacket(t, 104, 1, []byte{1}), r.Trunk().LocalRTP())
	nothing(t, phone.rtp)
	waitMetric(t, dropped(m, "trunk", dropDirection), 1)

	setRemote(t, r.Phone(), phone.remote(Inactive))
	setRemote(t, r.Trunk(), trunk.remote(Inactive))

	send(t, phone.rtp, rtpPacket(t, 104, 2, []byte{2}), r.Phone().LocalRTP())
	nothing(t, trunk.rtp)
	waitMetric(t, dropped(m, "phone", dropDirection), 1)

	rr := rtcpPacket(t)
	send(t, phone.rtcp, rr, r.Phone().LocalRTCP())

	if got, _ := receive(t, trunk.rtcp); !bytes.Equal(got, rr) {
		t.Fatalf("RTCP on hold %x, want %x", got, rr)
	}

	setRemote(t, r.Phone(), phone.remote(RecvOnly))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	relayed(t, trunk, phone, r.Trunk(), r.Phone(), rtpPacket(t, 104, 3, []byte{3}))
	send(t, phone.rtp, rtpPacket(t, 104, 3, []byte{3}), r.Phone().LocalRTP())
	nothing(t, trunk.rtp)

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	relayed(t, phone, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 104, 4, []byte{4}))
}

func TestEarlyPackets(t *testing.T) {
	g, m := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk := newEndpoint(t, loopback), newEndpoint(t, loopback)

	send(t, trunk.rtp, rtpPacket(t, 104, 1, []byte{1}), r.Trunk().LocalRTP())
	waitMetric(t, dropped(m, "trunk", dropNoRemote), 1)

	setRemote(t, r.Phone(), phone.remote(SendRecv))

	send(t, trunk.rtp, rtpPacket(t, 104, 2, []byte{2}), r.Trunk().LocalRTP())
	nothing(t, phone.rtp)
	waitMetric(t, dropped(m, "trunk", dropNoRemote), 2)

	send(t, phone.rtp, rtpPacket(t, 104, 1, []byte{1}), r.Phone().LocalRTP())
	waitMetric(t, dropped(m, "phone", dropNoDestination), 1)

	setRemote(t, r.Trunk(), trunk.remote(SendRecv))
	relayed(t, trunk, phone, r.Trunk(), r.Phone(), rtpPacket(t, 104, 3, []byte{3}))
	relayed(t, phone, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 104, 2, []byte{2}))

	setRemote(t, r.Trunk(), Remote{})
	send(t, trunk.rtp, rtpPacket(t, 104, 4, []byte{4}), r.Trunk().LocalRTP())
	nothing(t, phone.rtp)

	setRemote(t, r.Trunk(), Remote{RTP: netip.MustParseAddrPort("0.0.0.0:9"), Direction: Inactive})
	send(t, trunk.rtp, rtpPacket(t, 104, 5, []byte{5}), r.Trunk().LocalRTP())
	nothing(t, phone.rtp)
	waitMetric(t, dropped(m, "trunk", dropNoRemote), 4)
}

func TestPoolExhaustion(t *testing.T) {
	g, _ := newGateway(t, 2)

	var relays []*Relay

	for {
		r, err := g.Allocate(context.Background(), Allocation{TrunkID: testTrunk})
		if errors.Is(err, ErrNoPorts) {
			break
		}

		if err != nil {
			t.Fatal(err)
		}

		relays = append(relays, r)
	}

	if len(relays) > 1 {
		t.Fatalf("allocated %d relays from 2 port pairs", len(relays))
	}

	if len(relays) == 0 {
		t.Skip("ports of the range are taken by another process")
	}

	_ = relays[0].Close()

	r, err := g.Allocate(context.Background(), Allocation{TrunkID: testTrunk})
	if err != nil {
		t.Fatalf("allocate after release: %v", err)
	}

	_ = r.Close()
	_ = r.Close()
}

func TestNewValidation(t *testing.T) {
	bind := WithAddresses(Address{Bind: loopback})

	for name, opts := range map[string][]Option{
		"no address":         nil,
		"unspecified IPv4":   {WithAddresses(Address{Bind: netip.IPv4Unspecified()})},
		"unspecified IPv6":   {WithAddresses(Address{Bind: netip.IPv6Unspecified()})},
		"twice":              {bind, bind},
		"family mismatch":    {WithAddresses(Address{Bind: loopback, Advertise: netip.MustParseAddr("2001:db8::1")})},
		"unspecified public": {WithAddresses(Address{Bind: loopback, Advertise: netip.IPv4Unspecified()})},
		"privileged ports":   {bind, WithPortRange(1000, 2000)},
		"odd start":          {bind, WithPortRange(30001, 30010)},
		"empty range":        {bind, WithPortRange(30000, 30000)},
		"negative timeout":   {bind, WithMediaTimeout(-time.Second)},
	} {
		if _, err := New(opts...); err == nil {
			t.Errorf("New with %s succeeded", name)
		}
	}

	g, err := New(bind)
	if err != nil {
		t.Fatal(err)
	}

	_ = g.Close()

	if p := g.pools[loopback]; p.free[0] != DefaultMinPort || p.available() != (DefaultMaxPort-DefaultMinPort+1)/2 {
		t.Fatalf("default pool starts at %d with %d pairs", p.free[0], p.available())
	}
}

func TestAdvertisedAddress(t *testing.T) {
	public := netip.MustParseAddr("203.0.113.7")
	g, _ := newGateway(t, 8, WithAddresses(Address{Bind: netip.MustParseAddr("127.0.0.2"), Advertise: public}))

	r, err := g.Allocate(context.Background(), Allocation{TrunkID: testTrunk, TrunkAddress: netip.MustParseAddr("127.0.0.2")})
	if err != nil {
		t.Skipf("127.0.0.2 isn't bindable: %v", err)
	}

	if got := r.Trunk().AdvertisedRTP(); got != netip.AddrPortFrom(public, r.Trunk().LocalRTP().Port()) {
		t.Fatalf("advertised RTP %s for local %s", got, r.Trunk().LocalRTP())
	}

	if got := r.Trunk().AdvertisedRTCP(); got != netip.AddrPortFrom(public, r.Trunk().LocalRTCP().Port()) {
		t.Fatalf("advertised RTCP %s for local %s", got, r.Trunk().LocalRTCP())
	}

	if got := r.Phone().AdvertisedRTP(); got != r.Phone().LocalRTP() {
		t.Fatalf("advertised RTP %s without a public address, want %s", got, r.Phone().LocalRTP())
	}

	if got := g.Addresses(); len(got) != 2 || got[1].Advertise != public || got[0].Advertise != loopback {
		t.Fatalf("addresses %v", got)
	}
}

func TestAllocateAddresses(t *testing.T) {
	g, _ := newGateway(t, 8)

	_, err := g.Allocate(context.Background(), Allocation{PhoneAddress: netip.MustParseAddr("127.0.0.3")})
	if err == nil {
		t.Fatal("Allocate on an address that isn't bound succeeded")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := g.Allocate(ctx, Allocation{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Allocate with a cancelled context: %v", err)
	}
}

func TestRelayOutlivesAllocateContext(t *testing.T) {
	g, _ := newGateway(t, 8)
	ctx, cancel := context.WithCancel(context.Background())

	r, err := g.Allocate(ctx, Allocation{TrunkID: testTrunk})
	if err != nil {
		t.Fatal(err)
	}

	cancel()

	phone, trunk := newEndpoint(t, loopback), newEndpoint(t, loopback)
	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))
	relayed(t, phone, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 104, 1, []byte{1}))

	select {
	case <-r.Done():
		t.Fatal("relay closed with the Allocate context")
	default:
	}

	if r.Err() != nil {
		t.Fatalf("Err %v on an open relay", r.Err())
	}
}

func TestIPv6(t *testing.T) {
	v6 := netip.MustParseAddr("::1")

	c, err := net.ListenUDP("udp6", net.UDPAddrFromAddrPort(netip.AddrPortFrom(v6, 0)))
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}

	_ = c.Close()

	g, _ := newGateway(t, 8, WithAddresses(Address{Bind: v6}))

	r, err := g.Allocate(context.Background(), Allocation{TrunkID: testTrunk, PhoneAddress: v6, TrunkAddress: loopback})
	if err != nil {
		t.Fatal(err)
	}

	phone, trunk := newEndpoint(t, v6), newEndpoint(t, loopback)

	if err := r.Phone().SetRemote(trunk.remote(SendRecv)); err == nil {
		t.Fatal("an IPv4 remote on an IPv6 leg was accepted")
	}

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	relayed(t, phone, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 104, 1, []byte{1}))
	relayed(t, trunk, phone, r.Trunk(), r.Phone(), rtpPacket(t, 104, 1, []byte{1}))
}

func inOrder(from uint16) []uint16 {
	seqs := make([]uint16, lossWindow)
	for i := range seqs {
		seqs[i] = from + uint16(i)
	}

	return seqs
}

func TestLossCounted(t *testing.T) {
	g, m := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk := newEndpoint(t, loopback), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	for _, seq := range append([]uint16{1, 2, 3, 5, 6, 10}, inOrder(11)...) {
		relayed(t, phone, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 104, seq, []byte{1}))
	}

	waitMetric(t, func() float64 { return testutil.ToFloat64(m.lost.WithLabelValues(testTrunk, "phone")) }, 4)
	waitMetric(t, func() float64 { return testutil.ToFloat64(m.lost.WithLabelValues(testTrunk, "trunk")) }, 0)
}

func TestLossCounter(t *testing.T) {
	for _, c := range []struct {
		name string
		ssrc []uint32
		seqs []uint16
		lost uint64
	}{
		{"in order", nil, []uint16{1, 2, 3, 4, 5}, 0},
		{"gaps", nil, []uint16{1, 2, 3, 5, 6, 10}, 4},
		{"reordered", nil, []uint16{1, 2, 3, 5, 4, 6, 8, 7, 9}, 0},
		{"duplicates and a gap", nil, []uint16{1, 2, 3, 3, 3, 4, 6, 7}, 1},
		{"wrap", nil, []uint16{65533, 65534, 65535, 0, 2}, 1},
		{"reordered across wrap", nil, []uint16{65533, 65534, 0, 65535, 1, 2}, 0},
		{"late beyond the window", nil, append(append([]uint16{1, 2, 4}, inOrder(5)...), 3), 1},
		{"long gap", nil, []uint16{1, 2, 3, 1003}, 999},
		{"first packets on probation", nil, []uint16{7, 1, 2, 3}, 0},
		{"restart", nil, []uint16{1, 2, 3, 20000, 20001, 20002, 20004}, 1},
		{"single jump", nil, []uint16{1, 2, 3, 20000, 4, 5}, 0},
		{"new SSRC", []uint32{1, 1, 1, 2, 2, 2}, []uint16{1, 2, 3, 500, 501, 503}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			var (
				lc   lossCounter
				lost uint64
			)

			last := c.seqs[len(c.seqs)-1]
			seqs := append(c.seqs, inOrder(last+1)...)

			for i, seq := range seqs {
				ssrc := uint32(1)
				if c.ssrc != nil {
					ssrc = c.ssrc[min(i, len(c.ssrc)-1)]
				}

				lost += lc.update(ssrc, seq)
			}

			if lost != c.lost {
				t.Fatalf("lost %d, want %d", lost, c.lost)
			}
		})
	}
}

func TestLossCountedOnlyOutsideWindow(t *testing.T) {
	var lc lossCounter

	lost := uint64(0)
	for _, seq := range []uint16{1, 2, 3, 5} {
		lost += lc.update(1, seq)
	}

	if lost != 0 {
		t.Fatalf("lost %d while 4 is still in the window", lost)
	}

	for _, seq := range inOrder(6) {
		lost += lc.update(1, seq)
	}

	if lost != 1 {
		t.Fatalf("lost %d after 4 left the window, want 1", lost)
	}
}

func TestCloseWithoutLeaks(t *testing.T) {
	before := runtime.NumGoroutine()
	g, m := newGateway(t, 64)

	var relays []*Relay

	for range 8 {
		relays = append(relays, allocate(t, g))
	}

	if got := g.Relays(); got != 8 {
		t.Fatalf("%d relays, want 8", got)
	}

	for _, r := range relays {
		_ = r.Close()

		if !errors.Is(r.Err(), ErrClosed) {
			t.Fatalf("Err %v after Close", r.Err())
		}
	}

	if got := g.Relays(); got != 0 {
		t.Fatalf("%d relays after closing them all", got)
	}

	for range 8 {
		allocate(t, g)
	}

	waitMetric(t, func() float64 { return testutil.ToFloat64(m.relays.WithLabelValues(testTrunk)) }, 8)

	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Allocate(context.Background(), Allocation{TrunkID: testTrunk}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Allocate after Close: %v", err)
	}

	if got := testutil.ToFloat64(m.relays.WithLabelValues(testTrunk)); got != 0 {
		t.Fatalf("%v relays in the gauge after Close", got)
	}

	waitMetric(t, func() float64 { return float64(runtime.NumGoroutine()) }, float64(before))

	for _, p := range g.pools {
		if got := p.available(); got != 64 {
			t.Fatalf("%d free port pairs after Close, want 64", got)
		}
	}
}

func BenchmarkRelay(b *testing.B) {
	g, _ := newGateway(b, 8)
	r := allocate(b, g)
	phone, trunk := newEndpoint(b, loopback), newEndpoint(b, loopback)

	setRemote(b, r.Phone(), phone.remote(SendRecv))
	setRemote(b, r.Trunk(), trunk.remote(SendRecv))

	pkt := rtpPacket(b, 104, 0, make([]byte, 61))
	buf := make([]byte, bufferSize)
	to := r.Phone().LocalRTP()

	const window = 32

	b.ReportAllocs()
	b.ResetTimer()

	start := time.Now()

	for sent := 0; sent < b.N; {
		n := min(window, b.N-sent)

		for i := range n {
			seq := uint16(sent + i)
			pkt[2], pkt[3] = byte(seq>>8), byte(seq)

			if _, err := phone.rtp.WriteToUDPAddrPort(pkt, to); err != nil {
				b.Fatal(err)
			}
		}

		_ = trunk.rtp.SetReadDeadline(time.Now().Add(2 * time.Second))

		for range n {
			if _, _, err := trunk.rtp.ReadFromUDPAddrPort(buf); err != nil {
				b.Fatal(err)
			}
		}

		sent += n
	}

	b.ReportMetric(float64(b.N)/time.Since(start).Seconds(), "pkt/s")
}

func TestMetricsForget(t *testing.T) {
	m := NewMetrics()
	m.trunk(testTrunk).legs[phoneLeg].rtp.Inc()
	m.trunk("other").legs[trunkLeg].lost.Inc()

	series := func() int {
		n := 0

		for _, c := range m.Collectors() {
			n += testutil.CollectAndCount(c)
		}

		return n
	}

	before := series()

	m.Forget(testTrunk)

	if got, want := series(), before/2; got != want {
		t.Fatalf("%d series after Forget, want %d", got, want)
	}

	if _, ok := m.trunks[testTrunk]; ok {
		t.Fatal("trunk still cached after Forget")
	}

	if got := testutil.ToFloat64(m.lost.WithLabelValues("other", "trunk")); got != 1 {
		t.Fatalf("other trunk lost %v, want 1", got)
	}

	if got := testutil.ToFloat64(m.trunk(testTrunk).legs[phoneLeg].rtp); got != 0 {
		t.Fatalf("forgotten trunk restarts at %v, want 0", got)
	}
}

func TestMalformedDropped(t *testing.T) {
	g, m := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk := newEndpoint(t, loopback), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	rr := rtcpPacket(t)
	send(t, phone.rtp, rr, r.Phone().LocalRTP())
	send(t, phone.rtp, rtpPacket(t, 104, 1, make([]byte, bufferSize)), r.Phone().LocalRTP())
	nothing(t, trunk.rtp)
	waitMetric(t, dropped(m, "phone", dropMalformed), 2)

	short := append([]byte(nil), rr...)
	short[3]++
	send(t, phone.rtcp, short, r.Phone().LocalRTCP())
	send(t, phone.rtcp, append(append([]byte(nil), rr...), 0, 0, 0, 0), r.Phone().LocalRTCP())
	send(t, phone.rtcp, append(append([]byte(nil), rr...), make([]byte, bufferSize)...), r.Phone().LocalRTCP())
	nothing(t, trunk.rtcp)
	waitMetric(t, dropped(m, "phone", dropMalformed), 5)

	relayed(t, phone, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 104, 2, make([]byte, bufferSize-12)))

	compound, err := rtp.AppendRTCP(nil, &rtp.ReceiverReport{SSRC: 1}, &rtp.Goodbye{Sources: []uint32{1}})
	if err != nil {
		t.Fatal(err)
	}

	send(t, phone.rtcp, compound, r.Phone().LocalRTCP())

	if got, _ := receive(t, trunk.rtcp); !bytes.Equal(got, compound) {
		t.Fatalf("relayed %x, want %x", got, compound)
	}
}

func timeoutRelay(t *testing.T, timeout time.Duration) (*Relay, *endpoint, *endpoint, chan *Relay) {
	t.Helper()

	g, _ := newGateway(t, 8, WithMediaTimeout(timeout))
	expired := make(chan *Relay, 1)

	r, err := g.Allocate(context.Background(), Allocation{TrunkID: testTrunk, OnMediaTimeout: func(r *Relay) { expired <- r }})
	if err != nil {
		t.Fatal(err)
	}

	phone, trunk := newEndpoint(t, loopback), newEndpoint(t, loopback)
	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	return r, phone, trunk, expired
}

func answered(t *testing.T, timeout time.Duration) (*Relay, *endpoint, *endpoint, chan *Relay) {
	t.Helper()

	r, phone, trunk, expired := timeoutRelay(t, timeout)
	r.Answered()
	r.Answered()

	return r, phone, trunk, expired
}

func TestNoMediaTimeoutBeforeAnswer(t *testing.T) {
	r, _, _, expired := timeoutRelay(t, 100*time.Millisecond)

	select {
	case <-expired:
		t.Fatal("media timeout while ringing")
	case <-time.After(400 * time.Millisecond):
	}

	r.Answered()

	select {
	case <-expired:
	case <-time.After(2 * time.Second):
		t.Fatal("no media timeout after the answer")
	}

	if !errors.Is(r.Err(), ErrMediaTimeout) {
		t.Fatalf("Err %v, want ErrMediaTimeout", r.Err())
	}
}

func TestMediaTimeout(t *testing.T) {
	r, _, trunk, expired := answered(t, 200*time.Millisecond)

	start := time.Now()
	deadline := time.After(2 * time.Second)

	for got := (*Relay)(nil); got == nil; {
		send(t, trunk.rtp, rtpPacket(t, 104, 1, []byte{1}), r.Trunk().LocalRTP())

		select {
		case got = <-expired:
			if got != r {
				t.Fatal("callback for another relay")
			}
		case <-deadline:
			t.Fatal("no media timeout although the phone leg was silent")
		case <-time.After(20 * time.Millisecond):
		}
	}

	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("media timeout after %s", elapsed)
	}

	<-r.Done()

	if !errors.Is(r.Err(), ErrMediaTimeout) {
		t.Fatalf("Err %v, want ErrMediaTimeout", r.Err())
	}

	if r.Trunk().LastReceived().IsZero() || !r.Phone().LastReceived().IsZero() {
		t.Fatalf("last received phone %v trunk %v", r.Phone().LastReceived(), r.Trunk().LastReceived())
	}

	if got := r.gw.Relays(); got != 0 {
		t.Fatalf("%d relays after the timeout", got)
	}
}

func TestMediaTimeoutKeptAliveByRTCP(t *testing.T) {
	r, phone, trunk, expired := answered(t, 200*time.Millisecond)
	rr := rtcpPacket(t)

	stop := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(stop) {
		send(t, phone.rtcp, rr, r.Phone().LocalRTCP())
		send(t, trunk.rtcp, rr, r.Trunk().LocalRTCP())
		time.Sleep(30 * time.Millisecond)
	}

	select {
	case <-expired:
		t.Fatal("media timeout while RTCP flowed")
	case <-r.Done():
		t.Fatal("relay closed while RTCP flowed")
	default:
	}
}

func TestMediaTimeoutOnHold(t *testing.T) {
	r, phone, trunk, expired := answered(t, 100*time.Millisecond)

	setRemote(t, r.Phone(), phone.remote(SendOnly))
	setRemote(t, r.Trunk(), trunk.remote(RecvOnly))

	select {
	case <-expired:
		t.Fatal("media timeout on hold")
	case <-time.After(400 * time.Millisecond):
	}

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	select {
	case <-expired:
	case <-time.After(2 * time.Second):
		t.Fatal("no media timeout after the call was resumed")
	}
}

func TestMediaTimeoutOff(t *testing.T) {
	g, _ := newGateway(t, 8)
	r := allocate(t, g)

	time.Sleep(100 * time.Millisecond)

	select {
	case <-r.Done():
		t.Fatal("relay closed without a media timeout")
	default:
	}
}

func TestMediaTimeoutPeerDoesNotReceive(t *testing.T) {
	r, _, trunk, expired := answered(t, 100*time.Millisecond)

	setRemote(t, r.Trunk(), trunk.remote(SendOnly))

	stop := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(stop) {
		send(t, trunk.rtp, rtpPacket(t, 104, 1, []byte{1}), r.Trunk().LocalRTP())
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case <-expired:
		t.Fatal("media timeout of a phone that has no one to send to")
	default:
	}
}

func TestMetricsConfigure(t *testing.T) {
	g, m := newGateway(t, 4)

	series := func() int {
		n := 0

		for _, c := range m.Collectors() {
			n += testutil.CollectAndCount(c)
		}

		return n
	}

	g.Configure([]string{testTrunk, "other"})

	perTrunk := series() / 2
	if perTrunk == 0 {
		t.Fatal("no series for the configured trunks")
	}

	r := allocate(t, g)

	g.Configure([]string{"other"})

	if got := series(); got != 2*perTrunk {
		t.Fatalf("%d series while the deleted trunk relays media, want %d", got, 2*perTrunk)
	}

	_ = r.Close()

	if got := series(); got != perTrunk {
		t.Fatalf("%d series once the deleted trunk's relay closed, want %d", got, perTrunk)
	}

	if got := testutil.ToFloat64(m.relays.WithLabelValues("other")); got != 0 {
		t.Fatalf("relays of other %v, want 0", got)
	}
}
