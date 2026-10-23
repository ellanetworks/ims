package media_test

import (
	"net"
	"net/netip"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/mcptt/rtp"
)

// The relay on the system's UDP, on the loopback: the talker's media reaches the other leg.
func TestUDPLoopback(t *testing.T) {
	lo := netip.MustParseAddr("127.0.0.1")

	r, err := media.New(media.Config{Network: media.UDP{}, Addr: lo, MinPort: 41000, MaxPort: 41999})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = r.Close() }()

	call := r.NewCall()
	defer call.Close()

	socket := func() *net.UDPConn {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = c.Close() })

		return c
	}

	addr := func(c *net.UDPConn) netip.AddrPort { return c.LocalAddr().(*net.UDPAddr).AddrPort() }

	var legs []*media.Leg

	var peers []*net.UDPConn

	for range 2 {
		l, err := call.AddLeg(media.LegConfig{Mux: true, SharedControl: true, PayloadType: 96})
		if err != nil {
			t.Fatal(err)
		}

		p := socket()
		l.SetRemote(media.Remote{Audio: addr(p), Control: addr(p)})
		legs, peers = append(legs, l), append(peers, p)
	}

	call.Forward(legs[0], true)

	pkt := packet(t, legs[0].AudioSSRC(), 1, 1, 2, 3)
	if _, err := peers[0].WriteToUDPAddrPort(pkt, legs[0].Audio()); err != nil {
		t.Fatal(err)
	}

	_ = peers[1].SetReadDeadline(time.Now().Add(5 * time.Second))

	buf := make([]byte, 100)

	n, from, err := peers[1].ReadFromUDPAddrPort(buf)
	if err != nil || string(buf[:n]) != string(pkt) || from != legs[1].Audio() {
		t.Fatalf("%x from %s, %v", buf[:n], from, err)
	}
}

// discard is a network whose sockets send nowhere, for the cost of the relay alone.
type discard struct {
	handlers map[netip.AddrPort]func([]byte, netip.AddrPort)
}

func (d *discard) Listen(addr netip.AddrPort, h func([]byte, netip.AddrPort)) (media.Conn, error) {
	d.handlers[addr] = h
	return discardConn(addr), nil
}

type discardConn netip.AddrPort

func (discardConn) WriteTo([]byte, netip.AddrPort) error { return nil }
func (c discardConn) LocalAddr() netip.AddrPort          { return netip.AddrPort(c) }
func (discardConn) Close() error                         { return nil }

// The forwarding of a talker's packet to the 99 other legs of a 100-member call, per core.
func BenchmarkForward100(b *testing.B) {
	d := &discard{handlers: map[netip.AddrPort]func([]byte, netip.AddrPort){}}

	r, err := media.New(media.Config{Network: d, Addr: server, MinPort: 20000, MaxPort: 20999, ControlPort: 19999})
	if err != nil {
		b.Fatal(err)
	}

	call := r.NewCall()

	var talker *media.Leg

	for i := range 100 {
		l, err := call.AddLeg(media.LegConfig{Mux: true, SharedControl: true, PayloadType: 96, OnMedia: func() {}})
		if err != nil {
			b.Fatal(err)
		}

		peer := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 2, byte(i >> 8), byte(i)}), 4000)
		l.SetRemote(media.Remote{Audio: peer, Control: peer})

		if i == 0 {
			talker = l
		}
	}

	call.Forward(talker, true)

	h := d.handlers[talker.Audio()]
	from := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 2, 0, 0}), 4000)

	// An AMR-WB frame of 23.85 kbit/s in 20 ms (TS 26.179 §4.1.1, RFC 4867).
	pkt := packet(b, talker.AudioSSRC(), 1, make([]byte, 62)...)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		h(pkt, from)
	}

	b.ReportMetric(float64(b.N*99)/b.Elapsed().Seconds(), "sends/s")
}

// RFC 3550 §7.2: the RTCP of a talker of an SSRC of its own, translated to the 99 other legs without allocating.
func TestRTCPAllocs(t *testing.T) {
	d := &discard{handlers: map[netip.AddrPort]func([]byte, netip.AddrPort){}}

	r, err := media.New(media.Config{Network: d, Addr: server, MinPort: 20000, MaxPort: 20999, ControlPort: 19999})
	if err != nil {
		t.Fatal(err)
	}

	call := r.NewCall()

	var talker *media.Leg

	for i := range 100 {
		l, err := call.AddLeg(media.LegConfig{Mux: true, SharedControl: true, PayloadType: 96})
		if err != nil {
			t.Fatal(err)
		}

		peer := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 2, byte(i >> 8), byte(i)}), 4000)
		l.SetRemote(media.Remote{Audio: peer, Control: peer})

		if i == 0 {
			talker = l
		}
	}

	call.Forward(talker, true)

	h := d.handlers[talker.Audio()]
	from := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 2, 0, 0}), 4000)
	h(packet(t, 0xabcd, 1, 1), from)

	sr, _ := rtp.AppendRTCP(nil, &rtp.SenderReport{SSRC: 0xabcd, Reports: []rtp.ReceptionReport{{SSRC: 7}}},
		&rtp.SourceDescription{Chunks: []rtp.SDESChunk{{Source: 0xabcd, Items: []rtp.SDESItem{{Type: rtp.SDESCNAME, Text: "a@x"}}}}})

	if n := testing.AllocsPerRun(100, func() { h(sr, from) }); n != 0 {
		t.Errorf("%v allocations", n)
	}
}

// Close returns once the reader of the socket returned: no datagram is handled after it.
func TestUDPCloseWaitsForReader(t *testing.T) {
	conns := make(chan media.Conn, 1)
	entered := make(chan struct{})

	var handled atomic.Bool

	c, err := media.UDP{}.Listen(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 0), func(_ []byte, from netip.AddrPort) {
		c := <-conns

		close(entered)

		// Until Close has closed the socket.
		for c.WriteTo([]byte{0}, from) == nil {
			runtime.Gosched()
		}

		handled.Store(true)
	})
	if err != nil {
		t.Fatal(err)
	}

	conns <- c

	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = peer.Close() }()

	if _, err := peer.WriteToUDPAddrPort([]byte{0}, c.LocalAddr()); err != nil {
		t.Fatal(err)
	}

	<-entered

	_ = c.Close()

	if !handled.Load() {
		t.Error("Close returned with the datagram being handled")
	}
}
