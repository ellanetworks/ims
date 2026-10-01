package transport_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
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

func TestTooLargeOverUDP(t *testing.T) {
	tr, rec := siptest.NewTransport(t, transport.Config{MaxMessageSize: 1000})
	local := siptest.Listen(t, tr, lo)
	u := siptest.NewSocket(t, netip.AddrPortFrom(lo, 0))

	req := siptest.NewRequest("MESSAGE", "sip:a@127.0.0.1", sip.UDP, u.Addr())
	req.SetBody("text/plain", []byte(strings.Repeat("x", 1000)))
	u.Send(sip.UDP, local, req)

	res, _ := u.RecvResponse()
	if res.StatusCode != 513 {
		t.Errorf("got %s, want 513", res.StartLine())
	}

	rec.None(50 * time.Millisecond)
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

	if _, err := r.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("read: %v, want EOF", err)
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
