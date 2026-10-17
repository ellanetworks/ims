package ua_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

// nextRequest is the next request the raw peer receives, other than retransmissions of responses it ignores.
func (r *raw) nextRequest(method string) *sip.Request {
	r.t.Helper()

	deadline := time.Now().Add(wait)

	for {
		m, ok := r.sock.RecvWithin(time.Until(deadline))
		if !ok {
			r.t.Fatalf("no %s", method)
		}

		if req, ok := m.Msg.(*sip.Request); ok {
			if req.Method != method {
				r.t.Fatalf("%s, want %s", req.Method, method)
			}

			return req
		}
	}
}

// noRequest checks that no request comes for a while, responses aside.
func (r *raw) noRequest(d time.Duration) {
	r.t.Helper()

	deadline := time.Now().Add(d)

	for {
		m, ok := r.sock.RecvWithin(time.Until(deadline))
		if !ok {
			return
		}

		if req, ok := m.Msg.(*sip.Request); ok {
			r.t.Fatalf("unexpected %s", req.Method)
		}
	}
}

// RFC 3311 §5.1: the callee sends an UPDATE with an offer once the reliable 1xx with its answer is PRACKed. Asked
// for earlier, the UPDATE waits for the PRACK instead of crossing it.
func TestCalleeUpdateWaitsForPrack(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	alice := newRaw(t)

	invite := alice.invite(bob, "offer-1", sip.Field{Name: "Supported", Value: "100rel"})

	r := bob.next("invite").req
	if err := r.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")}); err != nil {
		t.Fatal(err)
	}

	res := alice.response()
	if res.StatusCode != 183 {
		t.Fatalf("%d, want 183", res.StatusCode)
	}

	done, f := results()

	if err := r.Session().Update(ua.Change{Bodies: sdpOf("offer-2")}, f); err != nil {
		t.Fatal(err)
	}

	alice.noRequest(300 * time.Millisecond)

	prack := alice.inDialog("PRACK", invite, res, 2)
	rseq, _ := res.Header.RSeq()
	prack.Header.Set("RAck", sip.RAck{RSeq: rseq, CSeq: 1, Method: "INVITE"}.String())
	alice.sock.Send(sip.UDP, bob.addr, prack)

	update := alice.nextRequest("UPDATE")
	if string(sessionBodyOf(update)) != "offer-2" {
		t.Fatalf("UPDATE offers %q", sessionBodyOf(update))
	}

	ok := sip.NewResponse(update, 200, "")
	ok.SetBodies(sdpOf("answer-2")...)
	alice.sock.Send(sip.UDP, bob.addr, ok)

	if res := recv(t, done).res; res.StatusCode != 200 {
		t.Fatalf("UPDATE %d", res.StatusCode)
	}
}

func sessionBodyOf(req *sip.Request) []byte {
	p, _, _ := req.Content().FindFunc(sip.BodyPart.IsSession)
	return p.Body
}

// RFC 3311 §5.2: one UPDATE of the agent at a time; a second waits for the first, which the peer would otherwise
// refuse with a 500.
func TestOneUpdateAtATime(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), serverPeer(t, "bob")
	as, _ := established(t, alice, bob)

	first, f1 := results()
	second, f2 := results()

	if err := as.Update(ua.Change{Header: sip.Header{{Name: "X-N", Value: "1"}}}, f1); err != nil {
		t.Fatal(err)
	}

	if err := as.Update(ua.Change{Header: sip.Header{{Name: "X-N", Value: "2"}}}, f2); err != nil {
		t.Fatal(err)
	}

	r := bob.next("request").req
	if r.Message().Header.Get("X-N") != "1" {
		t.Fatalf("first UPDATE %q", r.Message().Header.Get("X-N"))
	}

	bob.none()

	_ = r.AnswerUnchanged(ua.Change{})

	if res := recv(t, first).res; res.StatusCode != 200 {
		t.Fatalf("first %d", res.StatusCode)
	}

	r = bob.next("request").req
	if r.Message().Header.Get("X-N") != "2" {
		t.Fatalf("second UPDATE %q", r.Message().Header.Get("X-N"))
	}

	_ = r.AnswerUnchanged(ua.Change{})

	if res := recv(t, second).res; res.StatusCode != 200 {
		t.Fatalf("second %d", res.StatusCode)
	}
}

// RFC 3261 §14.1: a re-INVITE of the agent waits for the INVITE transaction of the peer in progress, instead of a
// glare. One without an offer is still good once the peer's exchange completed; a fixed offer would be stale.
func TestReinviteWaitsForThePeers(t *testing.T) {
	alice, bob := serverPeer(t, "alice"), newPeer(t, "bob", nil, ua.SessionTimer{})
	as, bs := established(t, alice, bob)

	bobs, fb := results()

	if err := bs.Reinvite(ua.Change{Bodies: sdpOf("offer-b")}, fb); err != nil {
		t.Fatal(err)
	}

	r := alice.next("request").req

	stale, fs := results()
	asking, fa := results()

	if err := as.Reinvite(ua.Change{Bodies: sdpOf("offer-a")}, fs); err != nil {
		t.Fatal(err)
	}

	if err := as.Reinvite(ua.Change{}, fa); err != nil {
		t.Fatal(err)
	}

	bob.none()

	_ = r.Accept(sdpOf("answer-b")...)

	if res := recv(t, bobs).res; res.StatusCode != 200 {
		t.Fatalf("bob's re-INVITE %d", res.StatusCode)
	}

	bob.next("answer")

	if res := recv(t, stale); !errors.Is(res.err, ua.ErrStale) {
		t.Fatalf("alice's offer: %v, want ErrStale", res.err)
	}

	// Bob, with ClientPolicy, answers the re-INVITE without an offer with his session; alice answers in the ACK.
	o := alice.next("offer")
	if string(o.sdp) != "offer-b" {
		t.Fatalf("offer %q, want bob's session", o.sdp)
	}

	_ = o.offer.Answer(ua.Change{Bodies: sdpOf("answer-b")})

	if res := recv(t, asking).res; res.StatusCode != 200 {
		t.Fatalf("alice's re-INVITE %d", res.StatusCode)
	}
}

// With the zero Policy, a 491 is not retried: done receives it, for the application, a B2BUA passing it back.
func TestServerPolicyHandsBackGlare(t *testing.T) {
	alice, bob := serverPeer(t, "alice"), newPeer(t, "bob", nil, ua.SessionTimer{})
	as, _ := established(t, alice, bob)

	done, f := results()

	if err := as.Reinvite(ua.Change{Bodies: sdpOf("offer-a")}, f); err != nil {
		t.Fatal(err)
	}

	_ = bob.next("request").req.Reject(491)

	if res := recv(t, done).res; res.StatusCode != 491 {
		t.Fatalf("re-INVITE %d, want the 491 handed back", res.StatusCode)
	}

	bob.none()
}

// RFC 4028 §7.2, §9: an UPDATE of an early dialog sets no session timer; it starts once the session is confirmed
// (S19).
func TestNoSessionTimerOnEarlyDialogs(t *testing.T) {
	clock := siptest.NewClock()
	timer := ua.SessionTimer{Interval: 90 * time.Second}
	alice, bob := newPeer(t, "alice", clock, timer), newPeer(t, "bob", clock, timer)

	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	if err := r.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")}); err != nil {
		t.Fatal(err)
	}

	es := alice.next("answer").s
	alice.next("progress")

	done, f := results()

	if err := es.Update(ua.Change{}, f); err != nil {
		t.Fatal(err)
	}

	if res := recv(t, done).res; res.StatusCode != 200 || res.Header.Get("Session-Expires") == "" {
		t.Fatalf("early UPDATE %d, Session-Expires %q", res.StatusCode, res.Header.Get("Session-Expires"))
	}

	// Ringing past the interval: no refresh and no expiry on the early dialog.
	advance(clock, 120*time.Second, alice, bob)

	alice.none()
	bob.none()

	if es.State() != ua.Early {
		t.Fatalf("early session %s", es.State())
	}
}
