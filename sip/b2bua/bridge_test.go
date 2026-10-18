package b2bua_test

import (
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/ua"
)

// A call bridged from alice to bob: the offer of alice reaches bob, his 180 and 200 with the answer reach her; her
// BYE reaches bob, and its 200 comes back once he answered his (TS 24.379 §6.3.2.1.6); the call then ends.
func TestBridgeCall(t *testing.T) {
	n := newNet(t)

	var held chan *ua.Request

	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{Copy: b2bua.DefaultCopy})
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler(), sip.Field{Name: "Resource-Priority", Value: "mcpttp.4"},
		sip.Field{Name: "X-Private", Value: "stays"})

	in := bob.next("invite").req
	if !hasSDP(in.Message(), "offer-1") {
		t.Fatalf("bob's INVITE without the offer: %q", in.Message().Body)
	}

	if in.Message().Header.Get("Resource-Priority") != "mcpttp.4" || in.Message().Header.Has("X-Private") {
		t.Errorf("bob's INVITE header %v", in.Message().Header)
	}

	held = make(chan *ua.Request, 1)
	h := bob.handler()
	h.Bye = func(_ *ua.Session, r *ua.Request) { held <- r }
	in.Session().SetHandler(h)

	_ = in.Progress(ua.Reply{Code: 180})

	if e := alice.next("progress"); e.res.StatusCode != 180 {
		t.Fatalf("alice got %d", e.res.StatusCode)
	}

	_ = in.Accept(sdpOf("answer-1")...)

	e := alice.next("established")
	as := e.s

	if !hasSDP(e.msg, "answer-1") {
		t.Errorf("alice's 200 without the answer")
	}

	alice.next("answer")
	bob.next("established")

	done := make(chan *sip.Response, 1)
	_ = as.Bye(ua.Change{}, func(res *sip.Response, _ error) { done <- res })

	r := <-held

	n.Quiesce()

	select {
	case <-done:
		t.Fatal("alice's BYE answered before bob's")
	default:
	}

	_ = r.Respond(ua.Reply{Code: 200})

	n.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 200 {
		t.Fatalf("alice's BYE: %v", res)
	}

	b.callEnded(t)
}

// RFC 3262: the answer of bob in a reliable 183 reaches alice in a reliable 183, PRACKed on each leg; his 200 then
// carries no session description to her, and waits for her PRACK (TS 24.379 §6.3.2.2.5.2 step 2b).
func TestBridgeReliableAnswer(t *testing.T) {
	n := newNet(t)

	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{AfterPrack: true})
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	in := bob.next("invite").req
	_ = in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")})

	alice.next("answer")

	e := alice.next("progress")
	if e.res.StatusCode != 183 || !e.res.Header.HasOptionTag("Require", sip.OptionTag100rel) ||
		!hasSDP(e.res, "answer-1") {
		t.Fatalf("alice's 183: %d %v", e.res.StatusCode, e.res.Header)
	}

	_ = in.Accept()

	e = alice.next("established")
	if part, ok, _ := e.msg.Env().Content().FindFunc(sip.BodyPart.IsSession); ok {
		t.Errorf("alice's 200 with a session description %q", part.Body)
	}

	bob.next("established")
}

// RFC 3261 §13.2.1: alice's INVITE without an offer: bob's offer in his 200 reaches her in hers, and her answer in
// the ACK reaches him in his.
func TestBridgeOfferIn2xx(t *testing.T) {
	n := newNet(t)

	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{})
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "", alice.handler())

	in := bob.next("invite").req
	if _, ok := in.Offer(); ok {
		t.Fatal("bob's INVITE with an offer")
	}

	_ = in.Accept(sdpOf("offer-b")...)

	alice.next("established")

	o := alice.next("offer")
	if string(o.sdp) != "offer-b" {
		t.Fatalf("alice got offer %q", o.sdp)
	}

	_ = o.offer.Answer(ua.Change{Bodies: sdpOf("answer-a")})

	if e := bob.next("answer"); string(e.sdp) != "answer-a" {
		t.Fatalf("bob got answer %q", e.sdp)
	}

	bob.next("established")
}

// RFC 3262 §5: an offer of bob in a reliable 183, to alice's INVITE without one, reaches her in a reliable 183;
// her answer in the PRACK reaches him in his.
func TestBridgeOfferInReliable1xx(t *testing.T) {
	n := newNet(t)

	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{})
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "", alice.handler())

	in := bob.next("invite").req
	_ = in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("offer-b")})

	o := alice.next("offer")
	if string(o.sdp) != "offer-b" {
		t.Fatalf("alice got offer %q", o.sdp)
	}

	_ = o.offer.Answer(ua.Change{Bodies: sdpOf("answer-a")})

	if e := bob.next("answer"); string(e.sdp) != "answer-a" {
		t.Fatalf("bob got answer %q", e.sdp)
	}

	alice.next("progress")

	_ = in.Accept()

	alice.next("established")
	bob.next("established")
}

// The CANCEL of alice cancels bob's INVITE, with her Reason (RFC 3326); his 487 does not reach her, whose INVITE
// the agent answered with a 487 already.
func TestBridgeCancel(t *testing.T) {
	n := newNet(t)

	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{Copy: b2bua.DefaultCopy})
	alice := newParty(t, n, "alice", nil)

	o := alice.invite(b.uri(), "offer-1", alice.handler())

	in := bob.next("invite").req
	_ = in.Progress(ua.Reply{Code: 180})

	alice.next("progress")

	reason, _ := sip.NewReason("SIP", 200, "Call completed elsewhere")
	_ = o.Cancel(ua.Reasons(reason))

	if e := alice.next("failed"); e.res == nil || e.res.StatusCode != 487 {
		t.Fatalf("alice's INVITE: %+v", e.res)
	}

	alice.next("ended")

	e := bob.next("ended")
	if e.end.Cause != ua.EndCancelled || e.end.Request == nil {
		t.Fatalf("bob ended %+v", e.end)
	}

	if got := e.end.Request.Header.Get("Reason"); got == "" {
		t.Error("CANCEL without the Reason of alice")
	}

	b.callEnded(t)
}

// A failure of bob reaches alice with its Warning; a 503 as a 500 (RFC 3261 §16.7 step 6).
func TestBridgeFailure(t *testing.T) {
	for _, c := range []struct{ code, want int }{{486, 486}, {503, 500}, {302, 480}} {
		n := newNet(t)

		bob := newParty(t, n, "bob", nil)
		b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{Copy: b2bua.DefaultCopy})
		alice := newParty(t, n, "alice", nil)

		alice.invite(b.uri(), "offer-1", alice.handler())

		_ = bob.next("invite").req.Reject(c.code, sip.Field{Name: "Warning", Value: `399 bob "busy here"`})

		e := alice.next("failed")
		if e.res == nil || e.res.StatusCode != c.want {
			t.Fatalf("bob's %d: alice got %+v", c.code, e.res)
		}

		if c.want == c.code && e.res.Header.Get("Warning") != `399 bob "busy here"` {
			t.Errorf("bob's %d: Warning %q", c.code, e.res.Header.Get("Warning"))
		}

		b.callEnded(t)
	}
}
