package proxy_test

import (
	"errors"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
)

// forker forks every request it receives to the callees, group by group.
type forker struct {
	t     *testing.T
	l     *transaction.Layer
	p     *proxy.Proxy
	local netip.AddrPort

	groups [][]*siptest.Socket
	opts   proxy.Options
	errs   chan error

	// track has every INVITE carry a Dialog, and in-dialog requests follow it.
	track   bool
	dialogs chan *proxy.Dialog
	events  chan proxy.DialogEvent
}

type forkScene struct {
	t      *testing.T
	f      *forker
	caller *siptest.Socket
	clock  *siptest.Clock
}

func newForkScene(t *testing.T, shape []int, opts proxy.Options) (*forkScene, [][]*siptest.Socket) {
	return newForkSceneTracking(t, shape, opts, false)
}

func newForkSceneTracking(t *testing.T, shape []int, opts proxy.Options, track bool) (*forkScene, [][]*siptest.Socket) {
	t.Helper()

	clock := siptest.NewClock()
	f := &forker{
		t: t, opts: opts, errs: make(chan error, 4), track: track,
		dialogs: make(chan *proxy.Dialog, 4), events: make(chan proxy.DialogEvent, 64),
	}

	for _, n := range shape {
		var g []*siptest.Socket

		for range n {
			g = append(g, siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)))
		}

		f.groups = append(f.groups, g)
	}

	f.l, _ = siptest.NewLayer(t, transaction.Config{Handler: f, Clock: clock})

	pc := proxy.Config{Layer: f.l, Clock: clock}
	if track {
		pc.OnDialog = func(e proxy.DialogEvent) { f.events <- e }
	}

	f.p = proxy.New(pc)
	f.local = siptest.ListenLayer(t, f.l, loopback)

	return &forkScene{t: t, f: f, caller: siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)), clock: clock}, f.groups
}

func (f *forker) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	if res := f.p.Check(req); res != nil {
		_ = tx.Respond(res)
		return
	}

	out, removed, err := f.p.Preprocess(req)
	if err != nil {
		f.t.Error(err)
		return
	}

	opts := f.opts

	if to, _ := out.Header.To(); to.Tag() != "" {
		err := f.p.Forward(tx, out, nextHop(f.t, out, req.Flow.Local), proxy.Options{Dialog: f.p.Dialog(removed)})
		if err != nil {
			f.t.Error(err)
		}

		return
	}

	if f.track && req.Method == "INVITE" {
		opts.Dialog = f.p.NewDialog(proxy.DialogConfig{})
		opts.RecordRoute = &proxy.RecordRoute{}

		f.dialogs <- opts.Dialog
	}

	var groups [][]proxy.Branch

	for _, g := range f.groups {
		var bs []proxy.Branch

		for _, s := range g {
			r := out.Clone()
			r.URI, _ = sip.ParseURI(target(s, sip.UDP))

			bs = append(bs, proxy.Branch{
				Request: r,
				Target:  proxy.Target{Flow: sip.Flow{Transport: sip.UDP, Local: req.Flow.Local, Remote: s.Addr()}},
				Options: opts,
			})
		}

		groups = append(groups, bs)
	}

	if err := f.p.Fork(tx, groups); err != nil {
		f.errs <- err

		code := 500
		if serr, ok := errors.AsType[*sip.StatusError](err); ok {
			code = serr.StatusCode
		}

		_ = tx.Respond(sip.NewResponse(req, code, ""))
	}
}

func (f *forker) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	f.p.Cancel(tx, cancel)
}

func (f *forker) HandleAck(ack *sip.Request) {
	out, removed, err := f.p.Preprocess(ack)
	if err != nil {
		f.t.Error(err)
		return
	}

	err = f.p.ForwardAck(out, nextHop(f.t, out, ack.Flow.Local), f.p.Dialog(removed))
	if err != nil && !errors.Is(err, proxy.ErrDialogEnded) {
		f.t.Error(err)
	}
}

func nextHop(t *testing.T, out *sip.Request, local netip.AddrPort) proxy.Target {
	tr, to, err := sip.NextHop(out)
	if err != nil {
		t.Error(err)
	}

	return proxy.Target{Flow: sip.Flow{Transport: tr, Local: local, Remote: to}}
}

func (f *forker) HandleTransactionError(*transaction.ServerTransaction, error) {}

func (s *forkScene) request(method string) *sip.Request {
	req := siptest.NewRequest(method, "sip:bob@example.com", sip.UDP, s.caller.Addr())
	req.Header.Set("Contact", "<sip:alice@"+s.caller.Addr().String()+">")
	req.Header.Prepend("Route", "<sip:"+s.f.local.String()+";lr>")

	return req
}

func (s *forkScene) send(req *sip.Request) {
	s.caller.Send(sip.UDP, s.f.local, req)
}

// final receives the next final response to the caller, other than one to a CANCEL.
func (s *forkScene) final() *sip.Response {
	s.t.Helper()

	for {
		res, _ := s.caller.RecvResponse()
		if cseq, _ := res.Header.CSeq(); !res.IsProvisional() && cseq.Method != "CANCEL" {
			return res
		}
	}
}

// relayed waits until the caller has a provisional response with code.
func (s *forkScene) relayed(code int) {
	s.t.Helper()

	for {
		if res, _ := s.caller.RecvResponse(); res.StatusCode == code {
			return
		}
	}
}

type leg struct {
	t   *testing.T
	s   *siptest.Socket
	req *sip.Request
	f   sip.Flow
	tag string
}

func received(t *testing.T, s *siptest.Socket, tag string) *leg {
	t.Helper()

	req, f := s.RecvRequest()

	return &leg{t: t, s: s, req: req, f: f, tag: tag}
}

func (l *leg) respond(code int, extra ...string) {
	l.t.Helper()

	res := sip.NewResponse(l.req, code, "")
	if code > 100 {
		_ = res.Header.SetToTag(l.tag)
		res.Header.Add("Contact", "<sip:"+l.tag+"@"+l.s.Addr().String()+">")
	}

	if code > 100 && code < 300 {
		dialog.CopyRecordRoute(res, l.req)
	}

	for i := 0; i+1 < len(extra); i += 2 {
		res.Header.Add(extra[i], extra[i+1])
	}

	l.s.Send(l.f.Transport, l.f.Remote, res)
}

// cancelled expects a CANCEL, answers it and the INVITE, and returns the CANCEL's Reason.
func (l *leg) cancelled() string {
	l.t.Helper()

	cancel, f := l.s.RecvRequest()
	if cancel.Method != "CANCEL" {
		l.t.Fatalf("got %s, want a CANCEL", cancel.StartLine())
	}

	l.s.Send(f.Transport, f.Remote, sip.NewResponse(cancel, 200, ""))
	l.respond(487)

	if ack, _ := l.s.RecvRequest(); ack.Method != "ACK" {
		l.t.Fatalf("got %s, want the ACK of the 487", ack.StartLine())
	}

	return cancel.Header.Get("Reason")
}

// RFC 3261 §16.7 step 10, TS 24.229 §5.4.3.3
func TestForkFirstAnswerWins(t *testing.T) {
	s, g := newForkScene(t, []int{2}, proxy.Options{})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(180)

	for range 2 {
		wantResponse(t, s.caller, 180)
	}

	b.respond(200)

	if res := wantResponse(t, s.caller, 200); toTagOf(res) != "b" {
		t.Errorf("200 from %q", toTagOf(res))
	}

	if got, want := a.cancelled(), `SIP;cause=200;text="Call completed elsewhere"`; got != want {
		t.Errorf("CANCEL Reason %q, want %q", got, want)
	}

	s.caller.RecvNone(quiet)
}

// A 2xx from a branch being cancelled still reaches the caller (RFC 3261 §16.7 step 5).
func TestForkSecondAnswer(t *testing.T) {
	s, g := newForkScene(t, []int{2}, proxy.Options{})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	wantResponse(t, s.caller, 180)

	b.respond(200)
	wantResponse(t, s.caller, 200)

	cancel, f := a.s.RecvRequest()
	if cancel.Method != "CANCEL" {
		t.Fatalf("got %s, want a CANCEL", cancel.StartLine())
	}

	a.s.Send(f.Transport, f.Remote, sip.NewResponse(cancel, 200, ""))
	a.respond(200)

	if res := wantResponse(t, s.caller, 200); toTagOf(res) != "a" {
		t.Errorf("200 from %q", toTagOf(res))
	}
}

func toTagOf(res *sip.Response) string {
	to, _ := res.Header.To()
	return to.Tag()
}

// RFC 3261 §16.7 steps 6 and 7
func TestForkBestResponse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		codes []int
		want  int
	}{
		{"lowest class", []int{486, 302, 500}, 302},
		{"first of the class", []int{486, 480}, 486},
		{"resubmission information", []int{486, 484}, 484},
		{"6xx over a lower class", []int{302, 600}, 600},
		{"503 as 500", []int{503}, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, g := newForkScene(t, []int{len(tc.codes)}, proxy.Options{})

			s.send(s.request("INVITE"))

			legs := make([]*leg, len(tc.codes))
			for i := range tc.codes {
				legs[i] = received(t, g[0][i], "t"+strconv.Itoa(i))
			}

			for i, code := range tc.codes {
				legs[i].respond(code)

				if code >= 600 {
					break
				}
			}

			for i, code := range tc.codes {
				if code >= 600 {
					for _, l := range legs[i+1:] {
						l.cancelled()
					}
				}
			}

			if res := s.final(); res.StatusCode != tc.want {
				t.Fatalf("got %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}

func TestForkMergesChallenges(t *testing.T) {
	s, g := newForkScene(t, []int{3}, proxy.Options{})

	s.send(s.request("INVITE"))

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")
	c := received(t, g[0][2], "c")

	a.respond(486)
	b.respond(401, "WWW-Authenticate", `Digest realm="b"`)
	c.respond(407, "Proxy-Authenticate", `Digest realm="c"`)

	res := s.final()
	if res.StatusCode != 401 {
		t.Fatalf("got %d, want 401", res.StatusCode)
	}

	if res.Header.Get("WWW-Authenticate") != `Digest realm="b"` || res.Header.Get("Proxy-Authenticate") != `Digest realm="c"` {
		t.Errorf("challenges %q and %q", res.Header.Get("WWW-Authenticate"), res.Header.Get("Proxy-Authenticate"))
	}
}

// RFC 3261 §16.7 step 5, TS 24.229 §5.4.3.3
func TestForkDeclineEndsTheSearch(t *testing.T) {
	s, g := newForkScene(t, []int{2, 1}, proxy.Options{})

	s.send(s.request("INVITE"))

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(603)

	if got, want := a.cancelled(), `SIP;cause=603;text="Decline"`; got != want {
		t.Errorf("CANCEL Reason %q, want %q", got, want)
	}

	if res := s.final(); res.StatusCode != 603 {
		t.Fatalf("got %d, want 603", res.StatusCode)
	}

	g[1][0].RecvNone(quiet)
}

// RFC 3261 §16.6, RFC 3841 §9.1
func TestForkGroupsInTurn(t *testing.T) {
	s, g := newForkScene(t, []int{2, 1}, proxy.Options{})

	s.send(s.request("INVITE"))
	wantResponse(t, s.caller, 100)

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	g[1][0].RecvNone(quiet)

	a.respond(486)
	g[1][0].RecvNone(quiet)
	s.caller.RecvNone(quiet)

	b.respond(480)

	c := received(t, g[1][0], "c")
	c.respond(200)

	if res := s.final(); res.StatusCode != 200 || toTagOf(res) != "c" {
		t.Fatalf("got %s from %q", res.StartLine(), toTagOf(res))
	}
}

// A group that rings without answer gives way to the next once its no-answer time is over.
func TestForkGroupNoAnswer(t *testing.T) {
	s, g := newForkScene(t, []int{1, 1}, proxy.Options{NoAnswer: 20 * time.Second})

	s.send(s.request("INVITE"))

	a := received(t, g[0][0], "a")
	a.respond(180)
	wantResponse(t, s.caller, 100)
	wantResponse(t, s.caller, 180)

	s.clock.Advance(20 * time.Second)

	if got := a.cancelled(); got != "" {
		t.Errorf("CANCEL Reason %q on a branch that rang out", got)
	}

	b := received(t, g[1][0], "b")
	b.respond(486)

	if res := s.final(); res.StatusCode != 486 {
		t.Fatalf("got %d, want 486 over the 408 of the branch that rang out", res.StatusCode)
	}
}

// RFC 3261 §16.10, RFC 3326
func TestForkCancelledByCaller(t *testing.T) {
	s, g := newForkScene(t, []int{2, 1}, proxy.Options{})

	invite := s.request("INVITE")
	s.send(invite)
	wantResponse(t, s.caller, 100)

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(180)
	wantResponse(t, s.caller, 180)
	wantResponse(t, s.caller, 180)

	cancel, _ := sip.NewCancel(invite)
	cancel.Header.Insert("Reason", `Q.850;cause=16`)
	s.send(cancel)
	wantResponse(t, s.caller, 200)

	for _, l := range []*leg{a, b} {
		if got := l.cancelled(); got != "Q.850;cause=16" {
			t.Errorf("CANCEL Reason %q", got)
		}
	}

	if res := s.final(); res.StatusCode != 487 {
		t.Fatalf("got %d, want 487", res.StatusCode)
	}

	g[1][0].RecvNone(quiet)
}

// RFC 3261 §16.7: the first 2xx to a non-INVITE request is the answer, and no CANCEL follows.
func TestForkMessage(t *testing.T) {
	s, g := newForkScene(t, []int{2}, proxy.Options{})

	s.send(s.request("MESSAGE"))

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	b.respond(202)

	if res := s.final(); res.StatusCode != 202 {
		t.Fatalf("got %d, want 202", res.StatusCode)
	}

	a.respond(200)
	s.caller.RecvNone(quiet)
	a.s.RecvNone(quiet)
}

// RFC 5393 §5.3.3
func TestForkMaxBreadth(t *testing.T) {
	breadth := func(l *leg) int {
		n, err := l.req.Header.MaxBreadth()
		if err != nil {
			t.Fatal(err)
		}

		return n
	}

	t.Run("added", func(t *testing.T) {
		s, g := newForkScene(t, []int{1}, proxy.Options{})

		s.send(s.request("INVITE"))

		if n := breadth(received(t, g[0][0], "a")); n != proxy.MaxBreadth {
			t.Errorf("Max-Breadth %d", n)
		}
	})

	t.Run("capped", func(t *testing.T) {
		s, g := newForkScene(t, []int{1}, proxy.Options{})

		req := s.request("INVITE")
		req.Header.Set("Max-Breadth", "1000")
		s.send(req)

		if n := breadth(received(t, g[0][0], "a")); n != proxy.MaxBreadth {
			t.Errorf("Max-Breadth %d", n)
		}
	})

	t.Run("split", func(t *testing.T) {
		s, g := newForkScene(t, []int{2, 1}, proxy.Options{})

		req := s.request("INVITE")
		req.Header.Set("Max-Breadth", "3")
		s.send(req)

		a := received(t, g[0][0], "a")
		b := received(t, g[0][1], "b")

		if breadth(a)+breadth(b) != 3 || breadth(a) < 1 || breadth(b) < 1 {
			t.Errorf("Max-Breadth %d and %d out of 3", breadth(a), breadth(b))
		}

		a.respond(486)
		b.respond(486)

		// The next group reuses the whole Max-Breadth (RFC 5393 §5.3.3.1).
		if n := breadth(received(t, g[1][0], "c")); n != 3 {
			t.Errorf("Max-Breadth %d in the next group", n)
		}
	})

	t.Run("serialised", func(t *testing.T) {
		s, g := newForkScene(t, []int{3}, proxy.Options{})

		req := s.request("INVITE")
		req.Header.Set("Max-Breadth", "2")
		s.send(req)

		a := received(t, g[0][0], "a")
		b := received(t, g[0][1], "b")
		g[0][2].RecvNone(quiet)

		if breadth(a) != 1 || breadth(b) != 1 {
			t.Errorf("Max-Breadth %d and %d out of 2", breadth(a), breadth(b))
		}

		b.respond(180)
		a.respond(486)

		c := received(t, g[0][2], "c")
		c.respond(200)

		if res := s.final(); res.StatusCode != 200 {
			t.Fatalf("got %d", res.StatusCode)
		}

		if got := b.cancelled(); got == "" {
			t.Error("no Reason on the CANCEL")
		}
	})

	t.Run("invalid", func(t *testing.T) {
		s, _ := newForkScene(t, []int{1}, proxy.Options{})

		req := s.request("INVITE")
		req.Header.Set("Max-Breadth", "0")
		s.send(req)

		if res := s.final(); res.StatusCode != 400 {
			t.Fatalf("got %d, want 400", res.StatusCode)
		}
	})
}

// RFC 5393 §4.2
func TestLoopDetection(t *testing.T) {
	s, g := newForkScene(t, []int{1}, proxy.Options{})

	invite := s.request("INVITE")
	s.send(invite)

	a := received(t, g[0][0], "a")

	// The request comes back with nothing changed that decides where it goes: a loop.
	looped := a.req.Clone()
	looped.URI = invite.URI
	looped.Header.Prepend("Route", invite.Header.Get("Route"))
	looped.Header.Prepend("Via", sip.NewVia(sip.UDP, a.s.Addr()).String())
	looped.Header.Set("CSeq", "2 INVITE")
	a.s.Send(sip.UDP, s.f.local, looped)

	for {
		res, _ := a.s.RecvResponse()
		if res.StatusCode == 100 {
			continue
		}

		if res.StatusCode != 482 {
			t.Fatalf("got %d, want 482", res.StatusCode)
		}

		break
	}

	// Retargeted, it spirals and goes on.
	spiral := looped.Clone()
	spiral.URI, _ = sip.ParseURI("sip:carol@example.com")
	spiral.Header.Set("CSeq", "3 INVITE")

	via, _ := spiral.Header.TopVia()
	via.Params.Set("branch", sip.NewBranch())
	_ = spiral.Header.SetTopVia(via)

	a.s.Send(sip.UDP, s.f.local, spiral)

	for {
		r := a.s.Recv()

		next, ok := r.Msg.(*sip.Request)
		if !ok {
			continue
		}

		if next.Method != "INVITE" || viaCount(next) != 4 {
			t.Fatalf("spiral forwarded as %s with %d Vias", next.StartLine(), viaCount(next))
		}

		break
	}
}
