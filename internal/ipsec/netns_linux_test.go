//go:build linux && (amd64 || arm64)

package ipsec

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/netnstest"
)

func TestMain(m *testing.M) {
	netnstest.Main(m)
}

type netns struct {
	*netnstest.Netns
}

func newNetns(t *testing.T) *netns {
	t.Helper()

	return &netns{netnstest.New(t)}
}

func (n *netns) do(f func()) {
	n.Do(f)
}

func (n *netns) ip(t *testing.T, args ...string) {
	t.Helper()
	n.IP(t, args...)
}

func (n *netns) xfrm(t *testing.T) *XFRM {
	t.Helper()

	var (
		x   *XFRM
		err error
	)

	n.do(func() { x, err = Open() })

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = x.Close() })

	return x
}

type lab struct {
	p, u     *netns
	px, ux   *XFRM
	p4, u4   netip.Addr
	p6, u6   netip.Addr
	keys     Keys
	spis     *SPIs
	nextPort uint16
}

func newLab(t *testing.T) *lab {
	t.Helper()

	l := &lab{
		p: newNetns(t), u: newNetns(t),
		p4: netip.MustParseAddr("10.0.0.1"), u4: netip.MustParseAddr("10.0.0.2"),
		p6: netip.MustParseAddr("fd00::1"), u6: netip.MustParseAddr("fd00::2"),
		keys: Keys{
			CK: []byte("0123456789abcdef"),
			IK: []byte("fedcba9876543210"),
		},
		spis:     NewSPIs(),
		nextPort: 6000,
	}

	netnstest.Link(t, l.p.Netns, l.u.Netns,
		[]netip.Prefix{netip.PrefixFrom(l.p4, 24), netip.PrefixFrom(l.p6, 64)},
		[]netip.Prefix{netip.PrefixFrom(l.u4, 24), netip.PrefixFrom(l.u6, 64)})

	l.px, l.ux = l.p.xfrm(t), l.u.xfrm(t)

	return l
}

func (l *lab) set(t *testing.T, v6 bool, i Integrity, e Encryption) Set {
	t.Helper()

	p, u := l.p4, l.u4
	if v6 {
		p, u = l.p6, l.u6
	}

	pc, ps, err := l.spis.Allocate()
	if err != nil {
		t.Fatal(err)
	}

	uc, us, err := l.spis.Allocate()
	if err != nil {
		t.Fatal(err)
	}

	port := l.nextPort
	l.nextPort += 4

	return Set{
		Local:      Endpoint{Addr: p, PortC: port, PortS: port + 1, SPIC: pc, SPIS: ps},
		Remote:     Endpoint{Addr: u, PortC: port + 2, PortS: port + 3, SPIC: uc, SPIS: us},
		Integrity:  i,
		Encryption: e,
	}
}

func (l *lab) install(t *testing.T, s Set) {
	t.Helper()

	if err := l.px.Install(s, l.keys); err != nil {
		t.Fatal(err)
	}

	if err := l.ux.Install(s.Reverse(), l.keys); err != nil {
		t.Fatal(err)
	}
}

func listenUDP(t *testing.T, n *netns, a netip.Addr, port uint16) *net.UDPConn {
	t.Helper()

	var (
		c   *net.UDPConn
		err error
	)

	n.do(func() { c, err = net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(a, port))) })

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return c
}

func udpDelivered(t *testing.T, from, to *net.UDPConn, wait time.Duration) bool {
	t.Helper()

	msg := fmt.Sprintf("REGISTER %s -> %s", from.LocalAddr(), to.LocalAddr())

	if _, err := from.WriteTo([]byte(msg), to.LocalAddr()); err != nil {
		t.Fatal(err)
	}

	_ = to.SetReadDeadline(time.Now().Add(wait))

	buf := make([]byte, 1500)

	n, src, err := to.ReadFrom(buf)
	if err != nil {
		return false
	}

	if string(buf[:n]) != msg || src.String() != from.LocalAddr().String() {
		t.Fatalf("received %q from %s", buf[:n], src)
	}

	return true
}

func tcpExchange(t *testing.T, cn *netns, from netip.AddrPort, sn *netns, to netip.AddrPort) {
	t.Helper()

	var (
		l   net.Listener
		err error
	)

	sn.do(func() {
		var lc net.ListenConfig

		l, err = lc.Listen(context.Background(), "tcp", to.String())
	})

	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = l.Close() }()

	accepted := make(chan net.Conn, 1)

	go func() {
		c, err := l.Accept()
		if err != nil {
			close(accepted)
			return
		}

		accepted <- c
	}()

	var c net.Conn

	cn.do(func() {
		d := net.Dialer{LocalAddr: net.TCPAddrFromAddrPort(from), Timeout: 2 * time.Second}
		c, err = d.DialContext(context.Background(), "tcp", to.String())
	})

	if err != nil {
		t.Fatalf("TCP %s -> %s: %v", from, to, err)
	}

	defer func() { _ = c.Close() }()

	s, ok := <-accepted
	if !ok {
		t.Fatal("accept failed")
	}

	defer func() { _ = s.Close() }()

	for _, dir := range []struct {
		w, r net.Conn
	}{{c, s}, {s, c}} {
		if _, err := dir.w.Write([]byte("SIP/2.0 200 OK\r\n")); err != nil {
			t.Fatal(err)
		}

		buf := make([]byte, 64)
		_ = dir.r.SetReadDeadline(time.Now().Add(2 * time.Second))

		n, err := dir.r.Read(buf)
		if err != nil || string(buf[:n]) != "SIP/2.0 200 OK\r\n" {
			t.Fatalf("TCP %s -> %s: read %q, %v", from, to, buf[:n], err)
		}
	}
}

func (x *XFRM) owned(t *testing.T) (map[uint32]uint64, int) {
	t.Helper()

	sas, err := x.dump(xfrmMsgGetSA)
	if err != nil {
		t.Fatal(err)
	}

	policies, err := x.dump(xfrmMsgGetPolicy)
	if err != nil {
		t.Fatal(err)
	}

	packets := make(map[uint32]uint64)

	for _, b := range sas {
		if v, ok := decode[xfrmUsersaInfo](b); ok && v.sel.user == owner {
			packets[be32(v.id.spi)] = v.curlft.packets
		}
	}

	n := 0

	for _, b := range policies {
		if v, ok := decode[xfrmUserpolicyInfo](b); ok && v.sel.user == owner {
			n++
		}
	}

	return packets, n
}
