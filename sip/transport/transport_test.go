package transport_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transport"
)

var lo = netip.MustParseAddr("127.0.0.1")

func dialTCP(t *testing.T, to netip.AddrPort) net.Conn {
	t.Helper()

	d := net.Dialer{Timeout: siptest.Timeout}

	c, err := d.DialContext(context.Background(), "tcp", to.String())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = c.Close() })

	_ = c.SetDeadline(time.Now().Add(siptest.Timeout))

	return c
}

func sendUDP(t *testing.T, to netip.AddrPort, data string) {
	t.Helper()

	var d net.Dialer

	c, err := d.DialContext(context.Background(), "udp", to.String())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = c.Close() }()

	if _, err := c.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
}

func options(via string) string {
	return "OPTIONS sip:a@127.0.0.1 SIP/2.0\r\nVia: " + via + "\r\nFrom: <sip:a@b>;tag=1\r\nTo: <sip:a@b>\r\n" +
		"Call-ID: c1\r\nCSeq: 1 OPTIONS\r\nMax-Forwards: 70\r\nContent-Length: 0\r\n\r\n"
}

func TestTCPKeepalive(t *testing.T) {
	tr, rec := siptest.NewTransport(t, transport.Config{})
	local := siptest.Listen(t, tr, lo)
	c := dialTCP(t, local)

	if _, err := c.Write([]byte("\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 2)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "\r\n" {
		t.Fatalf("pong = %q, %v", buf, err)
	}

	if _, err := c.Write([]byte("\r\n" + options("SIP/2.0/TCP 127.0.0.1:1;branch=z9hG4bK1"))); err != nil {
		t.Fatal(err)
	}

	if got := rec.NextRequest(); got.Method != "OPTIONS" {
		t.Errorf("got %s", got.StartLine())
	}
}

func TestUDPKeepalivesIgnored(t *testing.T) {
	tr, rec := siptest.NewTransport(t, transport.Config{})
	local := siptest.Listen(t, tr, lo)

	sendUDP(t, local, "\r\n\r\n")
	sendUDP(t, local, "\r\n")
	sendUDP(t, local, "\x00\x01\x00\x00\x21\x12\xa4\x42abcdefghijkl")
	sendUDP(t, local, options("SIP/2.0/UDP 127.0.0.1:1;branch=z9hG4bK1"))

	if got := rec.NextRequest(); got.Method != "OPTIONS" {
		t.Errorf("got %s", got.StartLine())
	}

	rec.None(50 * time.Millisecond)
}

func TestTooLargeOverTCP(t *testing.T) {
	tr, rec := siptest.NewTransport(t, transport.Config{MaxMessageSize: 1000})
	local := siptest.Listen(t, tr, lo)
	c := dialTCP(t, local)

	msg := strings.Replace(options("SIP/2.0/TCP 127.0.0.1:1;branch=z9hG4bK1;rport"), "Content-Length: 0", "Content-Length: 5000", 1)
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}

	m, _, err := sip.NewStreamReader(c, 0).Next()
	if err != nil {
		t.Fatal(err)
	}

	res, ok := m.(*sip.Response)
	if !ok || res.StatusCode != 513 {
		t.Fatalf("got %s, want 513", m.StartLine())
	}

	if v := topVia(t, res); v.Received() != "127.0.0.1" {
		t.Errorf("513 Via = %s", v)
	}

	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("connection still open: %v", err)
	}

	rec.None(50 * time.Millisecond)
}

func TestNo513ForACK(t *testing.T) {
	tr, rec := siptest.NewTransport(t, transport.Config{MaxMessageSize: 1000})
	local := siptest.Listen(t, tr, lo)
	c := dialTCP(t, local)

	ack := siptest.NewRequest("ACK", "sip:a@127.0.0.1", sip.TCP, netip.MustParseAddrPort("127.0.0.1:1"))
	ack.SetBody("text/plain", []byte(strings.Repeat("x", 1000)))

	if _, err := c.Write(ack.Bytes()); err != nil {
		t.Fatal(err)
	}

	if n, err := c.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("read %d bytes, %v; want EOF without a 513", n, err)
	}

	rec.None(10 * time.Millisecond)
}

func TestLargeUDPRequestAccepted(t *testing.T) {
	tr, rec := siptest.NewTransport(t, transport.Config{MaxMessageSize: 1000})
	local := siptest.Listen(t, tr, lo)
	u := siptest.NewSocket(t, netip.AddrPortFrom(lo, 0))

	req := siptest.NewRequest("MESSAGE", "sip:a@127.0.0.1", sip.UDP, u.Addr())
	req.SetBody("text/plain", []byte(strings.Repeat("x", 60000)))
	u.Send(sip.UDP, local, req)

	if got := rec.NextRequest(); len(got.Body) != 60000 {
		t.Errorf("body of %d bytes", len(got.Body))
	}
}

func TestFirstMessageTimeout(t *testing.T) {
	tr, rec := siptest.NewTransport(t, transport.Config{FirstMessageTimeout: 200 * time.Millisecond})
	local := siptest.Listen(t, tr, lo)

	silent := dialTCP(t, local)
	active := dialTCP(t, local)

	if _, err := active.Write([]byte(options("SIP/2.0/TCP 127.0.0.1:1;branch=z9hG4bK1"))); err != nil {
		t.Fatal(err)
	}

	rec.NextRequest()

	start := time.Now()

	if _, err := silent.Read(make([]byte, 1)); !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("silent connection: %v, want a reset", err)
	}

	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("silent connection closed after %v", d)
	}

	_ = active.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := active.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("active connection: %v, want it still open", err)
	}
}

func TestMaxConnections(t *testing.T) {
	tr, rec := siptest.NewTransport(t, transport.Config{MaxConnections: 2})
	local := siptest.Listen(t, tr, lo)

	for i := range 2 {
		c := dialTCP(t, local)
		if _, err := c.Write([]byte(options("SIP/2.0/TCP 127.0.0.1:1;branch=z9hG4bK" + strconv.Itoa(i)))); err != nil {
			t.Fatal(err)
		}

		rec.NextRequest()
	}

	d := net.Dialer{Timeout: siptest.Timeout}

	extra, err := d.DialContext(context.Background(), "tcp", local.String())
	if err == nil {
		defer func() { _ = extra.Close() }()

		_, err = extra.Read(make([]byte, 1))
	}

	if !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("third connection: %v, want a reset", err)
	}
}

func TestListenRefusesHeldPort(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		var lc net.ListenConfig

		other, err := lc.Listen(context.Background(), "tcp", netip.AddrPortFrom(addr, 0).String())
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = other.Close() }()

		tr, _ := siptest.NewTransport(t, transport.Config{})
		if _, err := tr.Listen(context.Background(), other.Addr().(*net.TCPAddr).AddrPort()); err == nil {
			t.Error("listened on a port another socket listens on")
		}
	})
}

func TestListenFixedPort(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		tr, _ := siptest.NewTransport(t, transport.Config{})
		free := siptest.Listen(t, tr, addr)

		if err := tr.Close(); err != nil {
			t.Fatal(err)
		}

		tr, _ = siptest.NewTransport(t, transport.Config{})

		got, err := tr.Listen(context.Background(), free)
		if err != nil || got != free {
			t.Fatalf("Listen(%s) = %s, %v", free, got, err)
		}
	})
}

func TestSTUNBinding(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		tr, rec := siptest.NewTransport(t, transport.Config{})
		local := siptest.Listen(t, tr, addr)

		var lc net.ListenConfig

		pc, err := lc.ListenPacket(context.Background(), "udp", netip.AddrPortFrom(addr, 0).String())
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = pc.Close() }()

		src := pc.LocalAddr().(*net.UDPAddr).AddrPort()
		req := []byte{0x00, 0x01, 0x00, 0x00, 0x21, 0x12, 0xa4, 0x42, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}

		if _, err := pc.WriteTo(req, net.UDPAddrFromAddrPort(local)); err != nil {
			t.Fatal(err)
		}

		_ = pc.SetReadDeadline(time.Now().Add(siptest.Timeout))

		buf := make([]byte, 128)

		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}

		res := buf[:n]
		if binary.BigEndian.Uint16(res[0:2]) != 0x0101 || !bytes.Equal(res[4:20], req[4:20]) {
			t.Fatalf("not a Binding success response for our transaction: % x", res)
		}

		attr := res[20:]
		if binary.BigEndian.Uint16(attr[0:2]) != 0x0020 {
			t.Fatalf("attribute %#x, want XOR-MAPPED-ADDRESS", binary.BigEndian.Uint16(attr[0:2]))
		}

		port := binary.BigEndian.Uint16(attr[6:8]) ^ 0x2112
		ip := make([]byte, len(attr)-8)

		for i := range ip {
			ip[i] = attr[8+i] ^ req[4+i]
		}

		got, _ := netip.AddrFromSlice(ip)
		if mapped := netip.AddrPortFrom(got, port); mapped != src {
			t.Errorf("XOR-MAPPED-ADDRESS = %s, want %s", mapped, src)
		}

		rec.None(10 * time.Millisecond)
	})
}

func TestTCPAddsContentLength(t *testing.T) {
	tr, _ := siptest.NewTransport(t, transport.Config{})
	local := siptest.Listen(t, tr, lo)
	u := siptest.NewSocket(t, netip.AddrPortFrom(lo, 0))

	req := siptest.NewRequest("MESSAGE", "sip:a@127.0.0.1", sip.TCP, local)
	req.Header.Del("Content-Length")
	req.Body = []byte("hello")
	req.Header.Set("Content-Type", "text/plain")
	req.Flow = sip.Flow{Transport: sip.TCP, Local: local, Remote: u.Addr()}

	if err := tr.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	in, _ := u.RecvRequest()
	if string(in.Body) != "hello" || in.Header.Get("Content-Length") != "5" {
		t.Errorf("got body %q, Content-Length %q", in.Body, in.Header.Get("Content-Length"))
	}
}

func TestTCPFramingErrorCloses(t *testing.T) {
	tr, _ := siptest.NewTransport(t, transport.Config{})
	local := siptest.Listen(t, tr, lo)
	c := dialTCP(t, local)

	msg := strings.Replace(options("SIP/2.0/TCP 127.0.0.1:1;branch=z9hG4bK1"), "Content-Length: 0\r\n", "", 1)
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("connection still open: %v", err)
	}
}

func TestParseErrorDelivered(t *testing.T) {
	for _, tc := range []struct {
		tr   sip.Transport
		send func(t *testing.T, local netip.AddrPort, data string)
	}{
		{sip.UDP, sendUDP},
		{sip.TCP, func(t *testing.T, local netip.AddrPort, data string) {
			if _, err := dialTCP(t, local).Write([]byte(data)); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(string(tc.tr), func(t *testing.T) {
			tr, rec := siptest.NewTransport(t, transport.Config{})
			local := siptest.Listen(t, tr, lo)

			bad := strings.Replace(options("SIP/2.0/"+string(tc.tr)+" 10.9.9.9:1;branch=z9hG4bK1"), "Max-Forwards: 70", "Max-Forwards 70", 1)
			tc.send(t, local, bad)
			tc.send(t, local, "garbage\r\n\r\n")

			perr := rec.NextParseError()
			if perr.Request == nil || perr.Request.Flow.Transport != tc.tr || perr.Request.Flow.Local != local {
				t.Fatalf("parse error %v without the request and its flow", perr)
			}

			if v := topVia(t, perr.Request); v.Received() != "127.0.0.1" {
				t.Errorf("Via = %s", v)
			}

			rec.None(50 * time.Millisecond)
		})
	}
}

func TestDropsUnanswerable(t *testing.T) {
	tr, rec := siptest.NewTransport(t, transport.Config{})
	local := siptest.Listen(t, tr, lo)

	sendUDP(t, local, strings.Replace(options("x"), "Via: x\r\n", "", 1))
	sendUDP(t, local, "SIP/2.0 200 OK\r\nVia: SIP/2.0/UDP 127.0.0.1;branch=z9hG4bK1\r\nContent-Length: 0\r\n\r\n")
	sendUDP(t, local, "SIP/2.0 200 OK\r\nVia: SIP/2.0/UDP 127.0.0.1;branch=z9hG4bK1\r\nFrom: <sip:a@b>;tag=1\r\n"+
		"To: <sip:a@b>\r\nCall-ID: c1\r\nCSeq: 1 OPTIONS\r\nContent-Length: 0\r\n\r\n")

	if got := rec.NextResponse(); got.StatusCode != 200 {
		t.Errorf("got %s", got.StartLine())
	}

	rec.None(50 * time.Millisecond)
}

func TestIdleTimeout(t *testing.T) {
	tr, _ := siptest.NewTransport(t, transport.Config{IdleTimeout: 200 * time.Millisecond})
	local := siptest.Listen(t, tr, lo)
	c := dialTCP(t, local)
	r := bufio.NewReader(c)

	for range 4 {
		time.Sleep(100 * time.Millisecond)

		if _, err := c.Write([]byte("\r\n\r\n")); err != nil {
			t.Fatal(err)
		}

		if _, err := r.Discard(2); err != nil {
			t.Fatalf("closed while active: %v", err)
		}
	}

	start := time.Now()

	if _, err := r.ReadByte(); !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("read: %v, want a reset", err)
	}

	if d := time.Since(start); d < 150*time.Millisecond || d > 2*time.Second {
		t.Errorf("closed after %v of inactivity", d)
	}
}

func TestListenErrors(t *testing.T) {
	tr, _ := siptest.NewTransport(t, transport.Config{})
	ctx := context.Background()

	for _, a := range []string{"0.0.0.0:0", "[::]:0"} {
		if _, err := tr.Listen(ctx, netip.MustParseAddrPort(a)); err == nil {
			t.Errorf("listened on %s", a)
		}
	}

	local := siptest.Listen(t, tr, lo)
	if _, err := tr.Listen(ctx, local); err == nil {
		t.Error("listened twice on the same address")
	}

	if _, err := tr.Listen(ctx, netip.MustParseAddrPort("[::ffff:127.0.0.1]:0")); err != nil {
		t.Errorf("IPv4-mapped address: %v", err)
	}
}

func TestSendErrors(t *testing.T) {
	tr, _ := siptest.NewTransport(t, transport.Config{})
	local := siptest.Listen(t, tr, lo)
	ctx := context.Background()
	remote := netip.MustParseAddrPort("127.0.0.1:9")

	other := netip.AddrPortFrom(lo, 1)
	for _, tc := range []struct {
		name string
		flow sip.Flow
		err  error
	}{
		{"no listener", sip.Flow{Transport: sip.UDP, Local: other, Remote: remote}, transport.ErrNoListener},
		{"unsupported transport", sip.Flow{Transport: "SCTP", Local: local, Remote: remote}, transport.ErrUnsupportedTransport},
		{"no remote", sip.Flow{Transport: sip.UDP, Local: local}, nil},
	} {
		req := siptest.NewRequest("OPTIONS", "sip:a@127.0.0.1", sip.UDP, local)
		req.Flow = tc.flow

		err := tr.Send(ctx, req)
		if err == nil || (tc.err != nil && !errors.Is(err, tc.err)) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.err)
		}
	}

	res := sip.NewResponse(siptest.NewRequest("OPTIONS", "sip:a@b", sip.UDP, netip.AddrPort{}), 200, "")
	res.Header.Set("Via", "SIP/2.0/UDP host.example.com;branch=z9hG4bK1")
	res.Flow = sip.Flow{Transport: sip.UDP, Local: local}

	if err := tr.Send(ctx, res); err == nil {
		t.Error("sent a response to a host name")
	}

	_ = tr.Close()

	req := siptest.NewRequest("OPTIONS", "sip:a@127.0.0.1", sip.UDP, local)
	req.Flow = sip.Flow{Transport: sip.UDP, Local: local, Remote: remote}

	if err := tr.Send(ctx, req); !errors.Is(err, transport.ErrClosed) {
		t.Errorf("after Close: err = %v", err)
	}

	if _, err := tr.Listen(ctx, netip.AddrPortFrom(lo, 0)); !errors.Is(err, transport.ErrClosed) {
		t.Errorf("Listen after Close: err = %v", err)
	}
}

func TestCloseEndsConnections(t *testing.T) {
	tr, _ := siptest.NewTransport(t, transport.Config{})
	local := siptest.Listen(t, tr, lo)
	c := dialTCP(t, local)

	if _, err := c.Write([]byte("\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
		t.Fatal(err)
	}

	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("connection still open after Close: %v", err)
	}

	if err := tr.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestOrderPerFlow(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		for _, trp := range []sip.Transport{sip.UDP, sip.TCP} {
			t.Run(string(trp), func(t *testing.T) {
				p, u := newPCSCF(t, addr), newUE(t, addr)
				inv := siptest.NewRequest("INVITE", "sip:callee@ims.example.com", trp, u.us.Addr())

				const n = 100

				var stream []byte

				for i := range n {
					res := sip.NewResponse(inv, 180+i%2*20, "")
					res.Header.Add("X-Seq", strconv.Itoa(i))

					if trp == sip.UDP {
						u.uc.Send(trp, p.pc, res)
					} else {
						stream = append(stream, res.Bytes()...)
					}
				}

				if trp == sip.TCP {
					u.uc.SendRaw(trp, p.pc, stream)
				}

				for i := range n {
					got := p.rec.NextResponse()
					if got.Header.Get("X-Seq") != strconv.Itoa(i) {
						t.Fatalf("message %d out of order", i)
					}
				}
			})
		}
	})
}

func TestDialHook(t *testing.T) {
	dialed := make(chan string, 1)

	tr, _ := siptest.NewTransport(t, transport.Config{
		Dial: func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error) {
			dialed <- address
			return d.DialContext(ctx, network, address)
		},
	})
	local := siptest.Listen(t, tr, lo)
	peer := siptest.NewSocket(t, netip.AddrPortFrom(lo, 0))

	req := siptest.NewRequest("OPTIONS", "sip:"+peer.Addr().String(), sip.TCP, local)
	req.Flow = sip.Flow{Transport: sip.TCP, Local: local, Remote: peer.Addr()}

	if err := tr.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	if got := <-dialed; got != peer.Addr().String() {
		t.Fatalf("dialed %s, want %s", got, peer.Addr())
	}

	if got, _ := peer.RecvRequest(); got.Method != "OPTIONS" {
		t.Fatalf("got %s", got.Method)
	}
}

// Control runs on the UDP and TCP sockets the transport listens on, and on those it dials, with their local address.
func TestControlHook(t *testing.T) {
	type call struct{ network, address string }

	calls := make(chan call, 8)

	tr, _ := siptest.NewTransport(t, transport.Config{
		Control: func(network, address string, c syscall.RawConn) error {
			calls <- call{network, address}
			return nil
		},
	})

	local := siptest.Listen(t, tr, lo)

	got := map[string]string{}

	for range 2 {
		c := <-calls
		got[c.network] = c.address
	}

	if got["tcp4"] != netip.AddrPortFrom(lo, 0).String() || got["udp4"] != local.String() {
		t.Fatalf("listening sockets %v, want tcp4 on %s and udp4 on %s", got, netip.AddrPortFrom(lo, 0), local)
	}

	peer := siptest.NewSocket(t, netip.AddrPortFrom(lo, 0))

	req := siptest.NewRequest("OPTIONS", "sip:"+peer.Addr().String(), sip.TCP, local)
	req.Flow = sip.Flow{Transport: sip.TCP, Local: local, Remote: peer.Addr()}

	if err := tr.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	if c := <-calls; c.network != "tcp4" || c.address != local.String() {
		t.Fatalf("dialled socket %v, want tcp4 from %s", c, local)
	}

	if got, _ := peer.RecvRequest(); got.Method != "OPTIONS" {
		t.Fatalf("got %s", got.Method)
	}
}

func TestDialCrossesPeerDial(t *testing.T) {
	l, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	peer := netip.MustParseAddrPort(l.Addr().String())
	_ = l.Close()

	peerConn := make(chan net.Conn, 1)

	tr, _ := siptest.NewTransport(t, transport.Config{
		// The peer dials the flow first, and the transport dials it before accepting the peer's connection.
		Dial: func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error) {
			pd := net.Dialer{LocalAddr: net.TCPAddrFromAddrPort(peer)}

			c, err := pd.DialContext(ctx, network, d.LocalAddr.String())
			if err != nil {
				return nil, err
			}

			peerConn <- c

			return d.DialContext(ctx, network, address)
		},
	})
	local := siptest.Listen(t, tr, lo)

	req := siptest.NewRequest("OPTIONS", "sip:"+peer.String(), sip.TCP, local)
	req.Flow = sip.Flow{Transport: sip.TCP, Local: local, Remote: peer}

	if err := tr.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	c := <-peerConn

	t.Cleanup(func() { _ = c.Close() })

	_ = c.SetDeadline(time.Now().Add(siptest.Timeout))

	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "OPTIONS ") {
		t.Fatalf("peer got %q, %v", line, err)
	}
}

func TestTrace(t *testing.T) {
	type traced struct {
		line   string
		remote netip.AddrPort
		sent   bool
		via    string
	}

	seen := make(chan traced, 2)

	tr, rec := siptest.NewTransport(t, transport.Config{Trace: func(m sip.Message, f sip.Flow, sent bool) {
		seen <- traced{m.StartLine(), f.Remote, sent, m.Env().Header.Get("Via")}
	}})
	local := siptest.Listen(t, tr, lo)
	u := siptest.NewSocket(t, netip.AddrPortFrom(lo, 0))

	req := siptest.NewRequest("OPTIONS", "sip:a@127.0.0.1", sip.UDP, u.Addr())
	via, _ := req.Header.TopVia()
	via.Params.Set("rport", "")
	_ = req.Header.SetTopVia(via)
	wire := req.Header.Get("Via")

	u.Send(sip.UDP, local, req)

	got := rec.NextRequest()
	if err := tr.Send(t.Context(), sip.NewResponse(got, 200, "")); err != nil {
		t.Fatal(err)
	}

	u.RecvResponse()

	if r := <-seen; r.line != "OPTIONS sip:a@127.0.0.1 SIP/2.0" || r.sent || r.remote != u.Addr() || r.via != wire {
		t.Errorf("traced %+v, want the OPTIONS received from %s with Via %q", r, u.Addr(), wire)
	}

	if r := <-seen; r.line != "SIP/2.0 200 OK" || !r.sent || r.remote != u.Addr() {
		t.Errorf("traced %+v, want the 200 sent to %s", r, u.Addr())
	}
}

// Sockets are the UDP socket and the TCP listener of a local address, bound to it.
func TestSockets(t *testing.T) {
	tr, _ := siptest.NewTransport(t, transport.Config{})
	local := siptest.Listen(t, tr, lo)

	rcs, err := tr.Sockets(local)
	if err != nil || len(rcs) != 2 {
		t.Fatalf("Sockets = %v, %v", rcs, err)
	}

	for i, want := range []int{syscall.SOCK_DGRAM, syscall.SOCK_STREAM} {
		var (
			typ  int
			port int
		)

		if err := rcs[i].Control(func(fd uintptr) {
			typ, _ = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_TYPE)

			if sa, err := syscall.Getsockname(int(fd)); err == nil {
				port = sa.(*syscall.SockaddrInet4).Port
			}
		}); err != nil {
			t.Fatal(err)
		}

		if typ != want || port != int(local.Port()) {
			t.Errorf("socket %d: type %d, port %d, want type %d on %d", i, typ, port, want, local.Port())
		}
	}

	if _, err := tr.Sockets(netip.AddrPortFrom(lo, 9)); err == nil {
		t.Error("Sockets of an address not listened on")
	}
}

// RFC 5626 §4.4.2: a STUN Binding request on UDP, answered by the other end's response; none from a socket that does
// not answer.
func TestPingUDP(t *testing.T) {
	a, _ := siptest.NewTransport(t, transport.Config{})
	b, _ := siptest.NewTransport(t, transport.Config{})
	la, lb := siptest.Listen(t, a, lo), siptest.Listen(t, b, lo)

	ctx, cancel := context.WithTimeout(context.Background(), siptest.Timeout)
	defer cancel()

	if err := a.Ping(ctx, sip.Flow{Transport: sip.UDP, Local: la, Remote: lb}); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	mute := siptest.NewSocket(t, netip.AddrPortFrom(lo, 0))
	mute.MuteSTUN(true)

	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := a.Ping(short, sip.Flow{Transport: sip.UDP, Local: la, Remote: mute.Addr()}); !errors.Is(err, transport.ErrNoPong) {
		t.Fatalf("Ping of a mute socket: %v", err)
	}
}

// RFC 5626 §4.4.1: a double CRLF on the TCP connection of the flow, answered by a CRLF; no connection, no ping.
func TestPingTCP(t *testing.T) {
	a, _ := siptest.NewTransport(t, transport.Config{})
	b, _ := siptest.NewTransport(t, transport.Config{})
	la, lb := siptest.Listen(t, a, lo), siptest.Listen(t, b, lo)

	f := sip.Flow{Transport: sip.TCP, Local: la, Remote: lb}

	ctx, cancel := context.WithTimeout(context.Background(), siptest.Timeout)
	defer cancel()

	if err := a.Ping(ctx, f); err == nil {
		t.Fatal("Ping without a connection succeeded")
	}

	req := siptest.NewRequest("OPTIONS", "sip:"+lb.String(), sip.TCP, la)
	req.Flow = f

	if err := a.Send(ctx, req); err != nil {
		t.Fatal(err)
	}

	if err := a.Ping(ctx, f); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.String()
}

type listenerFailure struct {
	local netip.AddrPort
	err   error
}

// failures has a transport report its listener failures on the channel it returns.
func failures(cfg *transport.Config) <-chan listenerFailure {
	ch := make(chan listenerFailure, 4)
	cfg.OnListenerFailed = func(local netip.AddrPort, err error) { ch <- listenerFailure{local, err} }

	return ch
}

func awaitFailure(t *testing.T, ch <-chan listenerFailure) listenerFailure {
	t.Helper()

	select {
	case f := <-ch:
		return f
	case <-time.After(siptest.Timeout):
		t.Fatal("no listener failure reported")
	}

	return listenerFailure{}
}

// A listening socket that stopped listening, as one Android destroyed with its network (accept fails with EINVAL),
// never recovers: its listener is closed, forgotten and reported, rather than accept retried; sends from it fail, and
// its address can be listened on again.
func TestBrokenListenerIsDropped(t *testing.T) {
	var logs syncBuffer

	cfg := transport.Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	failed := failures(&cfg)
	tr, _ := siptest.NewTransport(t, cfg)
	local := siptest.Listen(t, tr, lo)

	rcs, err := tr.Sockets(local)
	if err != nil {
		t.Fatal(err)
	}

	// Linux: a listening socket shut down is no longer listening; accept fails with EINVAL.
	var shut error
	if err := rcs[1].Control(func(fd uintptr) { shut = syscall.Shutdown(int(fd), syscall.SHUT_RD) }); err != nil || shut != nil {
		t.Fatalf("shutdown: %v, %v", err, shut)
	}

	if f := awaitFailure(t, failed); f.local != local || !errors.Is(f.err, syscall.EINVAL) {
		t.Fatalf("failure of %s: %v; want %s, EINVAL", f.local, f.err, local)
	}

	ctx := context.Background()
	req := siptest.NewRequest("OPTIONS", "sip:a@127.0.0.1", sip.UDP, local)
	req.Flow = sip.Flow{Transport: sip.UDP, Local: local, Remote: netip.MustParseAddrPort("127.0.0.1:9")}

	if err := tr.Send(ctx, req); !errors.Is(err, transport.ErrNoListener) {
		t.Fatalf("Send from the broken listener: err = %v, want %v", err, transport.ErrNoListener)
	}

	time.Sleep(200 * time.Millisecond)

	if n := strings.Count(logs.String(), "SIP accept failed"); n != 0 {
		t.Errorf("accept retried %d times on the broken listener", n)
	}

	if n := strings.Count(logs.String(), "SIP listener failed"); n != 1 {
		t.Errorf("listener failure logged %d times, want once", n)
	}

	select {
	case f := <-failed:
		t.Fatalf("failure reported again: %s: %v", f.local, f.err)
	default:
	}

	if _, err := tr.Listen(ctx, local); err != nil {
		t.Fatalf("listen again on %s: %v", local, err)
	}
}

// Closing the transport closes its listeners: no failure.
func TestCloseIsNoListenerFailure(t *testing.T) {
	cfg := transport.Config{}
	failed := failures(&cfg)
	tr, _ := siptest.NewTransport(t, cfg)
	siptest.Listen(t, tr, lo)

	_ = tr.Close()

	select {
	case f := <-failed:
		t.Fatalf("Close reported a failure of %s: %v", f.local, f.err)
	case <-time.After(100 * time.Millisecond):
	}
}

// A port held by another socket is told as such; an address no more local is not (EADDRNOTAVAIL: a network gone).
func TestListenHeldOrNotLocal(t *testing.T) {
	tr, _ := siptest.NewTransport(t, transport.Config{})

	held, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = held.Close() }()

	_, err = tr.Listen(context.Background(), netip.MustParseAddrPort(held.Addr().String()))
	if !errors.Is(err, syscall.EADDRINUSE) || !strings.Contains(err.Error(), "port held by another socket") {
		t.Errorf("held: %v", err)
	}

	// TEST-NET-1 (RFC 5737): no address of the host.
	_, err = tr.Listen(context.Background(), netip.MustParseAddrPort("192.0.2.1:5060"))
	if !errors.Is(err, syscall.EADDRNOTAVAIL) || strings.Contains(err.Error(), "port held") {
		t.Errorf("not local: %v", err)
	}
}

// RFC 3261 §18.4: the end of a connection that a request went out on is told, with that connection, so that its
// requests fail; the transport's own closing is not.
func TestConnectionLost(t *testing.T) {
	type lost struct {
		f   sip.Flow
		c   transport.Conn
		err error
	}

	losses := make(chan lost, 2)
	tr, _ := siptest.NewTransport(t, transport.Config{OnConnectionLost: func(f sip.Flow, c transport.Conn, err error) {
		losses <- lost{f, c, err}
	}})
	local := siptest.Listen(t, tr, lo)
	peer := siptest.NewSocket(t, netip.AddrPortFrom(lo, 0))

	req := siptest.NewRequest("OPTIONS", "sip:a@"+peer.Addr().String(), sip.TCP, local)
	req.Flow = sip.Flow{Transport: sip.TCP, Local: local, Remote: peer.Addr()}

	c, err := tr.SendRequest(context.Background(), req)
	if err != nil || c == 0 {
		t.Fatalf("SendRequest = %d, %v", c, err)
	}

	peer.RecvRequest()

	if !tr.Connected(req.Flow, c) {
		t.Fatal("not connected after sending")
	}

	peer.CloseConn(local)

	select {
	case l := <-losses:
		if l.f != req.Flow || l.c != c || !errors.Is(l.err, transport.ErrConnectionLost) {
			t.Fatalf("lost %v %d %v, want %v %d", l.f, l.c, l.err, req.Flow, c)
		}
	case <-time.After(siptest.Timeout):
		t.Fatal("loss not told")
	}

	if tr.Connected(req.Flow, c) {
		t.Fatal("connected after the loss")
	}

	next, err := tr.SendRequest(context.Background(), req)
	if err != nil || next == c || !tr.Connected(req.Flow, next) {
		t.Fatalf("SendRequest after the loss = %d, %v; want a new connection", next, err)
	}

	peer.RecvRequest()

	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case l := <-losses:
		t.Fatalf("closing the transport told a loss: %v", l.err)
	case <-time.After(50 * time.Millisecond):
	}
}
