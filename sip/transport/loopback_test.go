package transport_test

import (
	"context"
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

var loopbacks = []struct {
	name string
	addr netip.Addr
}{
	{"IPv4", netip.MustParseAddr("127.0.0.1")},
	{"IPv6", netip.MustParseAddr("::1")},
}

func forEachFamily(t *testing.T, f func(t *testing.T, addr netip.Addr)) {
	for _, lb := range loopbacks {
		t.Run(lb.name, func(t *testing.T) {
			if lb.addr.Is6() {
				var lc net.ListenConfig

				l, err := lc.ListenPacket(context.Background(), "udp6", "[::1]:0")
				if err != nil {
					t.Skip("no IPv6 loopback")
				}

				_ = l.Close()
			}

			f(t, lb.addr)
		})
	}
}

type pcscf struct {
	tr  *transport.Transport
	rec *siptest.Recorder

	unprot, ps, pc netip.AddrPort
}

func newPCSCF(t *testing.T, addr netip.Addr) *pcscf {
	t.Helper()

	tr, rec := siptest.NewTransport(t, transport.Config{})

	return &pcscf{
		tr:     tr,
		rec:    rec,
		unprot: siptest.Listen(t, tr, addr),
		ps:     siptest.Listen(t, tr, addr),
		pc:     siptest.Listen(t, tr, addr),
	}
}

func (p *pcscf) send(t *testing.T, m sip.Message) {
	t.Helper()

	if err := p.tr.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
}

func (p *pcscf) respond(t *testing.T, req *sip.Request, code int, extra ...string) *sip.Response {
	t.Helper()

	res := sip.NewResponse(req, code, "")
	for i := 0; i+1 < len(extra); i += 2 {
		res.Header.Add(extra[i], extra[i+1])
	}

	p.send(t, res)

	return res
}

func (p *pcscf) request(t *testing.T, method string, f sip.Flow, target netip.AddrPort) *sip.Request {
	t.Helper()

	req := siptest.NewRequest(method, "sip:ue@"+target.String(), f.Transport, f.Local)
	req.Flow = f
	p.send(t, req)

	return req
}

type ue struct {
	unprot, uc, us *siptest.Socket
}

func newUE(t *testing.T, addr netip.Addr) *ue {
	t.Helper()

	return &ue{
		unprot: siptest.NewSocket(t, netip.AddrPortFrom(addr, 0)),
		uc:     siptest.NewSocket(t, netip.AddrPortFrom(addr, 0)),
		us:     siptest.NewSocket(t, netip.AddrPortFrom(addr, 0)),
	}
}

func wantFlow(t *testing.T, what string, got sip.Flow, tr sip.Transport, local, remote netip.AddrPort) {
	t.Helper()

	if want := (sip.Flow{Transport: tr, Local: local, Remote: remote}); got != want {
		t.Fatalf("%s: flow %+v, want %+v", what, got, want)
	}
}

func topVia(t *testing.T, m sip.Message) sip.Via {
	t.Helper()

	v, err := m.Env().Header.TopVia()
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func TestSecAgree(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		for _, tr := range []sip.Transport{sip.UDP, sip.TCP} {
			t.Run(string(tr), func(t *testing.T) {
				p, u := newPCSCF(t, addr), newUE(t, addr)

				reg := siptest.NewRequest("REGISTER", "sip:ims.example.com", sip.UDP, u.unprot.Addr())
				reg.Header.Add("Security-Client", "ipsec-3gpp;alg=hmac-sha-1-96;spi-c=1;spi-s=2;port-c="+port(u.uc)+";port-s="+port(u.us))
				u.unprot.Send(sip.UDP, p.unprot, reg)

				got := p.rec.NextRequest()
				wantFlow(t, "unprotected REGISTER", got.Flow, sip.UDP, p.unprot, u.unprot.Addr())

				if v := topVia(t, got); v.Received() != addr.String() {
					t.Errorf("Via received = %q", v.Received())
				}

				p.respond(t, got, 401, "Security-Server", "ipsec-3gpp;alg=hmac-sha-1-96;port-c="+portOf(p.pc)+";port-s="+portOf(p.ps))

				res, f := u.unprot.RecvResponse()
				wantFlow(t, "401", f, sip.UDP, u.unprot.Addr(), p.unprot)

				if res.StatusCode != 401 || !strings.Contains(res.Header.Get("Security-Server"), "port-s="+portOf(p.ps)) {
					t.Fatalf("got %s", res.StartLine())
				}

				reg = siptest.NewRequest("REGISTER", "sip:ims.example.com", tr, u.us.Addr())
				u.uc.Send(tr, p.ps, reg)

				got = p.rec.NextRequest()
				wantFlow(t, "protected REGISTER", got.Flow, tr, p.ps, u.uc.Addr())

				p.respond(t, got, 200)

				_, f = u.uc.RecvResponse()
				wantFlow(t, "200 to protected REGISTER", f, tr, u.uc.Addr(), p.ps)

				notify := p.request(t, "NOTIFY", sip.Flow{Transport: tr, Local: p.pc, Remote: u.us.Addr()}, u.us.Addr())
				wantFlow(t, "NOTIFY sent", notify.Flow, tr, p.pc, u.us.Addr())

				in, f := u.us.RecvRequest()
				wantFlow(t, "NOTIFY received", f, tr, u.us.Addr(), p.pc)

				u.us.Send(tr, p.pc, sip.NewResponse(in, 200, ""))

				res = p.rec.NextResponse()
				wantFlow(t, "200 to NOTIFY", res.Flow, tr, p.pc, u.us.Addr())
			})
		}
	})
}

func TestResponseFromReceivingSocket(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		p, u := newPCSCF(t, addr), newUE(t, addr)

		p.request(t, "NOTIFY", sip.Flow{Transport: sip.UDP, Local: p.pc, Remote: u.us.Addr()}, u.us.Addr())

		in, _ := u.us.RecvRequest()
		u.uc.Send(sip.UDP, p.pc, sip.NewResponse(in, 200, ""))

		res := p.rec.NextResponse()
		wantFlow(t, "200 from port_uc", res.Flow, sip.UDP, p.pc, u.uc.Addr())

		for range 3 {
			u.uc.Send(sip.UDP, p.ps, siptest.NewRequest("SUBSCRIBE", "sip:ue@ims.example.com", sip.UDP, u.us.Addr()))

			got := p.rec.NextRequest()
			wantFlow(t, "SUBSCRIBE", got.Flow, sip.UDP, p.ps, u.uc.Addr())

			p.respond(t, got, 200)

			_, f := u.uc.RecvResponse()
			wantFlow(t, "200 to SUBSCRIBE", f, sip.UDP, u.uc.Addr(), p.ps)
		}
	})
}

func TestSharedClientPortTCP(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		p := newPCSCF(t, addr)
		ues := []*ue{newUE(t, addr), newUE(t, addr), newUE(t, addr)}

		for round := range 2 {
			for i, u := range ues {
				req := p.request(t, "MESSAGE", sip.Flow{Transport: sip.TCP, Local: p.pc, Remote: u.us.Addr()}, u.us.Addr())

				in, f := u.us.RecvRequest()
				wantFlow(t, "MESSAGE", f, sip.TCP, u.us.Addr(), p.pc)

				if in.Header.CallID() != req.Header.CallID() {
					t.Fatalf("round %d: UE %d got another phone's request", round, i)
				}

				u.us.Send(sip.TCP, p.pc, sip.NewResponse(in, 200, ""))

				res := p.rec.NextResponse()
				wantFlow(t, "200", res.Flow, sip.TCP, p.pc, u.us.Addr())

				if n := u.us.Opened(); n != 1 {
					t.Errorf("round %d: UE %d has %d connections, want 1", round, i, n)
				}
			}
		}
	})
}

func TestTCPFlowReuse(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		p, u := newPCSCF(t, addr), newUE(t, addr)

		u.unprot.Send(sip.TCP, p.unprot, siptest.NewRequest("REGISTER", "sip:ims.example.com", sip.TCP, u.unprot.Addr()))

		reg := p.rec.NextRequest()
		wantFlow(t, "REGISTER", reg.Flow, sip.TCP, p.unprot, u.unprot.Addr())

		p.respond(t, reg, 200)
		u.unprot.RecvResponse()

		p.request(t, "OPTIONS", reg.Flow, u.unprot.Addr())

		_, f := u.unprot.RecvRequest()
		wantFlow(t, "OPTIONS", f, sip.TCP, u.unprot.Addr(), p.unprot)

		if n := u.unprot.Opened(); n != 1 {
			t.Errorf("UE has %d connections, want 1", n)
		}
	})
}

func TestTCPResponseAfterConnectionLoss(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		p, u := newPCSCF(t, addr), newUE(t, addr)

		u.uc.Send(sip.TCP, p.ps, siptest.NewRequest("INVITE", "sip:callee@ims.example.com", sip.TCP, u.us.Addr()))

		inv := p.rec.NextRequest()
		p.tr.CloseFlow(inv.Flow)

		res := p.respond(t, inv, 180)
		wantFlow(t, "180 sent", res.Flow, sip.TCP, p.ps, u.us.Addr())

		_, f := u.us.RecvResponse()
		wantFlow(t, "180 received", f, sip.TCP, u.us.Addr(), p.ps)
	})
}

func TestLargeRequestOverTCP(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		p, u := newPCSCF(t, addr), newUE(t, addr)

		req := siptest.NewRequest("MESSAGE", "sip:ue@"+u.us.Addr().String(), sip.UDP, p.pc)
		req.SetBody("text/plain", []byte(strings.Repeat("x", transport.MaxUDPRequest)))
		req.Flow = sip.Flow{Transport: sip.UDP, Local: p.pc, Remote: u.us.Addr()}
		p.send(t, req)

		wantFlow(t, "large request sent", req.Flow, sip.TCP, p.pc, u.us.Addr())

		in, f := u.us.RecvRequest()
		wantFlow(t, "large request received", f, sip.TCP, u.us.Addr(), p.pc)

		if v := topVia(t, in); v.Transport != sip.TCP || v.Branch() != topVia(t, req).Branch() {
			t.Errorf("Via = %s", v)
		}

		small := siptest.NewRequest("MESSAGE", "sip:ue@"+u.us.Addr().String(), sip.UDP, p.pc)
		small.SetBody("text/plain", []byte("hi"))
		small.Flow = sip.Flow{Transport: sip.UDP, Local: p.pc, Remote: u.us.Addr()}
		p.send(t, small)

		_, f = u.us.RecvRequest()
		wantFlow(t, "small request", f, sip.UDP, u.us.Addr(), p.pc)
	})
}

func TestLargeRequestFallsBackToUDP(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		p := newPCSCF(t, addr)

		pc, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(addr, 0)))
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = pc.Close() }()

		dst := pc.LocalAddr().(*net.UDPAddr).AddrPort()

		req := siptest.NewRequest("MESSAGE", "sip:ue@"+dst.String(), sip.UDP, p.pc)
		req.SetBody("text/plain", []byte(strings.Repeat("x", 2000)))
		req.Flow = sip.Flow{Transport: sip.UDP, Local: p.pc, Remote: dst}
		want := req.String()

		p.send(t, req)

		if req.Flow.Transport != sip.UDP || req.String() != want {
			t.Errorf("request changed after fallback: flow %+v", req.Flow)
		}

		buf := make([]byte, 65535)

		n, _, err := pc.ReadFromUDPAddrPort(buf)
		if err != nil {
			t.Fatal(err)
		}

		if string(buf[:n]) != want {
			t.Error("datagram differs from the request")
		}
	})
}

// RFC 3261 §18.1.1
func TestLargeRequestFallsBackToUDPWhenTCPHangs(t *testing.T) {
	addr := loopbacks[0].addr
	dialed := make(chan struct{}, 1)

	tr, _ := siptest.NewTransport(t, transport.Config{
		FallbackTimeout: 100 * time.Millisecond,
		Dial: func(ctx context.Context, d *net.Dialer, _, _ string) (net.Conn, error) {
			dialed <- struct{}{}

			select {
			case <-ctx.Done():
			case <-time.After(d.Timeout):
			}

			return nil, context.DeadlineExceeded
		},
	})
	local := siptest.Listen(t, tr, addr)

	pc, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(addr, 0)))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = pc.Close() }()

	dst := pc.LocalAddr().(*net.UDPAddr).AddrPort()

	req := siptest.NewRequest("MESSAGE", "sip:ue@"+dst.String(), sip.UDP, local)
	req.SetBody("text/plain", []byte(strings.Repeat("x", 2000)))
	req.Flow = sip.Flow{Transport: sip.UDP, Local: local, Remote: dst}

	start := time.Now()

	if err := tr.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Send took %s, want the fallback after 100ms", elapsed)
	}

	select {
	case <-dialed:
	default:
		t.Error("no TCP attempt before the fallback")
	}

	if req.Flow.Transport != sip.UDP {
		t.Errorf("flow %+v after the fallback, want UDP", req.Flow)
	}

	_ = pc.SetReadDeadline(time.Now().Add(siptest.Timeout))

	buf := make([]byte, 65535)
	if n, _, err := pc.ReadFromUDPAddrPort(buf); err != nil || n < 2000 {
		t.Fatalf("datagram of %d bytes, %v; want the request", n, err)
	}
}

func TestLargeUDPRequestKept(t *testing.T) {
	addr := loopbacks[0].addr
	dialed := make(chan struct{}, 1)

	tr, _ := siptest.NewTransport(t, transport.Config{
		LargeUDP: true,
		Dial: func(context.Context, *net.Dialer, string, string) (net.Conn, error) {
			dialed <- struct{}{}
			return nil, context.Canceled
		},
	})
	local := siptest.Listen(t, tr, addr)

	pc, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(addr, 0)))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = pc.Close() }()

	dst := pc.LocalAddr().(*net.UDPAddr).AddrPort()

	req := siptest.NewRequest("INVITE", "sip:ue@"+dst.String(), sip.UDP, local)
	req.SetBody("text/plain", []byte(strings.Repeat("x", 2000)))
	req.Flow = sip.Flow{Transport: sip.UDP, Local: local, Remote: dst}

	if err := tr.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	select {
	case <-dialed:
		t.Error("TCP attempted for a large request with LargeUDP")
	default:
	}

	_ = pc.SetReadDeadline(time.Now().Add(siptest.Timeout))

	buf := make([]byte, 65535)
	if n, _, err := pc.ReadFromUDPAddrPort(buf); err != nil || n < 2000 {
		t.Fatalf("datagram of %d bytes, %v; want the request", n, err)
	}
}

func TestLargeUDPResponse(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		p, u := newPCSCF(t, addr), newUE(t, addr)

		u.uc.Send(sip.UDP, p.ps, siptest.NewRequest("REGISTER", "sip:ims.example.com", sip.UDP, u.us.Addr()))

		reg := p.rec.NextRequest()
		res := sip.NewResponse(reg, 200, "")
		res.Header.Add("P-Associated-URI", strings.Repeat("<sip:+15550000000@ims.example.com>, ", 80)+"<tel:+15550000000>")
		p.send(t, res)

		if len(res.Bytes()) < 3000 {
			t.Fatalf("response is only %d bytes", len(res.Bytes()))
		}

		got, f := u.uc.RecvResponse()
		wantFlow(t, "large 200", f, sip.UDP, u.uc.Addr(), p.ps)

		if got.String() != res.String() {
			t.Error("response altered")
		}
	})
}

func port(s *siptest.Socket) string { return portOf(s.Addr()) }

func portOf(a netip.AddrPort) string { return strconv.Itoa(int(a.Port())) }

func TestDialSurvivesCancelledCaller(t *testing.T) {
	p, u := newPCSCF(t, loopbacks[0].addr), newUE(t, loopbacks[0].addr)
	f := sip.Flow{Transport: sip.TCP, Local: p.pc, Remote: u.us.Addr()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := siptest.NewRequest("OPTIONS", "sip:ue@"+u.us.Addr().String(), sip.TCP, p.pc)
	req.Flow = f
	_ = p.tr.Send(ctx, req)

	p.request(t, "MESSAGE", f, u.us.Addr())

	for {
		in, got := u.us.RecvRequest()
		wantFlow(t, "request", got, sip.TCP, u.us.Addr(), p.pc)

		if in.Method == "MESSAGE" {
			break
		}
	}

	if n := u.us.Opened(); n != 1 {
		t.Errorf("UE has %d connections, want 1", n)
	}
}

func TestProtectedUDPResponseOnFlow(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		p, u := newPCSCF(t, addr), newUE(t, addr)

		u.uc.Send(sip.UDP, p.ps, siptest.NewRequest("REGISTER", "sip:ims.example.com", sip.UDP, u.us.Addr()))

		reg := p.rec.NextRequest()
		if rport, ok := topVia(t, reg).RPort(); !ok || rport != u.uc.Addr().Port() {
			t.Fatalf("rport = %d, %v", rport, ok)
		}

		res := sip.NewResponse(reg, 200, "")
		res.Flow = sip.Flow{Transport: sip.UDP, Local: p.pc, Remote: u.us.Addr()}

		if err := p.tr.SendOnFlow(context.Background(), res); err != nil {
			t.Fatal(err)
		}

		_, f := u.us.RecvResponse()
		wantFlow(t, "200 on the port_pc/port_us pair", f, sip.UDP, u.us.Addr(), p.pc)
		u.uc.RecvNone(50 * time.Millisecond)
	})
}

func TestRedialAfterCloseFlow(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		p, u := newPCSCF(t, addr), newUE(t, addr)
		f := sip.Flow{Transport: sip.TCP, Local: p.pc, Remote: u.us.Addr()}

		for i := range 3 {
			p.request(t, "OPTIONS", f, u.us.Addr())
			u.us.RecvRequest()

			p.tr.CloseFlow(f)

			if n := u.us.Opened(); n != i+1 {
				t.Fatalf("UE has seen %d connections, want %d", n, i+1)
			}
		}
	})
}
