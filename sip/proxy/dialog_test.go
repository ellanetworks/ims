package proxy_test

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

func sdpsession(b []byte) (*sdp.Session, error) {
	return sdp.Parse(b)
}

func sdpBody(port int) []byte {
	return fmt.Appendf(nil, "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n"+
		"m=audio %d RTP/AVP 96\r\na=rtpmap:96 AMR-WB/16000\r\n", port)
}

func withSDP[M interface{ Env() *sip.Envelope }](m M, port int) M {
	m.Env().SetBody("application/sdp", sdpBody(port))
	return m
}

func answer(t *testing.T, s *siptest.Socket, req *sip.Request, f sip.Flow, code int, extra ...string) *sip.Response {
	t.Helper()

	res := sip.NewResponse(req, code, "")

	if code > 100 {
		if to, _ := req.Header.To(); to.Tag() == "" {
			_ = res.Header.SetToTag("callee")
		}
	}

	if code > 100 && code < 300 {
		dialog.CopyRecordRoute(res, req)
		res.Header.Add("Contact", "<"+target(s, f.Transport)+">")
	}

	if code >= 200 && code < 300 && req.Method == "INVITE" && len(req.Body) > 0 {
		withSDP(res, 5000)
	}

	for i := 0; i+1 < len(extra); i += 2 {
		res.Header.Add(extra[i], extra[i+1])
	}

	s.Send(f.Transport, f.Remote, res)

	return res
}

func sendFrom(t *testing.T, s *siptest.Socket, tr sip.Transport, req *sip.Request) {
	t.Helper()

	req.Header.Prepend("Via", sip.NewVia(tr, s.Addr()).String())

	_, to, err := sip.NextHop(req)
	if err != nil {
		t.Fatal(err)
	}

	s.Send(tr, to, req)
}

func wantRequest(t *testing.T, s *siptest.Socket, method string) (*sip.Request, sip.Flow) {
	t.Helper()

	req, f := s.RecvRequest()
	if req.Method != method {
		t.Fatalf("got %s, want %s:\n%s", req.Method, method, req)
	}

	return req, f
}

func wantCSeq(t *testing.T, req *sip.Request, seq uint32) {
	t.Helper()

	if c, _ := req.Header.CSeq(); c.Seq != seq {
		t.Errorf("%s CSeq %d, want %d", req.Method, c.Seq, seq)
	}
}

func wantState(t *testing.T, d *proxy.Dialog, state proxy.DialogState) {
	t.Helper()

	if got := d.State(); got != state {
		t.Fatalf("dialog state %s, want %s", got, state)
	}
}

type call struct {
	s      *scene
	invite *sip.Request
	fwd    *sip.Request
	f      sip.Flow
	d      *proxy.Dialog
	caller *dialog.Dialog
	callee *dialog.Dialog
}

func ring(s *scene) *call {
	s.t.Helper()

	c := &call{s: s, invite: withSDP(s.request("INVITE"), 4000)}
	s.send(c.invite)
	wantResponse(s.t, s.caller, 100)

	c.d = s.r.nextDialog()
	s.r.nextEvent(proxy.EventStarted)
	c.fwd, c.f = s.forwarded()

	return c
}

func (c *call) answerCall(noAck bool, extra ...string) {
	t := c.s.t
	t.Helper()

	ok := answer(t, c.s.callee, c.fwd, c.f, 200, extra...)
	res := wantResponse(t, c.s.caller, 200)

	var err error

	if c.caller, err = dialog.NewUAC(c.invite, res); err != nil {
		t.Fatal(err)
	}

	if c.callee, err = dialog.NewUAS(c.fwd, ok); err != nil {
		t.Fatal(err)
	}

	if e := c.s.r.nextEvent(proxy.EventAnswered); e.Code != 200 || e.Dialog != c.d {
		t.Errorf("answered event %+v", e)
	}

	if noAck {
		return
	}

	ack, err := c.caller.NewAck(c.invite)
	if err != nil {
		t.Fatal(err)
	}

	sendFrom(t, c.s.caller, c.s.tr, ack)
	wantRequest(t, c.s.callee, "ACK")
	wantState(t, c.d, proxy.Confirmed)
}

func (c *call) request(from proxy.Side, method string, port int) (*sip.Request, *sip.Request, sip.Flow) {
	t := c.s.t
	t.Helper()

	d, sock, peer := c.caller, c.s.caller, c.s.callee
	if from == proxy.Callee {
		d, sock, peer = c.callee, c.s.callee, c.s.caller
	}

	req, err := d.NewRequest(method)
	if err != nil {
		t.Fatal(err)
	}

	contact := "<sip:alice@" + sock.Addr().String() + uriParam(c.s.tr) + ">"
	if from == proxy.Callee {
		contact = "<" + target(sock, c.s.tr) + ">"
	}

	if method == "INVITE" || method == "UPDATE" {
		req.Header.Add("Contact", contact)
	}

	if port > 0 {
		withSDP(req, port)
	}

	sendFrom(t, sock, c.s.tr, req)

	if method == "INVITE" {
		wantResponse(t, sock, 100)
	}

	got, f := wantRequest(t, peer, method)

	return req, got, f
}

func TestDialogCallThroughTwoProxies(t *testing.T) {
	forEachFamily(t, func(t *testing.T, addr netip.Addr) {
		forEachTransport(t, func(t *testing.T, tr sip.Transport) {
			p2 := newRouter(t, addr, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})
			p1 := newRouter(t, addr, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true, next: p2.uri() + uriParam(tr) + ";lr"})
			alice := newUA(t, addr, tr)
			bob := newUA(t, addr, tr)

			invite := withSDP(siptest.NewRequest("INVITE", "sip:bob@"+bob.local.String()+uriParam(tr), tr, alice.local), 4000)
			invite.Header.Prepend("Route", "<"+p1.uri()+uriParam(tr)+";lr>")
			invite.Header.Set("Contact", alice.contact())
			invite.Header.Add("Supported", "100rel")
			invite.Header.Del("Via")
			calls := alice.send(invite)

			d1, d2 := p1.nextDialog(), p2.nextDialog()
			p1.nextEvent(proxy.EventStarted)
			p2.nextEvent(proxy.EventStarted)

			if d1.ID() == d2.ID() || d1.CallID() != d2.CallID() {
				t.Fatalf("dialogs %s and %s for Call-IDs %s and %s", d1.ID(), d2.ID(), d1.CallID(), d2.CallID())
			}

			in := bob.tu.NextRequest()

			progress := withSDP(bob.respond(in, 183, "Require", "100rel", "RSeq", "1", "Contact", bob.contact()), 5000)

			bd, err := dialog.NewUAS(in.Req, progress)
			if err != nil {
				t.Fatal(err)
			}

			_ = in.Tx.Respond(progress)

			early := next(t, calls, 183)
			for _, d := range []*proxy.Dialog{d1, d2} {
				wantState(t, d, proxy.Early)

				if offer, ans, ok := d.Session(); !ok || offer.From != proxy.Caller || ans.From != proxy.Callee || !strings.Contains(string(ans.Data), "5000") {
					t.Errorf("session after the 183: %+v, %+v, %v", offer, ans, ok)
				}
			}

			ad, err := dialog.NewUAC(invite, early)
			if err != nil {
				t.Fatal(err)
			}

			prack, _ := ad.NewPrack(early)
			pracks := alice.send(prack)

			pr := bob.tu.NextRequest()
			_ = bd.ReceiveRequest(pr.Req)
			_ = pr.Tx.Respond(bob.respond(pr, 200))

			next(t, pracks, 200)

			update, _ := ad.NewRequest("UPDATE")
			update.Header.Add("Contact", alice.contact())
			updates := alice.send(withSDP(update, 4002))

			up := bob.tu.NextRequest()
			_ = bd.ReceiveRequest(up.Req)
			_ = up.Tx.Respond(withSDP(bob.respond(up, 200, "Contact", bob.contact()), 5002))
			_ = ad.ReceiveResponse(next(t, updates, 200))

			ringing := bob.respond(in, 180, "Contact", bob.contact())
			bd.PrepareResponse(in.Req, ringing)
			_ = in.Tx.Respond(ringing)

			next(t, calls, 180)

			ok := bob.respond(in, 200, "Contact", bob.contact(), "Session-Expires", "1800;refresher=uac")
			bd.PrepareResponse(in.Req, ok)
			_ = in.Tx.Respond(ok)

			_ = ad.ReceiveResponse(next(t, calls, 200))

			for _, p := range []*router{p1, p2} {
				p.nextEvent(proxy.EventAnswered)
			}

			ack, _ := ad.NewAck(invite)
			alice.route(ack)
			_ = alice.l.SendAck(t.Context(), ack)

			bob.tu.NextAck()

			update1, _ := update.Header.CSeq()

			for _, d := range []*proxy.Dialog{d1, d2} {
				wantState(t, d, proxy.Confirmed)

				if offer, ans, ok := d.Session(); !ok || !strings.Contains(string(offer.Data), "4002") || !strings.Contains(string(ans.Data), "5002") {
					t.Errorf("session after the UPDATE: %s, %s, %v", offer.Data, ans.Data, ok)
				}

				if seq, _ := d.Seq(proxy.Caller); seq != update1.Seq {
					t.Errorf("caller CSeq %d, want %d", seq, update1.Seq)
				}

				if got := d.Contact(proxy.Callee); got.Port != bob.local.Port() {
					t.Errorf("callee contact %s", got)
				}
			}

			routes := func(d *proxy.Dialog, s proxy.Side) string {
				var out []string
				for _, a := range d.Routes(s) {
					out = append(out, a.URI.Host+":"+fmt.Sprint(a.URI.Port))
				}

				return strings.Join(out, ",")
			}

			hostport := func(r *router) string {
				return sip.FormatHost(r.local.Addr()) + ":" + fmt.Sprint(r.local.Port())
			}

			if got := routes(d1, proxy.Callee); got != hostport(p2) {
				t.Errorf("p1 route toward the callee %q, want %q", got, hostport(p2))
			}

			if got := routes(d2, proxy.Caller); got != hostport(p1) {
				t.Errorf("p2 route toward the caller %q, want %q", got, hostport(p1))
			}

			if routes(d1, proxy.Caller) != "" || routes(d2, proxy.Callee) != "" {
				t.Errorf("routes beyond the parties: %q, %q", routes(d1, proxy.Caller), routes(d2, proxy.Callee))
			}

			bye, _ := ad.NewRequest("BYE")
			byes := alice.send(bye)

			hangup := bob.tu.NextRequest()
			_ = hangup.Tx.Respond(bob.respond(hangup, 200))

			next(t, byes, 200)

			for _, p := range []*router{p1, p2} {
				if e := p.nextEvent(proxy.EventEnded); e.End != proxy.EndBye || e.By != proxy.Caller || e.Code != 200 {
					t.Errorf("ended event %+v", e)
				}
			}

			wantState(t, d1, proxy.Ended)
			wantState(t, d2, proxy.Ended)
		})
	})
}

func TestDialogLegsShareCallID(t *testing.T) {
	forEachTransport(t, func(t *testing.T, tr sip.Transport) {
		p := newRouter(t, loopback, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})
		alice := newUA(t, loopback, tr)
		bob := newUA(t, loopback, tr)

		invite := siptest.NewRequest("INVITE", "sip:bob@"+bob.local.String()+uriParam(tr), tr, alice.local)
		route := "<" + p.uri() + uriParam(tr) + ";lr>"
		invite.Header.Prepend("Route", route+", "+route)
		invite.Header.Set("Contact", alice.contact())
		invite.Header.Del("Via")
		calls := alice.send(invite)

		first, second := p.nextDialog(), p.nextDialog()

		in := bob.tu.NextRequest()
		if rrs, _ := in.Req.Header.RecordRoutes(); len(rrs) != 2 {
			t.Fatalf("Record-Route %q", in.Req.Header.Values("Record-Route"))
		}

		ok := bob.respond(in, 200, "Contact", bob.contact())

		bd, err := dialog.NewUAS(in.Req, ok)
		if err != nil {
			t.Fatal(err)
		}

		_ = in.Tx.Respond(ok)

		ad, err := dialog.NewUAC(invite, next(t, calls, 200))
		if err != nil {
			t.Fatal(err)
		}

		ack, _ := ad.NewAck(invite)
		alice.route(ack)
		_ = alice.l.SendAck(t.Context(), ack)

		bob.tu.NextAck()

		for _, d := range []*proxy.Dialog{first, second} {
			wantState(t, d, proxy.Confirmed)
		}

		if first.ID() == second.ID() || first.CallID() != second.CallID() {
			t.Fatalf("dialogs %s and %s", first.ID(), second.ID())
		}

		bye, _ := bd.NewRequest("BYE")
		byes := bob.send(bye)

		hangup := alice.tu.NextRequest()
		_ = hangup.Tx.Respond(alice.respond(hangup, 200))

		next(t, byes, 200)

		ended := map[*proxy.Dialog]bool{}

		for len(ended) < 2 {
			select {
			case e := <-p.events:
				if e.Kind != proxy.EventEnded {
					continue
				}

				if e.By != proxy.Callee || e.End != proxy.EndBye {
					t.Errorf("ended event %+v", e)
				}

				ended[e.Dialog] = true
			case <-time.After(siptest.Timeout):
				t.Fatalf("ended %v, want both legs", ended)
			}
		}

		if !ended[first] || !ended[second] {
			t.Errorf("ended %v, want both legs", ended)
		}
	})
}

func TestDialogAckNeverArrives(t *testing.T) {
	clock := siptest.NewClock()
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true, clock: clock})

	c := ring(s)
	c.answerCall(true)
	wantState(t, c.d, proxy.Answered)

	clock.Advance(2*64*transaction.DefaultT1 - time.Second)
	s.r.noEvent()

	clock.Advance(time.Second)

	if e := s.r.nextEvent(proxy.EventEnded); e.End != proxy.EndNoAck || e.By != 0 {
		t.Errorf("ended event %+v", e)
	}

	s.caller.RecvNone(quiet)
	s.callee.RecvNone(quiet)

	bye, _ := c.callee.NewRequest("BYE")
	sendFrom(t, s.callee, s.tr, bye)
	wantRequest(t, s.caller, "BYE")
}

func TestDialogReinviteGlare(t *testing.T) {
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

	c := ring(s)
	c.answerCall(false)

	before := c.d.Contact(proxy.Callee)
	offer, ans, _ := c.d.Session()

	_, fromCaller, f1 := c.request(proxy.Caller, "INVITE", 4100)

	reinvite, err := c.callee.NewRequest("INVITE")
	if err != nil {
		t.Fatal(err)
	}

	reinvite.Header.Add("Contact", "<sip:moved@"+s.callee.Addr().String()+";transport=tcp>")
	sendFrom(t, s.callee, s.tr, withSDP(reinvite, 5100))
	wantResponse(t, s.callee, 100)

	fromCallee, f2 := wantRequest(t, s.caller, "INVITE")

	answer(t, s.callee, fromCaller, f1, 491)
	answer(t, s.caller, fromCallee, f2, 491)

	for _, sock := range []*siptest.Socket{s.caller, s.callee} {
		got := map[string]bool{}

		for range 2 {
			got[sock.Recv().Msg.StartLine()[:3]] = true
		}

		if !got["SIP"] || !got["ACK"] {
			t.Errorf("got %v, want the 491 and the ACK to the other 491", got)
		}
	}

	wantState(t, c.d, proxy.Confirmed)

	if got := c.d.Contact(proxy.Callee); !got.Equivalent(before) {
		t.Errorf("callee contact %s after a failed re-INVITE, want %s", got, before)
	}

	if o, a, ok := c.d.Session(); !ok || string(o.Data) != string(offer.Data) || string(a.Data) != string(ans.Data) {
		t.Errorf("session changed by failed re-INVITEs:\n%s\n%s", o.Data, a.Data)
	}
}

func TestDialogCancelRacing200(t *testing.T) {
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

	c := ring(s)
	answer(t, s.callee, c.fwd, c.f, 180)
	wantResponse(t, s.caller, 180)

	cancel, _ := sip.NewCancel(c.invite)
	s.send(cancel)
	wantResponse(t, s.caller, 200)
	wantRequest(t, s.callee, "CANCEL")

	c.answerCall(false)

	if c.d.Released() {
		t.Error("a CANCEL from the caller released the dialog")
	}

	bye, _ := c.caller.NewRequest("BYE")
	sendFrom(t, s.caller, s.tr, bye)
	got, f := wantRequest(t, s.callee, "BYE")
	answer(t, s.callee, got, f, 200)
	wantResponse(t, s.caller, 200)

	if e := s.r.nextEvent(proxy.EventEnded); e.End != proxy.EndBye || e.By != proxy.Caller || e.Code != 200 {
		t.Errorf("ended event %+v", e)
	}
}

func TestDialogCancelled(t *testing.T) {
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

	c := ring(s)
	answer(t, s.callee, c.fwd, c.f, 180)
	wantResponse(t, s.caller, 180)

	cancel, _ := sip.NewCancel(c.invite)
	s.send(cancel)
	wantResponse(t, s.caller, 200)

	fc, cf := wantRequest(t, s.callee, "CANCEL")
	answer(t, s.callee, fc, cf, 200)
	answer(t, s.callee, c.fwd, c.f, 487)
	wantResponse(t, s.caller, 487)

	if e := s.r.nextEvent(proxy.EventEnded); e.End != proxy.EndFailed || e.By != proxy.Caller || e.Code != 487 {
		t.Errorf("ended event %+v", e)
	}

	wantState(t, c.d, proxy.Ended)
}

func TestDialogLate2xxAfterRelease(t *testing.T) {
	cause503, _ := sip.NewReason(sip.ReasonSIP, 503, "")
	failure, _ := sip.NewReason(sip.ReasonFailureCause, sip.FailureSignallingBearerReleased, "")

	for _, tc := range []struct {
		name   string
		toward proxy.Side
	}{
		{"originating", proxy.Callee},
		{"terminating", proxy.Both},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

			c := ring(s)
			answer(t, s.callee, c.fwd, c.f, 180)
			wantResponse(t, s.caller, 180)

			if err := c.d.Release(proxy.Release{Toward: tc.toward, Reason: []sip.Reason{cause503}, ResponseReason: []sip.Reason{failure}}); err != nil {
				t.Fatal(err)
			}

			if e := s.r.nextEvent(proxy.EventEnded); e.End != proxy.EndReleased || e.By != 0 {
				t.Errorf("ended event %+v", e)
			}

			if tc.toward&proxy.Caller != 0 {
				res := wantResponse(t, s.caller, 500)
				if r := res.Header.Get("Reason"); !strings.Contains(r, "FAILURE_CAUSE;cause=2") {
					t.Errorf("Reason %q in the 500", r)
				}
			}

			fc, _ := wantRequest(t, s.callee, "CANCEL")
			if r := fc.Header.Get("Reason"); r != "SIP;cause=503" {
				t.Errorf("CANCEL Reason %q", r)
			}

			ok := answer(t, s.callee, c.fwd, c.f, 200)

			res := wantResponse(t, s.caller, 200)

			ack, _ := wantRequest(t, s.callee, "ACK")
			bye, _ := wantRequest(t, s.callee, "BYE")

			callee, err := dialog.NewUAS(c.fwd, ok)
			if err != nil {
				t.Fatal(err)
			}

			if err := callee.ReceiveRequest(ack); err != nil {
				t.Errorf("ACK: %v\n%s", err, ack)
			}

			if err := callee.ReceiveRequest(bye); err != nil {
				t.Errorf("BYE: %v\n%s", err, bye)
			}

			invite, _ := c.invite.Header.CSeq()
			wantCSeq(t, ack, invite.Seq)
			wantCSeq(t, bye, invite.Seq+1)

			if r := bye.Header.Get("Reason"); r != "SIP;cause=503" {
				t.Errorf("BYE Reason %q", r)
			}

			caller, err := dialog.NewUAC(c.invite, res)
			if err != nil {
				t.Fatal(err)
			}

			s.caller.RecvNone(quiet)

			callerAck, _ := caller.NewAck(c.invite)
			sendFrom(t, s.caller, s.tr, callerAck)

			toCaller, _ := wantRequest(t, s.caller, "BYE")
			if err := caller.ReceiveRequest(toCaller); err != nil {
				t.Errorf("BYE toward the caller: %v\n%s", err, toCaller)
			}

			if r := toCaller.Header.Get("Reason"); r != "SIP;cause=503" {
				t.Errorf("BYE Reason %q", r)
			}

			s.callee.RecvNone(quiet)
			s.r.noEvent()
		})
	}
}

func TestDialogLate2xxAfterTimerC(t *testing.T) {
	clock := siptest.NewClock()
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true, clock: clock})

	c := ring(s)
	answer(t, s.callee, c.fwd, c.f, 180)
	wantResponse(t, s.caller, 180)

	clock.Advance(proxy.DefaultTimerC)
	wantResponse(t, s.caller, 408)
	wantRequest(t, s.callee, "CANCEL")

	if e := s.r.nextEvent(proxy.EventEnded); e.End != proxy.EndFailed || e.Code != 408 || e.By != 0 {
		t.Errorf("ended event %+v", e)
	}

	answer(t, s.callee, c.fwd, c.f, 200)
	wantResponse(t, s.caller, 200)

	ack, _ := wantRequest(t, s.callee, "ACK")
	if len(ack.Body) != 0 {
		t.Errorf("ACK with a body for a 2xx without an offer:\n%s", ack)
	}

	wantRequest(t, s.callee, "BYE")
	s.caller.RecvNone(quiet)
	s.r.noEvent()
}

func TestDialogLate2xxWithAnOffer(t *testing.T) {
	clock := siptest.NewClock()
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true, clock: clock})

	invite := s.request("INVITE")
	s.send(invite)
	wantResponse(t, s.caller, 100)
	s.r.nextDialog()

	fwd, f := s.forwarded()
	answer(t, s.callee, fwd, f, 180)
	wantResponse(t, s.caller, 180)

	clock.Advance(proxy.DefaultTimerC)
	wantResponse(t, s.caller, 408)
	wantRequest(t, s.callee, "CANCEL")

	ok := sip.NewResponse(fwd, 200, "")
	_ = ok.Header.SetToTag("callee")
	dialog.CopyRecordRoute(ok, fwd)
	ok.Header.Add("Contact", "<"+target(s.callee, sip.TCP)+">")
	s.callee.Send(f.Transport, f.Remote, withSDP(ok, 5000))
	wantResponse(t, s.caller, 200)

	ack, _ := wantRequest(t, s.callee, "ACK")

	answer, err := sdpsession(ack.Body)
	if err != nil {
		t.Fatalf("ACK body: %v\n%s", err, ack)
	}

	if len(answer.Media) != 1 || answer.Media[0].Port() != 0 {
		t.Errorf("ACK answer does not reject the offered stream:\n%s", ack.Body)
	}

	wantRequest(t, s.callee, "BYE")
}

func TestDialogTimerCWithoutAnswer(t *testing.T) {
	clock := siptest.NewClock()
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true, clock: clock})

	c := ring(s)
	answer(t, s.callee, c.fwd, c.f, 180)
	wantResponse(t, s.caller, 180)

	clock.Advance(proxy.DefaultTimerC)
	wantResponse(t, s.caller, 408)

	if e := s.r.nextEvent(proxy.EventEnded); e.End != proxy.EndFailed || e.Code != 408 || e.By != 0 {
		t.Errorf("ended event %+v", e)
	}

	wantState(t, c.d, proxy.Ended)

	answer(t, s.callee, c.fwd, c.f, 487)
	s.caller.RecvNone(quiet)
	s.r.noEvent()
}

func TestDialogReleaseAnswered(t *testing.T) {
	cause, _ := sip.NewReason(sip.ReasonSIP, 503, "")

	t.Run("2xx relayed", func(t *testing.T) {
		s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

		c := ring(s)
		c.answerCall(true)

		if err := c.d.Release(proxy.Release{Toward: proxy.Both, Reason: []sip.Reason{cause}}); err != nil {
			t.Fatal(err)
		}

		invite, _ := c.invite.Header.CSeq()

		ack, _ := wantRequest(t, s.callee, "ACK")
		wantCSeq(t, ack, invite.Seq)

		bye, _ := wantRequest(t, s.callee, "BYE")
		if err := c.callee.ReceiveRequest(bye); err != nil {
			t.Errorf("BYE toward the callee: %v", err)
		}

		s.caller.RecvNone(quiet)

		callerAck, _ := c.caller.NewAck(c.invite)
		sendFrom(t, s.caller, s.tr, callerAck)

		toCaller, _ := wantRequest(t, s.caller, "BYE")
		if err := c.caller.ReceiveRequest(toCaller); err != nil {
			t.Errorf("BYE toward the caller: %v", err)
		}

		s.callee.RecvNone(quiet)
	})

	t.Run("2xx held", func(t *testing.T) {
		s := newScene(t, sip.TCP, routerConfig{
			opts: proxy.Options{RecordRoute: recordRoute}, track: true,
			onReply: func(_ *transaction.ServerTransaction, _ *sip.Request, r proxy.Reply) proxy.Verdict {
				if r.Response.StatusCode == 200 {
					return proxy.Hold
				}

				return proxy.Relay
			},
		})

		c := ring(s)
		answer(t, s.callee, c.fwd, c.f, 200)
		s.r.nextEvent(proxy.EventAnswered)

		failure, _ := sip.NewReason(sip.ReasonFailureCause, sip.FailureResourcesAllocation, "")
		if err := c.d.Release(proxy.Release{Toward: proxy.Both, Reason: []sip.Reason{cause}, ResponseReason: []sip.Reason{failure}}); err != nil {
			t.Fatal(err)
		}

		res := wantResponse(t, s.caller, 500)
		if r := res.Header.Get("Reason"); !strings.Contains(r, "FAILURE_CAUSE;cause=3") {
			t.Errorf("Reason %q in the 500", r)
		}

		wantRequest(t, s.callee, "ACK")
		wantRequest(t, s.callee, "BYE")
		s.caller.RecvNone(quiet)
	})
}

func TestDialogLostOn481(t *testing.T) {
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

	c := ring(s)
	c.answerCall(false)

	_, got, f := c.request(proxy.Caller, "INVITE", 0)
	answer(t, s.callee, got, f, 481)
	wantResponse(t, s.caller, 481)

	if e := s.r.nextEvent(proxy.EventEnded); e.End != proxy.EndLost || e.By != proxy.Callee {
		t.Errorf("ended event %+v", e)
	}

	wantState(t, c.d, proxy.Ended)
}

func TestDialogNotStartedWhenTheInviteFails(t *testing.T) {
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

	nowhere := netip.MustParseAddrPort("127.0.0.1:1")
	s.r.out.Store(&nowhere)

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)
	wantResponse(t, s.caller, 500)

	d := s.r.nextDialog()
	s.r.noEvent()

	if err := d.Release(proxy.Release{Toward: proxy.Both}); err == nil {
		t.Error("released a dialog that never started")
	}
}

func TestDialogEarlyUpdate(t *testing.T) {
	clock := siptest.NewClock()
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true, clock: clock})

	c := ring(s)
	progress := answer(t, s.callee, c.fwd, c.f, 183)
	early := wantResponse(t, s.caller, 183)

	ad, err := dialog.NewUAC(c.invite, early)
	if err != nil {
		t.Fatal(err)
	}

	bd, err := dialog.NewUAS(c.fwd, progress)
	if err != nil {
		t.Fatal(err)
	}

	update, _ := bd.NewRequest("UPDATE")
	update.Header.Add("Contact", "<sip:moved@"+s.callee.Addr().String()+";transport=tcp>")
	sendFrom(t, s.callee, s.tr, update)

	got, f := wantRequest(t, s.caller, "UPDATE")
	answer(t, s.caller, got, f, 200, "Session-Expires", "90;refresher=uas")
	_ = ad.ReceiveRequest(got)

	wantResponse(t, s.callee, 200)

	clock.Advance(91 * time.Second)
	wantState(t, c.d, proxy.Early)

	ok := sip.NewResponse(c.fwd, 200, "")
	_ = ok.Header.SetToTag("callee")
	dialog.CopyRecordRoute(ok, c.fwd)
	s.callee.Send(c.f.Transport, c.f.Remote, ok)
	wantResponse(t, s.caller, 200)

	if got := c.d.Contact(proxy.Callee); got.User != "moved" {
		t.Errorf("callee contact %s, want the one from the early UPDATE", got)
	}
}

func TestDialogEarlyByeFromCaller(t *testing.T) {
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

	c := ring(s)
	answer(t, s.callee, c.fwd, c.f, 180)
	early := wantResponse(t, s.caller, 180)

	ad, err := dialog.NewUAC(c.invite, early)
	if err != nil {
		t.Fatal(err)
	}

	bye, _ := ad.NewRequest("BYE")
	sendFrom(t, s.caller, s.tr, bye)

	got, f := wantRequest(t, s.callee, "BYE")
	answer(t, s.callee, got, f, 200)
	wantResponse(t, s.caller, 200)

	answer(t, s.callee, c.fwd, c.f, 487)
	wantResponse(t, s.caller, 487)

	if e := s.r.nextEvent(proxy.EventEnded); e.End != proxy.EndFailed || e.By != proxy.Caller || e.Code != 487 {
		t.Errorf("ended event %+v", e)
	}
}

func TestDialogForkedLegRelayed(t *testing.T) {
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

	c := ring(s)
	c.answerCall(false)

	other := sip.NewResponse(c.fwd, 200, "")
	_ = other.Header.SetToTag("other")
	dialog.CopyRecordRoute(other, c.fwd)
	other.Header.Add("Contact", "<"+target(s.callee, sip.TCP)+">")
	s.callee.Send(c.f.Transport, c.f.Remote, other)

	res := wantResponse(t, s.caller, 200)

	ad, err := dialog.NewUAC(c.invite, res)
	if err != nil {
		t.Fatal(err)
	}

	ack, _ := ad.NewAck(c.invite)
	sendFrom(t, s.caller, s.tr, ack)
	wantRequest(t, s.callee, "ACK")

	bye, _ := ad.NewRequest("BYE")
	sendFrom(t, s.caller, s.tr, bye)
	got, f := wantRequest(t, s.callee, "BYE")
	answer(t, s.callee, got, f, 200)
	wantResponse(t, s.caller, 200)

	wantState(t, c.d, proxy.Confirmed)
	s.r.noEvent()
}

func TestDialogByeCSeqAfterReinvites(t *testing.T) {
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

	c := ring(s)
	c.answerCall(false)

	reinvite := func(from proxy.Side) {
		sent, got, f := c.request(from, "INVITE", 0)
		answer(t, peerOf(s, from), got, f, 200)
		res := wantResponse(t, sockOf(s, from), 200)

		d := c.caller
		if from == proxy.Callee {
			d = c.callee
		}

		_ = d.ReceiveResponse(res)

		ack, _ := d.NewAck(sent)
		sendFrom(t, sockOf(s, from), s.tr, ack)
		wantRequest(t, peerOf(s, from), "ACK")
	}

	reinvite(proxy.Caller)
	reinvite(proxy.Callee)
	reinvite(proxy.Caller)
	reinvite(proxy.Callee)

	callerSeq, _ := c.caller.LocalSeq()
	calleeSeq, _ := c.callee.LocalSeq()

	if got, _ := c.d.Seq(proxy.Caller); got != callerSeq {
		t.Errorf("caller CSeq %d, want %d", got, callerSeq)
	}

	if got, _ := c.d.Seq(proxy.Callee); got != calleeSeq {
		t.Errorf("callee CSeq %d, want %d", got, calleeSeq)
	}

	if err := c.d.Release(proxy.Release{Toward: proxy.Both}); err != nil {
		t.Fatal(err)
	}

	toCallee, _ := wantRequest(t, s.callee, "BYE")
	toCaller, _ := wantRequest(t, s.caller, "BYE")

	wantCSeq(t, toCallee, callerSeq+1)
	wantCSeq(t, toCaller, calleeSeq+1)

	if err := c.callee.ReceiveRequest(toCallee); err != nil {
		t.Errorf("BYE toward the callee: %v", err)
	}

	if err := c.caller.ReceiveRequest(toCaller); err != nil {
		t.Errorf("BYE toward the caller: %v", err)
	}

	if toCallee.Header.Has("Reason") || toCaller.Header.Has("Reason") {
		t.Error("Reason without a cause")
	}
}

func sockOf(s *scene, side proxy.Side) *siptest.Socket {
	if side == proxy.Callee {
		return s.callee
	}

	return s.caller
}

func peerOf(s *scene, side proxy.Side) *siptest.Socket {
	return sockOf(s, proxy.Both&^side)
}

func TestDialogSessionExpires(t *testing.T) {
	clock := siptest.NewClock()
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true, clock: clock})

	c := ring(s)
	c.answerCall(false, "Session-Expires", "90;refresher=uac")

	clock.Advance(60 * time.Second)

	update, got, f := c.request(proxy.Caller, "UPDATE", 0)
	answer(t, s.callee, got, f, 200, "Session-Expires", "90;refresher=uac")
	_ = c.caller.ReceiveResponse(wantResponse(t, s.caller, 200))
	_ = update

	clock.Advance(89 * time.Second)
	wantState(t, c.d, proxy.Confirmed)

	clock.Advance(time.Second)

	if e := s.r.nextEvent(proxy.EventEnded); e.End != proxy.EndExpired {
		t.Errorf("ended event %+v", e)
	}

	wantState(t, c.d, proxy.Ended)
	s.caller.RecvNone(quiet)
	s.callee.RecvNone(quiet)

	bye, _ := c.caller.NewRequest("BYE")
	sendFrom(t, s.caller, s.tr, bye)
	wantRequest(t, s.callee, "BYE")
}

func TestDialogLifetime(t *testing.T) {
	clock := siptest.NewClock()
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true, clock: clock, lifetime: time.Hour})

	c := ring(s)
	c.answerCall(false)

	clock.Advance(time.Hour - time.Second)
	wantState(t, c.d, proxy.Confirmed)

	clock.Advance(time.Second)

	if e := s.r.nextEvent(proxy.EventEnded); e.End != proxy.EndExpired {
		t.Errorf("ended event %+v", e)
	}
}

func TestDialogDiscard(t *testing.T) {
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

	c := ring(s)
	c.answerCall(false)

	c.d.Discard()

	if e := s.r.nextEvent(proxy.EventEnded); e.End != proxy.EndDiscarded {
		t.Errorf("ended event %+v", e)
	}

	s.caller.RecvNone(quiet)
	s.callee.RecvNone(quiet)

	bye, _ := c.callee.NewRequest("BYE")
	sendFrom(t, s.callee, s.tr, bye)
	wantResponse(t, s.callee, 481)
	s.caller.RecvNone(quiet)
}

func TestDialogOfferInReliableProvisional(t *testing.T) {
	s := newScene(t, sip.TCP, routerConfig{opts: proxy.Options{RecordRoute: recordRoute}, track: true})

	invite := s.request("INVITE")
	s.send(invite)
	wantResponse(t, s.caller, 100)

	d := s.r.nextDialog()
	fwd, f := s.forwarded()

	progress := withSDP(sip.NewResponse(fwd, 183, ""), 5000)
	_ = progress.Header.SetToTag("callee")
	dialog.CopyRecordRoute(progress, fwd)
	progress.Header.Add("Contact", "<"+target(s.callee, sip.TCP)+">")
	progress.Header.Add("Require", "100rel")
	progress.Header.Add("RSeq", "1")
	s.callee.Send(f.Transport, f.Remote, progress)

	early := wantResponse(t, s.caller, 183)

	if offer, _, ok := d.Session(); ok || offer.From != proxy.Callee {
		t.Fatalf("offer %+v, answered %v", offer, ok)
	}

	ad, err := dialog.NewUAC(invite, early)
	if err != nil {
		t.Fatal(err)
	}

	prack, _ := ad.NewPrack(early)
	sendFrom(t, s.caller, s.tr, withSDP(prack, 4000))
	wantRequest(t, s.callee, "PRACK")

	if offer, ans, ok := d.Session(); !ok || offer.From != proxy.Callee || ans.From != proxy.Caller {
		t.Errorf("session after the PRACK: %+v, %+v, %v", offer, ans, ok)
	}
}
