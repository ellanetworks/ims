package ua_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

type uas struct {
	*raw
	to  netip.AddrPort
	tag string
}

func newUAS(t *testing.T, to *peer) *uas {
	return &uas{raw: newRaw(t), to: to.addr, tag: sip.NewTag()}
}

func (u *uas) uri() string {
	return "sip:bob@" + u.sock.Addr().String()
}

func (u *uas) answer(req *sip.Request, code int, body string, header ...sip.Field) *sip.Response {
	res := sip.NewResponse(req, code, "")

	if code > 100 {
		_ = res.Header.SetToTag(u.tag)
		res.Header.Set("Contact", "<"+u.uri()+">")
	}

	for _, f := range header {
		res.Header.Add(f.Name, f.Value)
	}

	if body != "" {
		res.SetBodies(sdpOf(body)...)
	}

	u.sock.Send(sip.UDP, u.to, res)

	return res
}

func inviteRaw(t *testing.T, alice *peer, u *uas, sdp string, h ua.Handler) (*ua.Outgoing, *sip.Request) {
	t.Helper()

	uri, _ := sip.ParseURI(u.uri())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+u.uri()+">")

	if sdp != "" {
		req.SetBodies(sdpOf(sdp)...)
	}

	o, err := alice.a.Invite(context.Background(), req, h)
	if err != nil {
		t.Fatal(err)
	}

	return o, u.nextRequest("INVITE")
}

// RFC 3261 section 11.2
func TestOptions(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.Allow = nil
		c.OnInvite = func(_ context.Context, r *ua.Request) ua.Handler {
			_ = r.Accept(sdpOf("answer-1")...)
			return ua.Handler{}
		}
	})

	alice.invite(bob, "offer-1")

	as := alice.next("established").s
	alice.next("answer")

	done, f := results()

	if err := as.Send(context.Background(), "OPTIONS", ua.Change{}, f); err != nil {
		t.Fatal(err)
	}

	res := recv(t, done).res
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Allow"), "UPDATE") || res.Header.Get("Accept") != "application/sdp" {
		t.Fatalf("OPTIONS %d Allow %q Accept %q", res.StatusCode, res.Header.Get("Allow"), res.Header.Get("Accept"))
	}

	if strings.Contains(res.Header.Get("Allow"), "INFO") {
		t.Errorf("Allow %q lists INFO, which bob does not handle", res.Header.Get("Allow"))
	}

	if err := as.Send(context.Background(), "INFO", ua.Change{}, f); err != nil {
		t.Fatal(err)
	}

	if res := recv(t, done).res; res.StatusCode != 405 || res.Header.Get("Allow") == "" {
		t.Fatalf("INFO %d", res.StatusCode)
	}

	if as.State() != ua.Confirmed {
		t.Fatalf("session %s after a 405 to INFO", as.State())
	}

	uri, _ := sip.ParseURI(bob.uri())
	options := sip.NewRequest("OPTIONS", uri)
	options.Header.Add("From", "<"+alice.uri()+">")
	options.Header.Add("To", "<"+bob.uri()+">")

	if err := alice.a.Request(context.Background(), options, f); err != nil {
		t.Fatal(err)
	}

	if res := recv(t, done).res; res.StatusCode != 200 || res.Header.Get("Allow") == "" {
		t.Fatalf("OPTIONS outside a dialog %d", res.StatusCode)
	}
}

// RFC 3261 section 13.2.2.4
// RFC 3261 section 15
func TestLateReinvite2xx(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newUAS(t, alice)

	_, invite := inviteRaw(t, alice, bob, "offer-1", alice.handler())
	ok := bob.answer(invite, 200, "answer-1")
	bob.nextRequest("ACK")

	as := alice.next("established").s
	alice.next("answer")

	done, f := results()

	if err := as.Reinvite(context.Background(), ua.Change{Bodies: sdpOf("offer-2")}, f); err != nil {
		t.Fatal(err)
	}

	reinvite := bob.nextRequest("INVITE")

	_ = as.Bye(context.Background(), ua.Change{}, nil)

	bye := bob.nextRequest("BYE")
	bob.answer(bye, 200, "")

	if r := recv(t, done); !errors.Is(r.err, ua.ErrState) {
		t.Fatalf("re-INVITE done %+v, want ErrState", r)
	}

	res := sip.NewResponse(reinvite, 200, "")
	res.Header.Set("To", ok.Header.Get("To"))
	res.Header.Set("Contact", "<"+bob.uri()+">")
	res.SetBodies(sdpOf("answer-2")...)
	bob.sock.Send(sip.UDP, bob.to, res)

	if ack := bob.nextRequest("ACK"); ack.Header.CallID() != invite.Header.CallID() {
		t.Fatal("ACK of another dialog")
	}

	bob.noRequest()
}

// RFC 3261 section 15
func TestShutdownWaitsForTheAck(t *testing.T) {
	answer := make(chan bool, 2)

	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(_ context.Context, r *ua.Request) ua.Handler {
			if <-answer {
				_ = r.Accept(sdpOf("answer-1")...)
			}

			return ua.Handler{}
		}
	})

	alice := newRaw(t)

	answer <- true

	invite := alice.invite(bob, "offer-1")
	ok := alice.response()

	answer <- false

	pending := alice.invite(bob, "offer-1")

	netOf(t).Quiesce()

	closed := make(chan error, 1)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		closed <- bob.a.Shutdown(ctx)
	}()

	for refused := false; !refused; {
		m, got := alice.sock.RecvWithin(wait)
		if !got {
			t.Fatal("the pending INVITE got no final response")
		}

		switch m := m.Msg.(type) {
		case *sip.Request:
			t.Fatalf("%s before the ACK", m.Method)
		case *sip.Response:
			if m.Header.CallID() == pending.Header.CallID() && m.StatusCode >= 200 {
				if m.StatusCode != 503 {
					t.Errorf("pending INVITE %d, want 503", m.StatusCode)
				}

				refused = true
			}
		}
	}

	alice.noRequest()

	alice.ack(invite, ok)

	bye := alice.nextRequest("BYE")

	res := sip.NewResponse(bye, 200, "")
	alice.sock.Send(sip.UDP, bob.addr, res)

	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

// RFC 6141 section 3.3
// RFC 6141 section 3.8
func TestCancelAfterTheChangeApplied(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	as, _ := established(t, alice, bob)

	done, f := results()

	if err := as.Reinvite(context.Background(), ua.Change{Header: sip.Header{{Name: "Supported", Value: "100rel"}}, Bodies: sdpOf("offer-2")}, f); err != nil {
		t.Fatal(err)
	}

	r := bob.next("request").req
	if err := r.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-2")}); err != nil {
		t.Fatal(err)
	}

	alice.next("answer")
	alice.next("progress")
	netOf(t).Quiesce()

	if err := as.CancelReinvite(context.Background(), ua.Change{}); err != nil {
		t.Fatal(err)
	}

	bob.next("cancelled")

	if res := recv(t, done).res; res.StatusCode != 200 {
		t.Fatalf("cancelled re-INVITE %d, want 200", res.StatusCode)
	}

	if l, _, _ := as.Negotiated(); string(l) != "offer-2" {
		t.Errorf("negotiated %q, want the change kept", l)
	}
}

// TS 24.379 clause 6.3.2.2.5.2
func TestAfterPrack(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	alice := newRaw(t)

	invite := alice.invite(bob, "offer-1", sip.Field{Name: "Supported", Value: "100rel"})

	r := bob.next("invite").req
	if err := r.Progress(ua.Reply{Code: 183, Reliable: true}); err != nil {
		t.Fatal(err)
	}

	res := alice.response()

	if err := r.Respond(ua.Reply{Code: 200, Bodies: sdpOf("answer-1"), AfterPrack: true}); err != nil {
		t.Fatal(err)
	}

	for {
		m, ok := alice.sock.RecvWithin(0)
		if !ok {
			break
		}

		if res, ok := m.Msg.(*sip.Response); ok && res.StatusCode == 200 {
			t.Fatal("200 before the PRACK")
		}
	}

	prack := alice.inDialog("PRACK", invite, res, 2)
	rseq, _ := res.Header.RSeq()
	prack.Header.Set("RAck", sip.RAck{RSeq: rseq, CSeq: 1, Method: "INVITE"}.String())
	alice.sock.Send(sip.UDP, bob.addr, prack)

	for {
		res := alice.response()
		if res.StatusCode == 200 && strings.HasSuffix(res.Header.Get("CSeq"), "INVITE") {
			break
		}
	}
}

// RFC 4028 section 7.1
// RFC 4028 section 7.3
func TestMinSE(t *testing.T) {
	alice := newPeerWith(t, "alice", func(c *ua.Config) {
		c.SessionTimer = ua.SessionTimer{Interval: 300 * time.Second, MinSE: 120 * time.Second}
	})
	bob := newUAS(t, alice)

	uri, _ := sip.ParseURI(bob.uri())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+bob.uri()+">")
	req.Header.Add("Supported", "precondition")
	req.SetBodies(sdpOf("offer-1")...)

	if _, err := alice.a.Invite(context.Background(), req, alice.handler()); err != nil {
		t.Fatal(err)
	}

	invite := bob.nextRequest("INVITE")
	if invite.Header.Get("Min-SE") != "120" {
		t.Errorf("Min-SE %q, want 120", invite.Header.Get("Min-SE"))
	}

	for _, tag := range []string{"precondition", "100rel", "timer"} {
		if !invite.Header.HasOptionTag("Supported", tag) {
			t.Errorf("Supported %v lacks %s", invite.Header.Elements("Supported"), tag)
		}
	}

	for _, minSE := range []string{"600", "900"} {
		res := sip.NewResponse(invite, 422, "")
		_ = res.Header.SetToTag(sip.NewTag())
		res.Header.Set("Min-SE", minSE)
		bob.sock.Send(sip.UDP, bob.to, res)
		bob.nextRequest("ACK")

		invite = bob.nextRequest("INVITE")
		if invite.Header.Get("Min-SE") != minSE || !strings.HasPrefix(invite.Header.Get("Session-Expires"), minSE) {
			t.Fatalf("after a 422 with %s: Min-SE %q, Session-Expires %q", minSE, invite.Header.Get("Min-SE"),
				invite.Header.Get("Session-Expires"))
		}
	}

	res := sip.NewResponse(invite, 422, "")
	_ = res.Header.SetToTag(sip.NewTag())
	res.Header.Set("Min-SE", "900")
	bob.sock.Send(sip.UDP, bob.to, res)
	bob.nextRequest("ACK")

	if e := alice.next("failed"); e.res == nil || e.res.StatusCode != 422 {
		t.Fatalf("failed %+v", e)
	}
}

// RFC 6228 section 4
// RFC 6228 section 8
func TestReliable199WithoutContact(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newUAS(t, alice)

	_, invite := inviteRaw(t, alice, bob, "", alice.handler())

	res := sip.NewResponse(invite, 199, "")
	_ = res.Header.SetToTag(sip.NewTag())
	res.Header.Set("Require", "100rel")
	res.Header.Set("RSeq", "1")
	res.SetBodies(sdpOf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n")...)
	bob.sock.Send(sip.UDP, bob.to, res)

	prack := bob.nextRequest("PRACK")
	if prack.URI.String() != invite.URI.String() {
		t.Errorf("PRACK to %s, want the Request-URI %s", prack.URI, invite.URI)
	}

	if prack.Header.Get("RAck") != "1 1 INVITE" || sessionBodyOf(prack) == nil {
		t.Errorf("PRACK RAck %q, answer %q", prack.Header.Get("RAck"), sessionBodyOf(prack))
	}
}

// RFC 3261 section 13.2.2.4
func TestOfferOf2xx(t *testing.T) {
	clock := netOf(t).Clock()
	alice := newPeer(t, "alice", clock, ua.SessionTimer{})
	bob := newUAS(t, alice)

	_, invite := inviteRaw(t, alice, bob, "", alice.handler())
	bob.answer(invite, 200, realSDP(5000, 1))

	alice.next("established")
	alice.next("offer")

	advance(clock, 32*transaction.DefaultT1, alice)

	ack := bob.nextRequest("ACK")
	if sessionBodyOf(ack) == nil {
		t.Error("ACK without an answer")
	}

	bob.nextRequest("BYE")

	if e := alice.next("ended"); !errors.Is(e.end.Err, ua.ErrNoAnswer) {
		t.Errorf("ended %+v", e.end)
	}

	carol := newUAS(t, alice)
	_, invite = inviteRaw(t, alice, carol, realSDP(4000, 1), alice.handler())
	ok := carol.answer(invite, 200, realSDP(5000, 1))
	carol.nextRequest("ACK")

	as := alice.next("established").s
	alice.next("answer")

	done, f := results()

	if err := as.Reinvite(context.Background(), ua.Change{}, f); err != nil {
		t.Fatal(err)
	}

	reinvite := carol.nextRequest("INVITE")

	res := sip.NewResponse(reinvite, 200, "")
	res.Header.Set("To", ok.Header.Get("To"))
	res.Header.Set("Contact", "<"+carol.uri()+">")
	res.SetBodies(sdpOf(realSDP(5000, 2))...)
	carol.sock.Send(sip.UDP, carol.to, res)

	alice.next("offer").offer.Reject(context.Background())

	if ack := carol.nextRequest("ACK"); sessionBodyOf(ack) == nil {
		t.Error("ACK without the refusal")
	}

	recv(t, done)
	carol.noRequest()

	if as.State() != ua.Confirmed {
		t.Errorf("session %s after refusing the offer of a re-INVITE", as.State())
	}
}

func TestCancelAfterTheAnswerAndChallengedBye(t *testing.T) {
	alice := newPeerWith(t, "alice", func(c *ua.Config) {
		c.Authenticate = func(req *sip.Request, _ *sip.Response) error {
			req.Header.Set("Authorization", `Digest username="alice"`)
			return nil
		}
	})
	bob := newUAS(t, alice)

	o, invite := inviteRaw(t, alice, bob, "offer-1", alice.handler())
	bob.answer(invite, 200, "answer-1")
	bob.nextRequest("ACK")

	as := alice.next("established").s
	alice.next("answer")

	if err := o.Cancel(context.Background(), ua.Change{}); !errors.Is(err, ua.ErrState) {
		t.Errorf("Cancel after the 2xx: %v, want ErrState", err)
	}

	done, f := results()

	if err := as.Bye(context.Background(), ua.Change{}, f); err != nil {
		t.Fatal(err)
	}

	bye := bob.nextRequest("BYE")
	bob.answer(bye, 401, "", sip.Field{Name: "WWW-Authenticate", Value: `Digest realm="x", nonce="y"`})

	bye = bob.nextRequest("BYE")
	if bye.Header.Get("Authorization") == "" {
		t.Fatal("BYE sent again without credentials")
	}

	bob.answer(bye, 200, "")

	if res := recv(t, done).res; res.StatusCode != 200 {
		t.Fatalf("BYE %d", res.StatusCode)
	}
}

func TestFailedBeforeEnded(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	_ = r.Progress(ua.Reply{Code: 180})

	alice.next("progress")

	_ = r.Reject(486)

	alice.next("failed")

	e := alice.next("ended")
	if e.end.Cause != ua.EndRejected || e.end.Response.StatusCode != 486 {
		t.Fatalf("ended %+v", e.end)
	}

	if rs := e.end.Reasons(); len(rs) != 1 || !strings.HasPrefix(rs[0].String(), "SIP;cause=486") {
		t.Errorf("reasons %v", rs)
	}
}
