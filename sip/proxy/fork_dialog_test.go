package proxy_test

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/siptest"
)

func (s *forkScene) nextDialog() *proxy.Dialog {
	s.t.Helper()

	select {
	case d := <-s.f.dialogs:
		return d
	case <-time.After(siptest.Timeout):
		s.t.Fatal("no dialog started")
		return nil
	}
}

func (s *forkScene) nextEvent(kind proxy.EventKind) proxy.DialogEvent {
	s.t.Helper()

	for {
		select {
		case e := <-s.f.events:
			if e.Kind == proxy.EventStarted && kind != proxy.EventStarted {
				continue
			}

			if e.Kind != kind {
				s.t.Fatalf("got dialog event %+v, want kind %d", e, kind)
			}

			return e
		case <-time.After(siptest.Timeout):
			s.t.Fatalf("no dialog event of kind %d", kind)
			return proxy.DialogEvent{}
		}
	}
}

func (s *forkScene) noEvent() {
	s.t.Helper()

	for {
		select {
		case e := <-s.f.events:
			if e.Kind == proxy.EventStarted {
				continue
			}

			s.t.Fatalf("unexpected dialog event %+v", e)
		case <-time.After(quiet):
			return
		}
	}
}

// invite199 is an INVITE whose caller takes 199 responses (RFC 6228 §4).
func (s *forkScene) invite199() *sip.Request {
	req := s.request("INVITE")
	req.Header.Add("Supported", "199")

	return req
}

// next199 receives the next response other than 100 and wants it a 199.
func (s *forkScene) next199() *sip.Response {
	s.t.Helper()

	for {
		res, _ := s.caller.RecvResponse()

		switch res.StatusCode {
		case 100, 180, 183:
			continue
		case 199:
			return res
		}

		s.t.Fatalf("got %s, want a 199", res.StartLine())
	}
}

// RFC 6228 §6, TS 24.229 §5.4.3.3
func TestForkedDialogBranchFails(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{2}, proxy.Options{}, true)

	s.send(s.invite199())
	d := s.nextDialog()

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(183)
	b.respond(180)

	a.respond(486)

	r199 := s.next199()
	if toTagOf(r199) != "a" || r199.Header.Get("Reason") != `SIP;cause=486;text="Busy Here"` {
		t.Errorf("199 to %q with Reason %q", toTagOf(r199), r199.Header.Get("Reason"))
	}

	if has := strings.Contains(r199.Header.Get("Supported")+r199.Header.Get("Require"), "199"); has {
		t.Error("199 option-tag in the 199")
	}

	s.noEvent()

	if d.State() != proxy.Early {
		t.Fatalf("dialog %s after a failed branch", d.State())
	}

	b.respond(200)

	if res := s.final(); res.StatusCode != 200 || toTagOf(res) != "b" {
		t.Fatalf("got %s from %q", res.StartLine(), toTagOf(res))
	}

	if e := s.nextEvent(proxy.EventAnswered); e.Code != 200 {
		t.Errorf("answered with %d", e.Code)
	}

	if got := d.Contact(proxy.Callee); got.User != "b" {
		t.Errorf("callee contact %s", got)
	}

	// A release reaches the callee that answered, on its branch.
	if err := d.Release(proxy.Release{Toward: proxy.Callee}); err != nil {
		t.Fatal(err)
	}

	ack, _ := g[0][1].RecvRequest()
	bye, _ := g[0][1].RecvRequest()

	if ack.Method != "ACK" || bye.Method != "BYE" || toTagOfRequest(bye) != "b" {
		t.Fatalf("callee b got %s and %s", ack.StartLine(), bye.StartLine())
	}
}

func toTagOfRequest(req *sip.Request) string {
	to, _ := req.Header.To()
	return to.Tag()
}

// The dialog fails with the INVITE, once every branch failed, on the best final response.
func TestForkedDialogAllFail(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{2}, proxy.Options{}, true)

	s.send(s.invite199())
	d := s.nextDialog()

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(180)

	a.respond(480)
	s.next199()
	s.noEvent()

	b.respond(486)

	if res := s.final(); res.StatusCode != 480 {
		t.Fatalf("got %d, want 480", res.StatusCode)
	}

	if e := s.nextEvent(proxy.EventEnded); e.Code != 480 || e.By != proxy.Callee || e.End != proxy.EndFailed {
		t.Errorf("ended %+v", e)
	}

	if d.State() != proxy.Ended {
		t.Errorf("dialog %s", d.State())
	}
}

func TestForkedDialogDeclined(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{2}, proxy.Options{}, true)

	s.send(s.invite199())
	s.nextDialog()

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(180)

	b.respond(603)

	if got := s.next199(); toTagOf(got) != "b" {
		t.Errorf("199 to %q", toTagOf(got))
	}

	a.cancelled()

	if res := s.final(); res.StatusCode != 603 {
		t.Fatalf("got %d, want 603", res.StatusCode)
	}

	if e := s.nextEvent(proxy.EventEnded); e.Code != 603 || e.By != proxy.Callee {
		t.Errorf("ended %+v", e)
	}
}

func TestForkedDialogCancelled(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{2}, proxy.Options{}, true)

	invite := s.invite199()
	s.send(invite)
	s.nextDialog()

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(180)

	cancel, _ := sip.NewCancel(invite)
	s.send(cancel)

	a.cancelled()
	s.noEvent()
	b.cancelled()

	if res := s.final(); res.StatusCode != 487 {
		t.Fatalf("got %d, want 487", res.StatusCode)
	}

	if e := s.nextEvent(proxy.EventEnded); e.Code != 487 || e.By != proxy.Caller {
		t.Errorf("ended %+v", e)
	}
}

// RFC 6228 §6: no 199 when the caller does not take them, requires reliable provisional responses,
// or the final response goes upstream at once.
func TestNo199(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*sip.Request)
	}{
		{"unsupported", func(r *sip.Request) { r.Header.Del("Supported") }},
		{"100rel required", func(r *sip.Request) { r.Header.Add("Require", "100rel") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, g := newForkScene(t, []int{2}, proxy.Options{})

			req := s.invite199()
			tc.edit(req)
			s.send(req)

			a := received(t, g[0][0], "a")
			b := received(t, g[0][1], "b")

			a.respond(180)
			a.respond(486)
			b.respond(486)

			for {
				res, _ := s.caller.RecvResponse()
				if res.StatusCode == 199 {
					t.Fatal("got a 199")
				}

				if res.StatusCode >= 200 {
					break
				}
			}
		})
	}

	t.Run("final forwarded at once", func(t *testing.T) {
		s, g := newForkScene(t, []int{1}, proxy.Options{})

		s.send(s.invite199())

		a := received(t, g[0][0], "a")
		a.respond(180)
		a.respond(486)

		for {
			res, _ := s.caller.RecvResponse()
			if res.StatusCode == 199 {
				t.Fatal("got a 199")
			}

			if res.StatusCode >= 200 {
				break
			}
		}
	})
}

// A device that rang out before the next group gets its early dialog ended with a 199.
func TestForkGroupStep199(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{1, 1}, proxy.Options{NoAnswer: 20 * time.Second}, true)

	s.send(s.invite199())
	d := s.nextDialog()

	a := received(t, g[0][0], "a")
	a.respond(180)
	s.relayed(180)

	s.clock.Advance(20 * time.Second)
	a.cancelled()

	if r := s.next199(); toTagOf(r) != "a" || r.Header.Get("Reason") != `SIP;cause=408;text="Request Timeout"` {
		t.Errorf("199 to %q with Reason %q", toTagOf(r), r.Header.Get("Reason"))
	}

	b := received(t, g[1][0], "b")
	b.respond(200)

	if res := s.final(); res.StatusCode != 200 || toTagOf(res) != "b" {
		t.Fatalf("got %s", res.StartLine())
	}

	s.nextEvent(proxy.EventAnswered)

	if got := d.Contact(proxy.Callee); got.User != "b" {
		t.Errorf("callee contact %s", got)
	}
}

// RFC 3261 §13.2.2.4: the caller ends a second 2xx with an ACK and a BYE that go through the proxy
// to the callee that sent it, leaving the dialog to the first.
func TestForkedDialogSecondAnswer(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{2}, proxy.Options{}, true)

	invite := s.invite199()
	s.send(invite)
	d := s.nextDialog()

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(200)

	first := s.final()
	s.nextEvent(proxy.EventAnswered)

	cancel, f := a.s.RecvRequest()
	a.s.Send(f.Transport, f.Remote, sip.NewResponse(cancel, 200, ""))
	a.respond(200)

	second := s.final()
	if toTagOf(first) != "b" || toTagOf(second) != "a" {
		t.Fatalf("answers from %q then %q", toTagOf(first), toTagOf(second))
	}

	for _, res := range []*sip.Response{first, second} {
		s.caller.Send(sip.UDP, s.f.local, s.inDialog("ACK", invite, res, 1))
	}

	s.caller.Send(sip.UDP, s.f.local, s.inDialog("BYE", invite, second, 2))

	if got, _ := g[0][1].RecvRequest(); got.Method != "ACK" {
		t.Fatalf("callee b got %s", got.StartLine())
	}

	var (
		bye  *sip.Request
		bf   sip.Flow
		acks int
	)

	for range 2 {
		req, f := g[0][0].RecvRequest()

		switch req.Method {
		case "ACK":
			acks++
		case "BYE":
			bye, bf = req, f
		default:
			t.Fatalf("callee a got %s", req.StartLine())
		}
	}

	if acks != 1 || bye == nil {
		t.Fatalf("callee a got %d ACKs and BYE %v", acks, bye != nil)
	}

	g[0][0].Send(bf.Transport, bf.Remote, sip.NewResponse(bye, 200, ""))

	if res := s.final(); res.StatusCode != 200 {
		t.Fatalf("BYE answered %d", res.StatusCode)
	}

	s.noEvent()

	if d.State() != proxy.Confirmed || d.Contact(proxy.Callee).User != "b" {
		t.Errorf("dialog %s with %s", d.State(), d.Contact(proxy.Callee))
	}
}

// inDialog builds a request of the caller in the dialog the response res created (RFC 3261 §12.2.1.1).
func (s *forkScene) inDialog(method string, invite *sip.Request, res *sip.Response, seq uint32) *sip.Request {
	s.t.Helper()

	contacts, err := res.Header.Contacts()
	if err != nil || len(contacts) == 0 {
		s.t.Fatalf("no Contact in %s", res.StartLine())
	}

	req := sip.NewRequest(method, contacts[0].URI)
	req.Header = nil

	via := sip.NewVia(sip.UDP, s.caller.Addr())
	req.Header.Add("Via", via.String())
	req.Header.Add("Max-Forwards", "70")

	rr := res.Header.Values("Record-Route")
	for i := len(rr) - 1; i >= 0; i-- {
		req.Header.Add("Route", rr[i])
	}

	req.Header.Add("From", invite.Header.Get("From"))
	req.Header.Add("To", res.Header.Get("To"))
	req.Header.Add("Call-ID", invite.Header.CallID())

	if method == "ACK" {
		seq = 1
	}

	req.Header.Add("CSeq", sip.CSeq{Seq: seq, Method: method}.String())
	req.Header.Add("Content-Length", "0")

	return req
}

func released480() proxy.Release {
	r, _ := sip.NewReason("SIP", 480, "Temporarily Unavailable")
	return proxy.Release{Toward: proxy.Both, Reason: []sip.Reason{r}, Code: 480, ResponseReason: []sip.Reason{r}}
}

const reason480 = `SIP;cause=480;text="Temporarily Unavailable"`

// Releasing one callee of an early forked dialog cancels its branch only; the others ring on.
func TestReleaseCalleeEarly(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{2}, proxy.Options{}, true)

	s.send(s.invite199())
	d := s.nextDialog()

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(180)

	if err := d.ReleaseCallee(g[0][0].Addr(), released480()); err != nil {
		t.Fatal(err)
	}

	if reason := a.cancelled(); reason != reason480 {
		t.Errorf("CANCEL Reason %q", reason)
	}

	if r := s.next199(); toTagOf(r) != "a" {
		t.Errorf("199 to %q", toTagOf(r))
	}

	s.noEvent()

	if d.State() != proxy.Early || d.Released() {
		t.Fatalf("dialog %s, released %v", d.State(), d.Released())
	}

	b.respond(200)

	if res := s.final(); res.StatusCode != 200 || toTagOf(res) != "b" {
		t.Fatalf("got %s from %q", res.StartLine(), toTagOf(res))
	}

	s.nextEvent(proxy.EventAnswered)
}

// Releasing the last callee left to answer releases the whole dialog.
func TestReleaseCalleeLast(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{2}, proxy.Options{}, true)

	s.send(s.invite199())
	d := s.nextDialog()

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(180)
	b.respond(486)
	s.next199()

	if err := d.ReleaseCallee(g[0][0].Addr(), released480()); err != nil {
		t.Fatal(err)
	}

	if res := s.final(); res.StatusCode != 480 || res.Header.Get("Reason") != reason480 {
		t.Fatalf("got %s with Reason %q", res.StartLine(), res.Header.Get("Reason"))
	}

	if reason := a.cancelled(); reason != reason480 {
		t.Errorf("CANCEL Reason %q", reason)
	}

	if e := s.nextEvent(proxy.EventEnded); e.End != proxy.EndReleased {
		t.Errorf("ended %+v", e)
	}
}

// A callee of a group not yet reached never gets the request.
func TestReleaseCalleePending(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{1, 1}, proxy.Options{}, true)

	s.send(s.invite199())
	d := s.nextDialog()

	a := received(t, g[0][0], "a")
	a.respond(180)
	s.relayed(180)

	if err := d.ReleaseCallee(g[1][0].Addr(), released480()); err != nil {
		t.Fatal(err)
	}

	a.respond(486)

	if res := s.final(); res.StatusCode != 486 {
		t.Fatalf("got %s", res.StartLine())
	}

	g[1][0].RecvNone(quiet)
}

// Once answered, the dialog reaches only the callee that answered; releasing it sends the BYE.
func TestReleaseCalleeAnswered(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{2}, proxy.Options{}, true)

	invite := s.invite199()
	s.send(invite)
	d := s.nextDialog()

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(200)

	res := s.final()
	s.nextEvent(proxy.EventAnswered)
	a.cancelled()
	s.caller.Send(sip.UDP, s.f.local, s.inDialog("ACK", invite, res, 1))

	if got, _ := g[0][1].RecvRequest(); got.Method != "ACK" {
		t.Fatalf("callee b got %s", got.StartLine())
	}

	for _, key := range []any{g[0][0].Addr(), "unknown"} {
		if err := d.ReleaseCallee(key, released480()); !errors.Is(err, proxy.ErrNotReached) {
			t.Errorf("release of %v: %v", key, err)
		}
	}

	if err := d.ReleaseCallee(g[0][1].Addr(), released480()); err != nil {
		t.Fatal(err)
	}

	bye, _ := g[0][1].RecvRequest()
	if bye.Method != "BYE" || bye.Header.Get("Reason") != reason480 {
		t.Fatalf("callee b got %s with Reason %q", bye.StartLine(), bye.Header.Get("Reason"))
	}

	if req, _ := s.caller.RecvRequest(); req.Method != "BYE" {
		t.Fatalf("caller got %s", req.StartLine())
	}

	if e := s.nextEvent(proxy.EventEnded); e.End != proxy.EndReleased {
		t.Errorf("ended %+v", e)
	}

	if err := d.ReleaseCallee(g[0][1].Addr(), released480()); !errors.Is(err, proxy.ErrDialogEnded) {
		t.Errorf("second release: %v", err)
	}
}

// TS 24.229 §5.4.5.1.1: a withdrawn branch stands for its release, a 480, not for a CANCEL of the
// caller's: its early dialog ends with a 199 for 480, and the caller gets the other branches' answer.
func TestReleaseCalleeThenOthersFail(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{2}, proxy.Options{}, true)

	s.send(s.invite199())
	d := s.nextDialog()

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(180)

	if err := d.ReleaseCallee(g[0][0].Addr(), released480()); err != nil {
		t.Fatal(err)
	}

	a.cancelled()

	if r := s.next199(); toTagOf(r) != "a" || r.Header.Get("Reason") != reason480 {
		t.Errorf("199 to %q with Reason %q, want a with %q", toTagOf(r), r.Header.Get("Reason"), reason480)
	}

	b.respond(486)

	if res := s.final(); res.StatusCode != 486 {
		t.Fatalf("got %s, want the 486 of the branch left", res.StartLine())
	}
}

// When the withdrawn branch is the best answer left, the caller gets its 480 and Reason.
func TestReleaseCalleeOnlyAnswer(t *testing.T) {
	s, g := newForkSceneTracking(t, []int{2}, proxy.Options{}, true)

	s.send(s.invite199())
	d := s.nextDialog()

	a := received(t, g[0][0], "a")
	b := received(t, g[0][1], "b")

	a.respond(180)
	b.respond(180)

	if err := d.ReleaseCallee(g[0][0].Addr(), released480()); err != nil {
		t.Fatal(err)
	}

	a.cancelled()
	s.next199()

	b.respond(503)

	if res := s.final(); res.StatusCode != 480 || res.Header.Get("Reason") != reason480 {
		t.Fatalf("got %s with Reason %q, want 480 with %q", res.StartLine(), res.Header.Get("Reason"), reason480)
	}
}

// A callee that is only the 430 retry of a branch not yet sent is withdrawn too: the branch fails
// without trying it.
func TestReleaseCalleeRetryOfPendingBranch(t *testing.T) {
	flow := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	s, g := newForkSceneWith(t, []int{1, 1}, proxy.Options{}, true, func(f *forker) {
		f.retry, f.retryGroup = []*siptest.Socket{flow}, 1
	})

	s.send(s.invite199())
	d := s.nextDialog()

	a := received(t, g[0][0], "a")
	a.respond(180)

	if err := d.ReleaseCallee(flow.Addr(), released480()); err != nil {
		t.Fatalf("ReleaseCallee: %v", err)
	}

	a.respond(486)

	b := received(t, g[1][0], "b")
	b.respond(430)

	s.final()
	flow.RecvNone(quiet)
}
