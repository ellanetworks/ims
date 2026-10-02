package proxy_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

type scene struct {
	t      *testing.T
	tr     sip.Transport
	r      *router
	caller *siptest.Socket
	callee *siptest.Socket
}

func newScene(t *testing.T, tr sip.Transport, cfg routerConfig) *scene {
	t.Helper()

	return &scene{
		t:      t,
		tr:     tr,
		r:      newRouter(t, loopback, cfg),
		caller: siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
		callee: siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)),
	}
}

func (s *scene) request(method string) *sip.Request {
	req := siptest.NewRequest(method, target(s.callee, s.tr), s.tr, s.caller.Addr())
	req.Header.Set("Contact", "<sip:alice@"+s.caller.Addr().String()+uriParam(s.tr)+">")
	req.Header.Prepend("Route", "<"+s.r.uri()+uriParam(s.tr)+";lr>")

	return req
}

func (s *scene) send(req *sip.Request) {
	s.caller.Send(s.tr, s.r.local, req)
}

func (s *scene) forwarded() (*sip.Request, sip.Flow) {
	s.t.Helper()

	return s.callee.RecvRequest()
}

func TestForwardInvite(t *testing.T) {
	forEachTransport(t, func(t *testing.T, tr sip.Transport) {
		s := newScene(t, tr, routerConfig{opts: proxy.Options{RecordRoute: recordRoute, OnReply: func(r proxy.Reply) proxy.Verdict {
			r.Response.Header.Add("X-Relayed", "yes")
			return proxy.Relay
		}}})

		invite := s.request("INVITE")
		s.send(invite)
		wantResponse(t, s.caller, 100)

		fwd, f := s.forwarded()

		if fwd.URI.String() != target(s.callee, tr) {
			t.Errorf("Request-URI = %s", fwd.URI)
		}

		if fwd.Header.Has("Route") {
			t.Errorf("own Route not removed: %s", fwd.Header.Get("Route"))
		}

		if mf, _ := fwd.Header.MaxForwards(); mf != 69 {
			t.Errorf("Max-Forwards = %d, want 69", mf)
		}

		if got, want := fwd.Header.Values("Record-Route"), "<"+s.r.uri()+uriParam(tr)+";lr>"; len(got) != 1 || got[0] != want {
			t.Errorf("Record-Route = %q, want %s", got, want)
		}

		via, _ := fwd.Header.TopVia()
		if via.Transport != tr || via.SentBy() != s.r.local.String() || viaCount(fwd) != 2 {
			t.Errorf("Via = %q", fwd.Header.Values("Via"))
		}

		if fwd.Header.Get("Content-Length") != "0" || fwd.Header.CallID() != invite.Header.CallID() {
			t.Error("forwarded INVITE lost header fields")
		}

		reply(t, s.callee, fwd, f, 100)
		reply(t, s.callee, fwd, f, 180)

		ringing := wantResponse(t, s.caller, 180)
		if viaCount(ringing) != 1 || ringing.Header.Get("X-Relayed") != "yes" {
			t.Errorf("relayed 180:\n%s", ringing)
		}

		if to, _ := ringing.Header.To(); to.Tag() != "callee" {
			t.Errorf("To tag changed to %q", to.Tag())
		}

		ok := sip.NewResponse(fwd, 200, "")
		_ = ok.Header.SetToTag("callee")
		ok.Header.Add("Contact", "<"+target(s.callee, tr)+">")
		dialog.CopyRecordRoute(ok, fwd)
		s.callee.Send(f.Transport, f.Remote, ok)

		got := wantResponse(t, s.caller, 200)
		if got.Header.Count("Record-Route") != 1 {
			t.Errorf("200 Record-Route = %q", got.Header.Values("Record-Route"))
		}

		s.callee.Send(f.Transport, f.Remote, ok)
		wantResponse(t, s.caller, 200)

		d, err := dialog.NewUAC(invite, got)
		if err != nil {
			t.Fatal(err)
		}

		ack, err := d.NewAck(invite)
		if err != nil {
			t.Fatal(err)
		}

		ack.Header.Prepend("Via", sip.NewVia(tr, s.caller.Addr()).String())
		s.send(ack)

		fack, _ := s.forwarded()
		if fack.Method != "ACK" || fack.Header.Has("Route") || viaCount(fack) != 2 {
			t.Fatalf("forwarded ACK:\n%s", fack)
		}

		s.send(ack)

		again, _ := s.forwarded()
		v1, _ := fack.Header.TopVia()
		v2, _ := again.Header.TopVia()

		if v1.Branch() != v2.Branch() || !strings.HasPrefix(v1.Branch(), sip.MagicCookie) {
			t.Errorf("ACK retransmission branches %s and %s", v1.Branch(), v2.Branch())
		}

		s.callee.RecvNone(quiet)
	})
}

func TestForwardNonInvite(t *testing.T) {
	forEachTransport(t, func(t *testing.T, tr sip.Transport) {
		s := newScene(t, tr, routerConfig{})

		req := s.request("MESSAGE")
		req.Header.Del("Max-Forwards")
		s.send(req)

		fwd, f := s.forwarded()
		if fwd.Header.Has("Record-Route") {
			t.Error("Record-Route added without RecordRoute")
		}

		if mf, err := fwd.Header.MaxForwards(); err != nil || mf != 70 {
			t.Errorf("Max-Forwards = %d, %v; want 70 added", mf, err)
		}

		reply(t, s.callee, fwd, f, 202)
		wantResponse(t, s.caller, 202)

		if n := s.r.p.Pending(); n != 0 {
			t.Errorf("%d pending contexts after the final response", n)
		}

		s.caller.RecvNone(quiet)
	})
}

func TestCheck(t *testing.T) {
	r := newRouter(t, loopback, routerConfig{})

	req := siptest.NewRequest("OPTIONS", "sip:x@127.0.0.1", sip.UDP, netip.MustParseAddrPort("127.0.0.1:5060"))

	if res := r.p.Check(req); res != nil {
		t.Fatalf("valid request rejected with %s", res.StartLine())
	}

	req.Header.Set("Max-Forwards", "0")

	if res := r.p.Check(req); res == nil || res.StatusCode != 483 {
		t.Errorf("Max-Forwards 0: %v", res)
	}

	req.Header.Set("Max-Forwards", "1")
	req.Header.Add("Proxy-Require", "sec-agree, foo")
	req.Header.Add("Proxy-Require", "Sec-Agree,bar")

	res := r.p.Check(req)
	if res == nil || res.StatusCode != 420 || res.Header.Get("Unsupported") != "foo, bar" {
		t.Fatalf("Proxy-Require: %v", res)
	}

	req.Header.Del("Proxy-Require")
	req.Header.Add("Route", "<sip:x;lr")

	if res := r.p.Check(req); res == nil || res.StatusCode != 400 {
		t.Errorf("malformed Route: %v", res)
	}
}

func TestPreprocess(t *testing.T) {
	r := newRouter(t, loopback, routerConfig{})
	self := r.local.String()

	cases := []struct {
		name, uri, to, routes string
		wantURI, wantRoute    string
		removed               int
	}{
		{name: "loose", uri: "sip:b@10.0.0.9", routes: "<sip:" + self + ";lr>, <sip:next;lr>", wantURI: "sip:b@10.0.0.9", wantRoute: "<sip:next;lr>", removed: 1},
		{name: "own Route with a user part", uri: "sip:b@10.0.0.9", routes: "<sip:orig@" + self + ";lr;transport=tcp>", wantURI: "sip:b@10.0.0.9", removed: 1},
		{name: "foreign", uri: "sip:b@10.0.0.9", routes: "<sip:next;lr>", wantURI: "sip:b@10.0.0.9", wantRoute: "<sip:next;lr>"},
		{name: "other port", uri: "sip:b@10.0.0.9", routes: "<sip:127.0.0.1:1;lr>", wantURI: "sip:b@10.0.0.9", wantRoute: "<sip:127.0.0.1:1;lr>"},
		{name: "GRUU", uri: "sip:b@10.0.0.9", routes: "<sip:" + self + ";gr=x;lr>", wantURI: "sip:b@10.0.0.9", wantRoute: "<sip:" + self + ";gr=x;lr>"},
		{name: "strict upstream", uri: "sip:" + self + ";lr", to: ";tag=t", routes: "<sip:next;lr>, <sip:b@10.0.0.9>", wantURI: "sip:b@10.0.0.9", wantRoute: "<sip:next;lr>", removed: 1},
		{name: "strict upstream, two Route fields", uri: "sip:" + self + ";lr", to: ";tag=t", routes: "<sip:next;lr>\x00<sip:b@10.0.0.9>", wantURI: "sip:b@10.0.0.9", wantRoute: "<sip:next;lr>", removed: 1},
		{name: "own Request-URI without a To tag", uri: "sip:" + self, routes: "<sip:next;lr>", wantURI: "sip:" + self, wantRoute: "<sip:next;lr>"},
		{name: "double", uri: "sip:b@10.0.0.9", routes: "<sip:" + self + ";transport=tcp;lr;r2=on>, <sip:" + self + ";lr;r2=on>, <sip:next;lr>", wantURI: "sip:b@10.0.0.9", wantRoute: "<sip:next;lr>", removed: 2},
		{name: "single then own", uri: "sip:b@10.0.0.9", routes: "<sip:" + self + ";lr>, <sip:" + self + ";lr;r2=on>", wantURI: "sip:b@10.0.0.9", wantRoute: "<sip:" + self + ";lr;r2=on>", removed: 1},
		{name: "tel", uri: "tel:+15551234", routes: "<sip:" + self + ";lr>", wantURI: "tel:+15551234", removed: 1},
		{name: "r2 then own without r2", uri: "sip:b@10.0.0.9", routes: "<sip:" + self + ";lr;r2=on>, <sip:" + self + ";lr>", wantURI: "sip:b@10.0.0.9", wantRoute: "<sip:" + self + ";lr>", removed: 1},
		{name: "r2 then foreign r2", uri: "sip:b@10.0.0.9", routes: "<sip:" + self + ";lr;r2=on>, <sip:127.0.0.1:1;lr;r2=on>", wantURI: "sip:b@10.0.0.9", wantRoute: "<sip:127.0.0.1:1;lr;r2=on>", removed: 1},
		{name: "sips", uri: "sip:b@10.0.0.9", routes: "<sips:" + self + ";lr>", wantURI: "sip:b@10.0.0.9", wantRoute: "<sips:" + self + ";lr>"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := siptest.NewRequest("BYE", c.uri, sip.UDP, netip.MustParseAddrPort("127.0.0.1:5999"))
			req.Header.Set("To", req.Header.Get("To")+c.to)

			for v := range strings.SplitSeq(c.routes, "\x00") {
				req.Header.Add("Route", v)
			}

			before := req.String()

			out, removed, err := r.p.Preprocess(req)
			if err != nil {
				t.Fatal(err)
			}

			if out.URI.String() != c.wantURI || strings.Join(out.Header.Values("Route"), ", ") != c.wantRoute || len(removed) != c.removed {
				t.Errorf("got %s, Route %q, removed %v", out.URI, out.Header.Values("Route"), removed)
			}

			if req.String() != before {
				t.Error("Preprocess modified its argument")
			}
		})
	}
}

func TestIsLocalOnPort(t *testing.T) {
	l, _ := siptest.NewLayer(t, transaction.Config{Aliases: []string{"ims.example.org"}})
	a := siptest.ListenLayer(t, l, loopback)
	b := siptest.ListenLayer(t, l, loopback)

	p := proxy.New(proxy.Config{Layer: l, Port: a.Port()})

	cases := []struct {
		uri  string
		want bool
	}{
		{"sip:" + a.String(), true},
		{"sip:ims.example.org:" + strconv.Itoa(int(a.Port())) + ";lr", true},
		{"sip:" + b.String(), false},
		{"sip:ims.example.org:" + strconv.Itoa(int(b.Port())) + ";lr", false},
		{"sip:ims.example.org", false},
	}

	for _, c := range cases {
		u, err := sip.ParseURI(c.uri)
		if err != nil {
			t.Fatal(err)
		}

		if got := p.IsLocal(u); got != c.want {
			t.Errorf("IsLocal(%s) = %t, want %t", c.uri, got, c.want)
		}
	}

	if u, _ := sip.ParseURI("sip:" + b.String()); !proxy.New(proxy.Config{Layer: l, Port: a.Port(), LocalPorts: []uint16{b.Port()}}).IsLocal(u) {
		t.Errorf("IsLocal(%s) on a further local port = false, want true", u)
	}

	if u, _ := sip.ParseURI("sip:" + b.String()); !proxy.New(proxy.Config{Layer: l}).IsLocal(u) {
		t.Errorf("IsLocal(%s) without a port = false, want true", u)
	}
}

func TestDoubleRecordRoute(t *testing.T) {
	s := newScene(t, sip.UDP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}})

	invite := s.request("INVITE")
	invite.URI, _ = sip.ParseURI(target(s.callee, sip.TCP))
	s.send(invite)
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()
	if f.Transport != sip.TCP {
		t.Fatalf("forwarded over %s", f.Transport)
	}

	self := s.r.uri()
	if got := fwd.Header.Values("Record-Route"); strings.Join(got, ", ") != "<"+self+";transport=tcp;lr;r2=on>, <"+self+";lr;r2=on>" {
		t.Fatalf("Record-Route = %q", got)
	}

	ok := sip.NewResponse(fwd, 200, "")
	_ = ok.Header.SetToTag("callee")
	ok.Header.Add("Contact", "<"+target(s.callee, sip.TCP)+">")

	d, err := dialog.NewUAS(fwd, ok)
	if err != nil {
		t.Fatal(err)
	}

	s.callee.Send(f.Transport, f.Remote, ok)

	res := wantResponse(t, s.caller, 200)
	if res.Header.Count("Record-Route") != 2 {
		t.Errorf("200 Record-Route = %q", res.Header.Values("Record-Route"))
	}

	bye, err := d.NewRequest("BYE")
	if err != nil {
		t.Fatal(err)
	}

	bye.Header.Prepend("Via", sip.NewVia(sip.TCP, s.callee.Addr()).String())
	s.callee.Send(sip.TCP, s.r.local, bye)

	got, gf := s.caller.RecvRequest()
	if got.Method != "BYE" || got.Header.Has("Route") || gf.Transport != sip.UDP || got.URI.String() != invite.Header.Get("Contact")[1:len(invite.Header.Get("Contact"))-1] {
		t.Errorf("BYE at the caller over %s:\n%s", gf.Transport, got)
	}
}

func TestStrictNextHop(t *testing.T) {
	s := newScene(t, sip.UDP, routerConfig{})

	req := s.request("OPTIONS")
	req.URI, _ = sip.ParseURI("sip:bob@example.com")
	req.Header.Set("Route", "<"+s.r.uri()+";lr>, <sip:"+s.callee.Addr().String()+";method=INVITE?x=y>, <sip:p3;lr>")
	s.send(req)

	fwd, _ := s.forwarded()
	if fwd.URI.String() != "sip:"+s.callee.Addr().String() || fwd.Header.Get("Route") != "<sip:p3;lr>" || fwd.Header.Values("Route")[1] != "<sip:bob@example.com>" {
		t.Errorf("forwarded to a strict router:\n%s", fwd)
	}
}

func TestServiceUnavailableBecomes500(t *testing.T) {
	forEachTransport(t, func(t *testing.T, tr sip.Transport) {
		s := newScene(t, tr, routerConfig{})

		s.send(s.request("INVITE"))
		wantResponse(t, s.caller, 100)

		fwd, f := s.forwarded()
		reply(t, s.callee, fwd, f, 503, "Retry-After", "30")

		res := wantResponse(t, s.caller, 500)
		if res.Header.Has("Retry-After") {
			t.Error("500 carries the 503's Retry-After")
		}

		if ack, _ := s.forwarded(); ack.Method != "ACK" {
			t.Errorf("got %s, want the hop-by-hop ACK", ack.Method)
		}
	})
}

func TestUnreachableNextHop(t *testing.T) {
	s := newScene(t, sip.UDP, routerConfig{})

	var lc net.ListenConfig

	udp, err := lc.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = udp.Close() }()

	req := s.request("MESSAGE")
	req.URI, _ = sip.ParseURI("sip:bob@" + udp.LocalAddr().String() + ";transport=tcp")
	s.send(req)

	wantResponse(t, s.caller, 500)
}

func TestCancel(t *testing.T) {
	forEachTransport(t, func(t *testing.T, tr sip.Transport) {
		s := newScene(t, tr, routerConfig{})

		invite := s.request("INVITE")
		s.send(invite)
		wantResponse(t, s.caller, 100)

		fwd, f := s.forwarded()
		reply(t, s.callee, fwd, f, 180)
		wantResponse(t, s.caller, 180)

		cancel, _ := sip.NewCancel(invite)
		s.send(cancel)
		wantResponse(t, s.caller, 200)

		fc, cf := s.forwarded()
		if fc.Method != "CANCEL" {
			t.Fatalf("got %s, want CANCEL", fc.Method)
		}

		if v1, _ := fc.Header.TopVia(); v1.Branch() != mustTopVia(t, fwd).Branch() {
			t.Error("CANCEL branch differs from the INVITE's")
		}

		reply(t, s.callee, fc, cf, 200)
		reply(t, s.callee, fwd, f, 487)

		wantResponse(t, s.caller, 487)

		if ack, _ := s.forwarded(); ack.Method != "ACK" {
			t.Errorf("got %s, want the ACK to the 487", ack.Method)
		}
	})
}

func mustTopVia(t *testing.T, m sip.Message) sip.Via {
	t.Helper()

	v, err := m.Env().Header.TopVia()
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func TestCancelBeforeForward(t *testing.T) {
	s := newScene(t, sip.UDP, routerConfig{hold: true})

	invite := s.request("INVITE")
	s.send(invite)
	wantResponse(t, s.caller, 100)

	tx := <-s.r.held

	cancel, _ := sip.NewCancel(invite)
	s.send(cancel)

	codes := map[int]bool{}

	for range 2 {
		res, _ := s.caller.RecvResponse()
		codes[res.StatusCode] = true
	}

	if !codes[200] || !codes[487] {
		t.Fatalf("got %v, want 200 and 487", codes)
	}

	out := s.r.preprocess(tx.Request())

	if err := s.r.p.Forward(tx, out, s.r.target(out, s.r.local), proxy.Options{}); !errors.Is(err, proxy.ErrAnswered) {
		t.Errorf("Forward after CANCEL: %v", err)
	}

	s.callee.RecvNone(quiet)
}

func TestForwardTwice(t *testing.T) {
	s := newScene(t, sip.UDP, routerConfig{hold: true})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	tx := <-s.r.held
	out := s.r.preprocess(tx.Request())

	if err := s.r.p.Forward(tx, out, s.r.target(out, s.r.local), proxy.Options{}); err != nil {
		t.Fatal(err)
	}

	if err := s.r.p.Forward(tx, out, s.r.target(out, s.r.local), proxy.Options{}); !errors.Is(err, proxy.ErrForwarded) {
		t.Errorf("second Forward: %v", err)
	}
}

func TestTimerC(t *testing.T) {
	clock := siptest.NewClock()
	codes := make(chan int, 8)
	s := newScene(t, sip.TCP, routerConfig{clock: clock, onReply: func(_ *transaction.ServerTransaction, _ *sip.Request, r proxy.Reply) proxy.Verdict {
		codes <- r.Response.StatusCode
		return proxy.Relay
	}})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 180)
	wantResponse(t, s.caller, 180)

	clock.Advance(proxy.DefaultTimerC - transaction.DefaultT1)
	reply(t, s.callee, fwd, f, 183)
	wantResponse(t, s.caller, 183)

	clock.Advance(proxy.DefaultTimerC - transaction.DefaultT1)

	if got := drain(codes); len(got) != 2 || got[0] != 180 || got[1] != 183 {
		t.Fatalf("OnReply saw %v before Timer C, want [180 183]", got)
	}

	clock.Advance(transaction.DefaultT1)
	wantResponse(t, s.caller, 408)

	fc, cf := s.forwarded()
	if fc.Method != "CANCEL" {
		t.Fatalf("got %s, want CANCEL", fc.Method)
	}

	reply(t, s.callee, fwd, f, 180)
	reply(t, s.callee, fc, cf, 200)
	reply(t, s.callee, fwd, f, 487)

	if ack, _ := s.forwarded(); ack.Method != "ACK" {
		t.Errorf("got %s, want the ACK to the 487", ack.Method)
	}

	s.caller.RecvNone(quiet)

	if got := drain(codes); len(got) != 1 || got[0] != 408 {
		t.Errorf("OnReply saw %v after Timer C, want [408]", got)
	}
}

func drain(ch chan int) []int {
	var out []int

	for {
		select {
		case v := <-ch:
			out = append(out, v)
		default:
			return out
		}
	}
}

func TestNoReplyOnClose(t *testing.T) {
	codes := make(chan int, 8)
	s := newScene(t, sip.UDP, routerConfig{onReply: func(_ *transaction.ServerTransaction, _ *sip.Request, r proxy.Reply) proxy.Verdict {
		codes <- r.Response.StatusCode
		return proxy.Relay
	}})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)
	s.forwarded()

	if err := s.r.l.Close(); err != nil {
		t.Fatal(err)
	}

	if got := drain(codes); len(got) != 0 {
		t.Errorf("OnReply saw %v on Close", got)
	}
}

func TestDoubleRecordRouteOnSocketChange(t *testing.T) {
	s := newScene(t, sip.UDP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}})
	out := siptest.ListenLayer(t, s.r.l, loopback)
	s.r.out.Store(&out)

	s.send(s.request("MESSAGE"))

	fwd, f := s.forwarded()
	if f.Remote != out {
		t.Errorf("sent from %s, want %s", f.Remote, out)
	}

	want := "<sip:" + out.String() + ";lr;r2=on>, <" + s.r.uri() + ";lr;r2=on>"
	if got := strings.Join(fwd.Header.Values("Record-Route"), ", "); got != want {
		t.Errorf("Record-Route %s, want %s", got, want)
	}
}

func TestForwardErrors(t *testing.T) {
	s := newScene(t, sip.UDP, routerConfig{hold: true})

	s.send(s.request("MESSAGE"))
	tx := <-s.r.held
	out := s.r.preprocess(tx.Request())

	to := s.r.target(out, netip.MustParseAddrPort("127.0.0.1:1"))

	var serr *sip.StatusError
	if err := s.r.p.Forward(tx, out, to, proxy.Options{}); !errors.As(err, &serr) || serr.StatusCode != 500 {
		t.Errorf("Forward from a socket the layer doesn't listen on: %v", err)
	}

	out.Header.Set("Max-Forwards", "0")

	if err := s.r.p.Forward(tx, out, s.r.target(out, s.r.local), proxy.Options{}); !errors.As(err, &serr) || serr.StatusCode != 483 {
		t.Errorf("Forward with Max-Forwards 0: %v", err)
	}

	ack := s.request("ACK")
	ack.Header.Set("Max-Forwards", "0")

	if res := s.r.p.Check(ack); res != nil {
		t.Errorf("Check answered an ACK with %s", res.StartLine())
	}
}

func TestTimerCThen2xx(t *testing.T) {
	clock := siptest.NewClock()
	s := newScene(t, sip.UDP, routerConfig{clock: clock})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 180)
	wantResponse(t, s.caller, 180)

	clock.Advance(proxy.DefaultTimerC)
	wantResponse(t, s.caller, 408)

	if fc, _ := s.forwarded(); fc.Method != "CANCEL" {
		t.Fatalf("got %s, want CANCEL", fc.Method)
	}

	reply(t, s.callee, fwd, f, 200, "Contact", "<"+target(s.callee, sip.UDP)+">")
	wantResponse(t, s.caller, 200)
}

func TestNoResponse(t *testing.T) {
	clock := siptest.NewClock()
	s := newScene(t, sip.TCP, routerConfig{clock: clock})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)
	s.forwarded()

	clock.Advance(64 * transaction.DefaultT1)
	wantResponse(t, s.caller, 408)
}

func TestUpstreamCancelWithoutDownstreamAnswer(t *testing.T) {
	clock := siptest.NewClock()
	s := newScene(t, sip.TCP, routerConfig{clock: clock})

	invite := s.request("INVITE")
	s.send(invite)
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 180)
	wantResponse(t, s.caller, 180)

	cancel, _ := sip.NewCancel(invite)
	s.send(cancel)
	wantResponse(t, s.caller, 200)

	if fc, _ := s.forwarded(); fc.Method != "CANCEL" {
		t.Fatalf("got %s, want CANCEL", fc.Method)
	}

	clock.Advance(64 * transaction.DefaultT1)
	wantResponse(t, s.caller, 487)
}

func TestForcedDoubleRecordRoute(t *testing.T) {
	rr := &proxy.RecordRoute{User: "flow", Double: true, UpstreamParams: sip.Params{{Name: "ue"}}}
	s := newScene(t, sip.UDP, routerConfig{opts: proxy.Options{RecordRoute: rr}})

	s.send(s.request("MESSAGE"))

	fwd, _ := s.forwarded()

	self := "sip:flow@" + s.r.local.String()
	want := "<" + self + ";lr;r2=on>, <" + self + ";lr;r2=on;ue>"

	if got := strings.Join(fwd.Header.Values("Record-Route"), ", "); got != want {
		t.Errorf("Record-Route %s, want %s", got, want)
	}
}
