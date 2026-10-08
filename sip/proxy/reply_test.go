package proxy_test

import (
	"errors"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

type held struct {
	tx  *transaction.ServerTransaction
	req *sip.Request
	r   proxy.Reply
}

func holdAll(ch chan held) func(*transaction.ServerTransaction, *sip.Request, proxy.Reply) proxy.Verdict {
	return func(tx *transaction.ServerTransaction, req *sip.Request, r proxy.Reply) proxy.Verdict {
		ch <- held{tx: tx, req: req, r: r}
		return proxy.Hold
	}
}

func nextHeld(t *testing.T, ch chan held) held {
	t.Helper()

	select {
	case h := <-ch:
		return h
	case <-time.After(siptest.Timeout):
		t.Fatal("no reply reached OnReply")
		return held{}
	}
}

func TestAdvertisedAddress(t *testing.T) {
	rr := &proxy.RecordRoute{User: "mt", Params: sip.Params{{Name: "ftag", Value: "x"}}}
	r := newRouter(t, loopback, routerConfig{opts: proxy.Options{RecordRoute: rr}})
	protected := siptest.ListenLayer(t, r.l, loopback)
	r.sentBy.Store(&protected)

	s := &scene{t: t, tr: sip.UDP, r: r, caller: siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)), callee: siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))}

	invite := s.request("INVITE")
	s.send(invite)
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()

	if f.Remote != r.local {
		t.Errorf("sent from %s, want %s", f.Remote, r.local)
	}

	if via := mustTopVia(t, fwd); via.SentBy() != protected.String() {
		t.Errorf("Via sent-by %s, want %s", via.SentBy(), protected)
	}

	want := "<sip:mt@" + protected.String() + ";lr;r2=on;ftag=x>, <sip:mt@" + r.local.String() + ";lr;r2=on;ftag=x>"
	if got := strings.Join(fwd.Header.Values("Record-Route"), ", "); got != want {
		t.Fatalf("Record-Route = %s, want %s", got, want)
	}

	ok := sip.NewResponse(fwd, 200, "")
	_ = ok.Header.SetToTag("callee")
	ok.Header.Add("Contact", "<"+target(s.callee, sip.UDP)+">")

	d, err := dialog.NewUAS(fwd, ok)
	if err != nil {
		t.Fatal(err)
	}

	s.callee.Send(sip.UDP, protected, ok)
	wantResponse(t, s.caller, 200)

	bye, err := d.NewRequest("BYE")
	if err != nil {
		t.Fatal(err)
	}

	bye.Header.Prepend("Via", sip.NewVia(sip.UDP, s.callee.Addr()).String())

	if _, next, _ := sip.NextHop(bye); next != protected {
		t.Fatalf("in-dialog request goes to %s, want %s", next, protected)
	}

	s.callee.Send(sip.UDP, protected, bye)

	if got, _ := s.caller.RecvRequest(); got.Method != "BYE" || got.Header.Has("Route") {
		t.Errorf("BYE at the caller:\n%s", got)
	}
}

func TestSingleRecordRouteWithUser(t *testing.T) {
	s := newScene(t, sip.UDP, routerConfig{opts: proxy.Options{RecordRoute: &proxy.RecordRoute{User: "mo"}}})

	s.send(s.request("MESSAGE"))

	fwd, _ := s.forwarded()
	if got := fwd.Header.Values("Record-Route"); len(got) != 1 || got[0] != "<sip:mo@"+s.r.local.String()+";lr>" {
		t.Errorf("Record-Route = %q", got)
	}
}

func TestCancelRacingFinal(t *testing.T) {
	var (
		sc   atomic.Pointer[scene]
		sent atomic.Pointer[sip.Request]
	)

	s := newScene(t, sip.TCP, routerConfig{onReply: func(_ *transaction.ServerTransaction, _ *sip.Request, r proxy.Reply) proxy.Verdict {
		if r.Response.StatusCode == 200 {
			s := sc.Load()
			cancel, _ := sip.NewCancel(sent.Load())
			s.send(cancel)
			<-s.r.cancelled
		}

		return proxy.Relay
	}})
	sc.Store(s)

	invite := s.request("INVITE")
	sent.Store(invite)
	s.send(invite)
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 200, "Contact", "<"+target(s.callee, sip.TCP)+">")

	methods := map[string]bool{}

	for range 2 {
		res := wantResponse(t, s.caller, 200)
		cseq, _ := res.Header.CSeq()
		methods[cseq.Method] = true
	}

	if !methods["CANCEL"] || !methods["INVITE"] {
		t.Errorf("got 200s for %v, want CANCEL and INVITE", methods)
	}

	s.caller.RecvNone(quiet)
	s.callee.RecvNone(quiet)
}

func TestHoldAndRelay(t *testing.T) {
	replies := make(chan held, 8)
	s := newScene(t, sip.UDP, routerConfig{onReply: holdAll(replies)})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()

	reply(t, s.callee, fwd, f, 183)
	progress := nextHeld(t, replies)

	reply(t, s.callee, fwd, f, 200, "Contact", "<"+target(s.callee, sip.UDP)+">")
	answer := nextHeld(t, replies)

	reply(t, s.callee, fwd, f, 200, "Contact", "<"+target(s.callee, sip.UDP)+">")
	s.caller.RecvNone(quiet)

	if err := s.r.p.Relay(progress.tx, progress.r.Response); err != nil {
		t.Fatal(err)
	}

	wantResponse(t, s.caller, 183)

	if err := s.r.p.Relay(answer.tx, answer.r.Response); err != nil {
		t.Fatal(err)
	}

	wantResponse(t, s.caller, 200)
	s.caller.RecvNone(quiet)

	reply(t, s.callee, fwd, f, 200, "Contact", "<"+target(s.callee, sip.UDP)+">")
	wantResponse(t, s.caller, 200)

	select {
	case h := <-replies:
		t.Errorf("OnReply called again for %s", h.r.Response.StartLine())
	case <-time.After(quiet):
	}
}

func TestRetryAnotherTarget(t *testing.T) {
	var (
		sc   atomic.Pointer[scene]
		seen atomic.Int32
	)

	second := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	s := newScene(t, sip.UDP, routerConfig{onReply: func(tx *transaction.ServerTransaction, req *sip.Request, r proxy.Reply) proxy.Verdict {
		s := sc.Load()

		if r.Response.StatusCode != 503 {
			return proxy.Relay
		}

		seen.Add(1)

		out := req.Clone()
		out.URI, _ = sip.ParseURI(target(second, sip.UDP))

		if err := s.r.p.Forward(tx, out, s.r.target(out, tx.Request().Flow.Local), proxy.Options{}); err != nil {
			t.Error(err)
			return proxy.Relay
		}

		return proxy.Hold
	}})
	sc.Store(s)

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 503)

	if ack, _ := s.forwarded(); ack.Method != "ACK" {
		t.Errorf("got %s, want the ACK to the 503", ack.Method)
	}

	retry, rf := second.RecvRequest()
	if retry.Method != "INVITE" || mustTopVia(t, retry).Branch() == mustTopVia(t, fwd).Branch() {
		t.Fatalf("retry:\n%s", retry)
	}

	reply(t, second, retry, rf, 200, "Contact", "<"+target(second, sip.UDP)+">")
	wantResponse(t, s.caller, 200)
	s.caller.RecvNone(quiet)

	if seen.Load() != 1 {
		t.Errorf("OnReply saw %d 503s", seen.Load())
	}
}

func TestTimeoutRetry(t *testing.T) {
	var sc atomic.Pointer[scene]

	clock := siptest.NewClock()
	second := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	failures := make(chan proxy.Reply, 1)

	s := newScene(t, sip.TCP, routerConfig{clock: clock, opts: proxy.Options{Timeout: 2 * time.Second}, onReply: func(tx *transaction.ServerTransaction, req *sip.Request, r proxy.Reply) proxy.Verdict {
		s := sc.Load()

		if r.Err == nil {
			return proxy.Relay
		}

		failures <- r

		out := req.Clone()
		out.URI, _ = sip.ParseURI(target(second, sip.TCP))

		if err := s.r.p.Forward(tx, out, s.r.target(out, tx.Request().Flow.Local), proxy.Options{}); err != nil {
			t.Error(err)
		}

		return proxy.Hold
	}})
	sc.Store(s)

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)
	s.forwarded()

	clock.Advance(2 * time.Second)

	r := <-failures
	if !errors.Is(r.Err, transaction.ErrTimeout) || r.Response == nil || r.Response.StatusCode != 408 || r.Responded {
		t.Errorf("timeout reply: %v, %v, responded %v", r.Response, r.Err, r.Responded)
	}

	retry, rf := second.RecvRequest()
	reply(t, second, retry, rf, 200, "Contact", "<"+target(second, sip.TCP)+">")
	wantResponse(t, s.caller, 200)
}

func TestNonInviteTimeout(t *testing.T) {
	clock := siptest.NewClock()
	replies := make(chan held, 1)
	s := newScene(t, sip.TCP, routerConfig{clock: clock, onReply: holdAll(replies)})

	s.send(s.request("MESSAGE"))
	s.forwarded()

	clock.Advance(64 * transaction.DefaultT1)

	h := nextHeld(t, replies)
	if h.r.Response != nil || !errors.Is(h.r.Err, transaction.ErrTimeout) {
		t.Errorf("reply %v, %v; want no response and ErrTimeout", h.r.Response, h.r.Err)
	}

	select {
	case <-h.tx.Done():
	case <-time.After(siptest.Timeout):
		t.Fatal("server transaction never ended")
	}

	wantResponse(t, s.caller, 100)
	s.caller.RecvNone(quiet)
}

func TestNonInviteTimeoutAfterTrying(t *testing.T) {
	clock := siptest.NewClock()
	replies := make(chan held, 1)
	s := newScene(t, sip.TCP, routerConfig{clock: clock, opts: proxy.Options{Timeout: 2 * time.Second}, onReply: holdAll(replies)})

	s.send(s.request("MESSAGE"))

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 100)

	for deadline := time.Now().Add(siptest.Timeout); !s.r.p.Responded(); {
		if time.Now().After(deadline) {
			t.Fatal("the 100 never reached the branch")
		}

		time.Sleep(time.Millisecond)
	}

	clock.Advance(2 * time.Second)

	h := nextHeld(t, replies)
	if h.r.Response != nil || !errors.Is(h.r.Err, transaction.ErrTimeout) || !h.r.Responded {
		t.Fatalf("reply %v, %v, responded %v; want a timeout after the 100", h.r.Response, h.r.Err, h.r.Responded)
	}
}

func TestTimerCReplyResponded(t *testing.T) {
	clock := siptest.NewClock()
	replies := make(chan held, 2)
	s := newScene(t, sip.UDP, routerConfig{clock: clock, onReply: holdAll(replies)})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 180)

	if h := nextHeld(t, replies); h.r.Response.StatusCode != 180 || !h.r.Responded {
		t.Fatalf("got %v, responded %v; want 180", h.r.Response, h.r.Responded)
	}

	clock.Advance(proxy.DefaultTimerC)

	fc, cf := s.forwarded()
	if fc.Method != "CANCEL" {
		t.Fatalf("got %s, want CANCEL", fc.Method)
	}

	reply(t, s.callee, fc, cf, 200)
	reply(t, s.callee, fwd, f, 487)

	h := nextHeld(t, replies)
	if h.r.Response.StatusCode != 408 || !errors.Is(h.r.Err, transaction.ErrTimeout) || !h.r.Responded {
		t.Fatalf("Timer C reply: %v, responded %v; want ErrTimeout after a response", h.r.Err, h.r.Responded)
	}
}

func TestTaglessResponses(t *testing.T) {
	s := newScene(t, sip.UDP, routerConfig{})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()

	ringing := sip.NewResponse(fwd, 180, "")
	s.callee.Send(f.Transport, f.Remote, ringing)

	if to, _ := wantResponse(t, s.caller, 180).Header.To(); to.Tag() != "" {
		t.Errorf("proxy added To tag %q to a 180", to.Tag())
	}

	busy := sip.NewResponse(fwd, 486, "")
	s.callee.Send(f.Transport, f.Remote, busy)

	if to, _ := wantResponse(t, s.caller, 486).Header.To(); to.Tag() == "" {
		t.Error("486 relayed without a To tag")
	}
}

func TestCancelReason(t *testing.T) {
	s := newScene(t, sip.UDP, routerConfig{})

	invite := s.request("INVITE")
	s.send(invite)
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 180)
	wantResponse(t, s.caller, 180)

	cancel, _ := sip.NewCancel(invite)
	cancel.Header.Insert("Reason", `SIP;cause=200;text="Call completed elsewhere"`)
	s.send(cancel)
	wantResponse(t, s.caller, 200)

	fc, _ := s.forwarded()
	if fc.Method != "CANCEL" || fc.Header.Get("Reason") != `SIP;cause=200;text="Call completed elsewhere"` {
		t.Errorf("forwarded CANCEL:\n%s", fc)
	}
}

func TestCancelWhileHoldingFinal(t *testing.T) {
	replies := make(chan held, 4)
	s := newScene(t, sip.UDP, routerConfig{onReply: holdAll(replies)})

	invite := s.request("INVITE")
	s.send(invite)
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 486)

	h := nextHeld(t, replies)

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

	if err := s.r.p.Relay(h.tx, h.r.Response); !errors.Is(err, proxy.ErrAnswered) {
		t.Errorf("Relay after CANCEL: %v", err)
	}
}

func TestForwardAfterAnswer(t *testing.T) {
	s := newScene(t, sip.UDP, routerConfig{hold: true})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	tx := <-s.r.held
	if err := tx.Respond(sip.NewResponse(tx.Request(), 486, "")); err != nil {
		t.Fatal(err)
	}

	out := s.r.preprocess(tx.Request())

	if err := s.r.p.Forward(tx, out, s.r.target(out, s.r.local), proxy.Options{}); !errors.Is(err, proxy.ErrAnswered) {
		t.Errorf("Forward after a final response: %v", err)
	}
}

func TestTimerCMinimum(t *testing.T) {
	clock := siptest.NewClock()
	s := newScene(t, sip.TCP, routerConfig{clock: clock, timerC: time.Minute})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 180)
	wantResponse(t, s.caller, 180)

	clock.Advance(3 * time.Minute)
	s.caller.RecvNone(quiet)

	clock.Advance(time.Second)

	fc, cf := s.forwarded()
	if fc.Method != "CANCEL" {
		t.Fatalf("got %s, want CANCEL", fc.Method)
	}

	reply(t, s.callee, fc, cf, 200)
	reply(t, s.callee, fwd, f, 487)
	wantResponse(t, s.caller, 408)
}

func TestHeldReplyDroppedWhenTransactionEnds(t *testing.T) {
	clock := siptest.NewClock()
	replies := make(chan held, 1)
	s := newScene(t, sip.UDP, routerConfig{clock: clock, onReply: holdAll(replies)})

	s.send(s.request("MESSAGE"))

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 200)
	h := nextHeld(t, replies)

	if n := s.r.p.Pending(); n != 1 {
		t.Fatalf("%d pending contexts, want 1", n)
	}

	clock.Advance(64 * transaction.DefaultT1)

	select {
	case <-h.tx.Done():
	case <-time.After(siptest.Timeout):
		t.Fatal("server transaction never ended")
	}

	if n := s.r.p.Pending(); n != 0 {
		t.Errorf("%d pending contexts after the transaction ended", n)
	}

	if err := s.r.p.Relay(h.tx, h.r.Response); !errors.Is(err, proxy.ErrAnswered) {
		t.Errorf("Relay after the transaction ended: %v", err)
	}
}

func TestDirectAnswerReleasesContext(t *testing.T) {
	replies := make(chan held, 1)
	s := newScene(t, sip.TCP, routerConfig{onReply: holdAll(replies)})

	invite := s.request("INVITE")
	s.send(invite)
	wantResponse(t, s.caller, 100)

	fwd, f := s.forwarded()
	reply(t, s.callee, fwd, f, 486)
	h := nextHeld(t, replies)

	if err := h.tx.Respond(sip.NewResponse(h.tx.Request(), 480, "")); err != nil {
		t.Fatal(err)
	}

	res := wantResponse(t, s.caller, 480)

	ack, err := sip.NewAck(invite, res)
	if err != nil {
		t.Fatal(err)
	}

	s.send(ack)

	select {
	case <-h.tx.Done():
	case <-time.After(siptest.Timeout):
		t.Fatal("server transaction never ended")
	}

	if n := s.r.p.Pending(); n != 0 {
		t.Errorf("%d pending contexts after the transaction ended", n)
	}
}
