package trgw

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func ipv6Loopback(t *testing.T) netip.Addr {
	t.Helper()

	v6 := netip.MustParseAddr("::1")

	c, err := net.ListenUDP("udp6", net.UDPAddrFromAddrPort(netip.AddrPortFrom(v6, 0)))
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}

	_ = c.Close()

	return v6
}

func TestMuxedRemote(t *testing.T) {
	g, m := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk := newEndpoint(t, loopback), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), Remote{RTP: addrOf(phone.rtp), RTCP: addrOf(phone.rtp), Mux: true})
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	if got := r.Phone().Remote().RTCP; got != addrOf(phone.rtp) {
		t.Fatalf("muxed RTCP destination %s, want %s", got, addrOf(phone.rtp))
	}

	relayed(t, phone, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 104, 1, []byte{1}))

	rr := rtcpPacket(t)
	send(t, phone.rtp, rr, r.Phone().LocalRTP())

	got, from := receive(t, trunk.rtcp)
	if !bytes.Equal(got, rr) || from != r.Trunk().LocalRTCP() {
		t.Fatalf("muxed RTCP relayed as %x from %s", got, from)
	}

	send(t, trunk.rtcp, rr, r.Trunk().LocalRTCP())

	got, from = receive(t, phone.rtp)
	if !bytes.Equal(got, rr) || from != r.Phone().LocalRTP() {
		t.Fatalf("RTCP towards a muxed remote relayed as %x from %s", got, from)
	}

	send(t, trunk.rtp, rr, r.Trunk().LocalRTP())
	waitMetric(t, dropped(m, "trunk", dropMalformed), 1)
}

func TestSeparateRTCPOnSamePortRefused(t *testing.T) {
	g, _ := newGateway(t, 8)
	r := allocate(t, g)
	phone := newEndpoint(t, loopback)

	if err := r.Phone().SetRemote(Remote{RTP: addrOf(phone.rtp), RTCP: addrOf(phone.rtp)}); err == nil {
		t.Fatal("RTCP on the RTP port without multiplexing was accepted")
	}
}

func TestForbiddenRemotes(t *testing.T) {
	public := netip.MustParseAddr("192.0.2.10")

	host := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.1/24"), netip.MustParsePrefix("172.16.5.9/16"),
		netip.MustParsePrefix("198.51.100.7/32"), netip.MustParsePrefix("2001:db8:1::5/64"),
	}
	g := &Gateway{
		addrs: []Address{{Bind: netip.MustParseAddr("10.0.0.1"), Advertise: public}},
		host:  newHostAddrs(func() ([]netip.Prefix, error) { return host, nil }),
	}
	local := netip.MustParseAddr("10.0.0.1")

	for _, c := range []struct {
		addr string
		ok   bool
	}{
		{"10.0.0.2", true},
		{"203.0.113.7", true},
		{"2001:db8::1", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"169.254.1.1", false},
		{"fe80::1", false},
		{"224.0.0.1", false},
		{"ff02::1", false},
		{"255.255.255.255", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"10.0.0.1", false},
		{"192.0.2.10", false},
		{"10.0.0.255", false},
		{"10.0.0.254", true},
		{"172.16.255.255", false},
		{"172.16.255.254", true},
		{"172.16.5.9", false},
		{"198.51.100.7", false},
		{"2001:db8:1::5", false},
		{"2001:db8:1::ff", true},
		{"192.0.2.255", true},
	} {
		if got := g.allowed(local, netip.MustParseAddr(c.addr)); got != c.ok {
			t.Errorf("remote %s allowed %v, want %v", c.addr, got, c.ok)
		}
	}

	if g.allowed(loopback, netip.MustParseAddr("10.0.0.2")) || !g.allowed(loopback, netip.MustParseAddr("127.0.0.5")) {
		t.Error("a loopback binding must reach loopback remotes only")
	}
}

func TestForbiddenRemoteRefused(t *testing.T) {
	g, _ := newGateway(t, 8)
	r := allocate(t, g)

	if err := r.Phone().SetRemote(Remote{RTP: netip.MustParseAddrPort("192.0.2.1:4000")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("SetRemote with a non-loopback remote on a loopback binding: %v", err)
	}
}

func TestDualFamilyLeg(t *testing.T) {
	v6 := ipv6Loopback(t)
	g, _ := newGateway(t, 8, WithAddresses(Address{Bind: v6}))
	r := allocate(t, g)

	if _, ok := r.Phone().Bound(false); ok {
		t.Fatal("IPv6 binding before Open")
	}

	if err := r.Phone().SetRemote(Remote{RTP: netip.AddrPortFrom(v6, 4000)}); !errors.Is(err, ErrFamily) {
		t.Fatalf("SetRemote without a binding in the family: %v", err)
	}

	b6, err := r.Phone().Open(v6)
	if err != nil {
		t.Fatal(err)
	}

	if again, err := r.Phone().Open(v6); err != nil || again != b6 {
		t.Fatalf("second Open of the family gave %+v, %v", again, err)
	}

	if b6.AdvertisedRTP.Addr() != v6 {
		t.Fatalf("IPv6 binding advertised as %s", b6.AdvertisedRTP)
	}

	phone, trunk := newEndpoint(t, v6), newEndpoint(t, loopback)
	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	send(t, phone.rtp, rtpPacket(t, 104, 1, []byte{1}), b6.LocalRTP)

	if got, _ := receive(t, trunk.rtp); len(got) == 0 {
		t.Fatal("nothing relayed from the IPv6 binding")
	}

	send(t, trunk.rtp, rtpPacket(t, 104, 1, []byte{1}), r.Trunk().LocalRTP())

	if _, from := receive(t, phone.rtp); from != b6.LocalRTP {
		t.Fatalf("relayed to the IPv6 phone from %s, want %s", from, b6.LocalRTP)
	}

	r.Phone().Keep(false)

	if _, ok := r.Phone().Bound(true); ok {
		t.Fatal("the unused IPv4 binding was kept")
	}

	if got := r.Phone().LocalRTP(); got != b6.LocalRTP {
		t.Fatalf("primary binding %s, want %s", got, b6.LocalRTP)
	}
}

func TestKeepWaitsForMedia(t *testing.T) {
	v6 := ipv6Loopback(t)
	g, _ := newGateway(t, 8, WithAddresses(Address{Bind: v6}))
	r := allocate(t, g)
	old, phone, trunk := newEndpoint(t, loopback), newEndpoint(t, v6), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), old.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))
	relayed(t, old, trunk, r.Phone(), r.Trunk(), rtpPacket(t, 104, 1, []byte{1}))

	b4 := r.Phone().LocalRTP()

	b6, err := r.Phone().Open(v6)
	if err != nil {
		t.Fatal(err)
	}

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	r.Phone().Keep(false)

	if _, ok := r.Phone().Bound(true); !ok {
		t.Fatal("the IPv4 binding in use was closed before media arrived on IPv6")
	}

	send(t, old.rtp, rtpPacket(t, 104, 2, []byte{2}), b4)

	if got, _ := receive(t, trunk.rtp); len(got) == 0 {
		t.Fatal("media from the old address was not relayed")
	}

	send(t, phone.rtp, rtpPacket(t, 104, 3, []byte{3}), b6.LocalRTP)

	if got, _ := receive(t, trunk.rtp); len(got) == 0 {
		t.Fatal("media on the IPv6 binding was not relayed")
	}

	deadline := time.Now().Add(2 * time.Second)

	for {
		if _, ok := r.Phone().Bound(true); !ok {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("the IPv4 binding outlived media on IPv6")
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestOpenAfterCloseRefused(t *testing.T) {
	v6 := ipv6Loopback(t)
	g, _ := newGateway(t, 8, WithAddresses(Address{Bind: v6}))
	r := allocate(t, g)
	_ = r.Close()

	if _, err := r.Phone().Open(v6); !errors.Is(err, ErrClosed) {
		t.Fatalf("Open on a closed relay: %v", err)
	}
}

func TestPolicing(t *testing.T) {
	g, m := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk := newEndpoint(t, loopback), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	r.Phone().Police(8000)

	payload := make([]byte, 200)
	for i := range 20 {
		send(t, phone.rtp, rtpPacket(t, 104, uint16(i+1), payload), r.Phone().LocalRTP())
	}

	got := 0

	for {
		_ = trunk.rtp.SetReadDeadline(time.Now().Add(300 * time.Millisecond))

		if _, _, err := trunk.rtp.ReadFromUDPAddrPort(make([]byte, bufferSize)); err != nil {
			break
		}

		got++
	}

	if got < 8 || got > 9 || dropped(m, "phone", dropPoliced)() != float64(20-got) {
		t.Fatalf("%d of 20 packets of 240 octets relayed at 8 kbit/s, %v policed", got, dropped(m, "phone", dropPoliced)())
	}

	r.Phone().Police(0)
	relayed(t, trunk, phone, r.Trunk(), r.Phone(), rtpPacket(t, 104, 1, payload))
}

func TestBucket(t *testing.T) {
	now := time.Unix(0, 0)
	b := newBucket(8000, now)

	if !b.admit(2000, now) {
		t.Fatal("the burst allowance of two seconds was refused")
	}

	if b.admit(1, now) {
		t.Fatal("a packet above the bucket was admitted")
	}

	if !b.admit(500, now.Add(500*time.Millisecond)) || b.admit(1, now.Add(500*time.Millisecond)) {
		t.Fatal("the bucket did not refill at the sustainable rate")
	}

	if !b.admit(2000, now.Add(time.Hour)) || b.admit(1, now.Add(time.Hour)) {
		t.Fatal("the bucket grew above its depth")
	}
}

func TestEFMarking(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("DSCP marking is set on Linux only")
	}

	g, _ := newGateway(t, 8)
	r := allocate(t, g)

	b := r.Phone().binds.Load().primaryBinding()

	for _, c := range []*net.UDPConn{b.rtp, b.rtcp} {
		raw, err := c.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}

		var tos int

		err = raw.Control(func(fd uintptr) { tos, err = unix.GetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_TOS) })
		if err != nil {
			t.Fatal(err)
		}

		if tos != dscpEF<<2 {
			t.Fatalf("TOS %#x, want EF", tos)
		}
	}
}

func TestOpenNoPorts(t *testing.T) {
	v6 := ipv6Loopback(t)

	g, err := New(WithAddresses(Address{Bind: loopback}, Address{Bind: v6}), WithPortRange(30400, 30401))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = g.Close() })

	r, err := g.Allocate(context.Background(), Allocation{TrunkID: testTrunk, PhoneAddress: v6, TrunkAddress: loopback})
	if err != nil {
		t.Skipf("ports of the range are taken: %v", err)
	}

	if _, err := r.Trunk().Open(v6); !errors.Is(err, ErrNoPorts) {
		t.Fatalf("Open with the IPv6 pool exhausted: %v", err)
	}
}

func TestHostAddressesRefreshed(t *testing.T) {
	calls := 0
	h := newHostAddrs(func() ([]netip.Prefix, error) {
		calls++
		return []netip.Prefix{netip.MustParsePrefix("10.0.0.1/24")}, nil
	})

	now := time.Now()
	h.prefixes(now)
	h.prefixes(now.Add(time.Second))

	if calls != 1 {
		t.Fatalf("listed the host addresses %d times within the refresh period", calls)
	}

	h.prefixes(now.Add(hostRefresh))

	if calls != 2 {
		t.Fatalf("listed the host addresses %d times after the refresh period", calls)
	}
}

func TestInterfacePrefixes(t *testing.T) {
	ps, err := interfacePrefixes()
	if err != nil {
		t.Skipf("no interface addresses: %v", err)
	}

	for _, p := range ps {
		if p.Addr().Is4() && p.Bits() > 32 || p.Bits() < 0 {
			t.Errorf("prefix %s", p)
		}
	}
}

func TestRTCPRangePayloadTypeWithoutMux(t *testing.T) {
	g, _ := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk := newEndpoint(t, loopback), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))

	pkt := rtpPacket(t, 72, 1, make([]byte, 160))
	pkt[1] |= 0x80

	relayed(t, phone, trunk, r.Phone(), r.Trunk(), pkt)
}

func TestPreviousRemoteExpires(t *testing.T) {
	g, _ := newGateway(t, 8)
	r := allocate(t, g)
	old, moved := newEndpoint(t, loopback), newEndpoint(t, loopback)
	l := r.Trunk()

	setRemote(t, l, old.remote(SendRecv))
	setRemote(t, l, moved.remote(SendRecv))

	now := time.Now()
	later := now.Add(time.Hour).UnixNano()

	if ok, _ := l.remote.Load().rtpFrom(addrOf(old.rtp), later); !ok {
		t.Fatal("the old remote expired before the exchange ended")
	}

	l.Commit()

	rs := l.remote.Load()
	if ok, _ := rs.rtpFrom(addrOf(old.rtp), now.UnixNano()); !ok {
		t.Fatal("the old remote refused within the grace period")
	}

	expired := now.Add(prevGrace + time.Second).UnixNano()

	if ok, _ := rs.rtpFrom(addrOf(old.rtp), expired); ok || rs.rtcpFrom(addrOf(old.rtcp), expired) {
		t.Fatal("the old remote accepted after the grace period")
	}

	if ok, current := rs.rtpFrom(addrOf(moved.rtp), expired); !ok || !current {
		t.Fatal("the new remote refused")
	}

	setRemote(t, l, Remote{RTP: addrOf(moved.rtp), RTCP: addrOf(moved.rtcp), Direction: SendOnly})

	if l.remote.Load().prevUntil != rs.prevUntil {
		t.Fatal("a direction change restarted the grace period of the old remote")
	}
}

func TestRevertDropsRejectedRemote(t *testing.T) {
	g, m := newGateway(t, 8)
	r := allocate(t, g)
	phone, trunk, rejected := newEndpoint(t, loopback), newEndpoint(t, loopback), newEndpoint(t, loopback)

	setRemote(t, r.Phone(), phone.remote(SendRecv))
	setRemote(t, r.Trunk(), trunk.remote(SendRecv))
	r.Trunk().Commit()

	setRemote(t, r.Trunk(), rejected.remote(SendRecv))

	if err := r.Trunk().Revert(trunk.remote(SendRecv)); err != nil {
		t.Fatal(err)
	}

	if rs := r.Trunk().remote.Load(); rs.prev != nil || rs.RTP != addrOf(trunk.rtp) {
		t.Fatalf("after the revert: remote %s, previous %v", rs.RTP, rs.prev)
	}

	send(t, rejected.rtp, rtpPacket(t, 104, 1, []byte{1}), r.Trunk().LocalRTP())
	nothing(t, phone.rtp)
	waitMetric(t, dropped(m, "trunk", dropUnexpectedSource), 1)

	relayed(t, trunk, phone, r.Trunk(), r.Phone(), rtpPacket(t, 104, 2, []byte{2}))
}

func TestRevertRestoresPendingPrevious(t *testing.T) {
	g, _ := newGateway(t, 8)
	r := allocate(t, g)
	a, b, c := newEndpoint(t, loopback), newEndpoint(t, loopback), newEndpoint(t, loopback)
	l := r.Trunk()

	setRemote(t, l, a.remote(SendRecv))
	setRemote(t, l, b.remote(SendRecv))
	l.Commit()

	committed := l.remote.Load()

	setRemote(t, l, c.remote(SendRecv))

	if err := l.Revert(b.remote(SendRecv)); err != nil {
		t.Fatal(err)
	}

	rs := l.remote.Load()
	if rs.RTP != addrOf(b.rtp) || rs.prev == nil || rs.prev.RTP != addrOf(a.rtp) || rs.prevUntil != committed.prevUntil {
		t.Fatalf("after the revert: remote %s, previous %v until %d", rs.RTP, rs.prev, rs.prevUntil)
	}
}

func TestDropSecondFamily(t *testing.T) {
	v6 := ipv6Loopback(t)
	g, _ := newGateway(t, 8, WithAddresses(Address{Bind: v6}))
	r := allocate(t, g)
	l := r.Phone()

	l.Drop(true)

	if _, ok := l.Bound(true); !ok {
		t.Fatal("dropped the only binding")
	}

	b6, err := l.Open(v6)
	if err != nil {
		t.Fatal(err)
	}

	l.Drop(false)

	if _, ok := l.Bound(false); ok {
		t.Fatal("the IPv6 binding was kept")
	}

	if _, ok := l.Bound(true); !ok {
		t.Fatal("the IPv4 binding was dropped")
	}

	c, err := net.ListenUDP("udp6", net.UDPAddrFromAddrPort(b6.LocalRTP))
	if err != nil {
		t.Fatalf("the IPv6 port %s is still bound: %v", b6.LocalRTP, err)
	}

	_ = c.Close()
}
