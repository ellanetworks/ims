package ua_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/ua"
)

func checkTables(t *testing.T, p *peer) {
	t.Helper()

	deadline := time.Now().Add(wait)

	for {
		d, i, n, o := ua.Tables(p.a)
		if d == 0 && i == 0 && n == 0 && o == 0 {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("%s: %d dialogs, %d INVITEs, %d initial requests, %d outgoing INVITEs left", p.name, d, i, n, o)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// Nothing is left in the tables of the agents once calls end, however they end.
func TestNoLeaks(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	_, bs := established(t, alice, bob)
	_ = bs.Bye()

	alice.next("ended")
	bob.next("ended")

	o := alice.invite(bob, "offer-1")
	r := bob.next("invite").req
	_ = r.Progress(ua.Reply{Code: 180})

	alice.next("progress")

	_ = o.Cancel()

	bob.next("ended")
	alice.nextAll("ended", "failed")

	alice.invite(bob, "offer-1")
	_ = bob.next("invite").req.Reject(486)
	bob.next("ended")
	alice.next("failed")

	for _, p := range []*peer{alice, bob} {
		checkTables(t, p)
	}
}

// A call of the application from another goroutine returns before the handlers it makes due run: the application
// may hold its own lock while calling the agent, and take it in a handler.
func TestHandlersAfterTheCall(t *testing.T) {
	var mu sync.Mutex

	ended := make(chan struct{})

	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(r *ua.Request) ua.Handler {
			go func() {
				mu.Lock()
				defer mu.Unlock()

				_ = r.Reject(486)
			}()

			return ua.Handler{Ended: func(*ua.Session, ua.End) {
				mu.Lock()
				defer mu.Unlock()

				close(ended)
			}}
		}
	})

	alice.invite(bob, "offer-1")

	select {
	case <-ended:
	case <-time.After(wait):
		t.Fatal("deadlock")
	}

	alice.next("failed")
}

// Handlers run on the loop of the agent and call it directly: a handler ending the session reads its state at once.
func TestHandlersCallTheAgent(t *testing.T) {
	states := make(chan ua.State, 2)

	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(r *ua.Request) ua.Handler {
			if err := r.Reject(486); err != nil {
				t.Error(err)
			}

			states <- r.Session().State()

			return ua.Handler{Ended: func(s *ua.Session, _ ua.End) { states <- s.State() }}
		}
	})

	alice.invite(bob, "offer-1")
	alice.next("failed")

	for range 2 {
		select {
		case st := <-states:
			if st != ua.Terminated {
				t.Errorf("state %v, want terminated", st)
			}
		case <-time.After(wait):
			t.Fatal("no state")
		}
	}
}

// RFC 3262 §5, RFC 3264 §6: an offer of a reliable 1xx refused is answered in the PRACK with its streams refused, and
// the INVITE cancelled.
func TestRejectOfferInThePrack(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	alice.invite(bob, "")

	in := bob.next("invite").req
	if err := in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf(realSDP(5000, 1))}); err != nil {
		t.Fatal(err)
	}

	alice.next("offer").offer.Reject()

	a := bob.next("answer")

	if s, err := sdp.Parse(a.sdp); err != nil || s.Media[0].Port() != 0 {
		t.Errorf("answer in the PRACK %q", a.sdp)
	}

	if e := bob.next("ended"); e.end.Cause != ua.EndCancelled {
		t.Errorf("bob ended %+v", e.end)
	}
}

// RFC 3311 §5.2: an offer while one of the peer is pending gets a 500 with Retry-After; a PRACK matching nothing a 481
// (RFC 3262 §3).
func TestConflicts(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	r := newRaw(t)

	invite := r.invite(bob, realSDP(4000, 1))
	_ = bob.next("invite").req.Accept(ua.SDP([]byte(realSDP(5000, 1))))

	ok := r.response()
	r.ack(invite, ok)
	bob.next("established")

	send := func(method string, seq uint32, body string, header ...sip.Field) {
		t.Helper()

		req := r.inDialog(method, invite, ok, seq)
		for _, f := range header {
			req.Header.Add(f.Name, f.Value)
		}

		if body != "" {
			req.SetBodies(ua.SDP([]byte(body)))
		}

		r.sock.Send(sip.UDP, bob.addr, req)
	}

	send("UPDATE", 2, realSDP(4002, 2))
	bob.next("request")

	send("UPDATE", 3, realSDP(4004, 3))

	if res := r.response(); res.StatusCode != 500 || !res.Header.Has("Retry-After") {
		t.Errorf("second UPDATE %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}

	send("PRACK", 4, "", sip.Field{Name: "RAck", Value: "1 1 INVITE"})

	if res := r.response(); res.StatusCode != 481 {
		t.Errorf("PRACK %d, want 481", res.StatusCode)
	}
}

// RFC 3262 §4: a reliable 1xx out of order is not acknowledged; the next one in order is.
func TestReliableOutOfOrder(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	r := newRaw(t)

	uri, _ := sip.ParseURI("sip:bob@" + r.sock.Addr().String())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+uri.String()+">")
	req.SetBodies(ua.SDP([]byte(realSDP(4000, 1))))

	if _, err := alice.a.Invite(req, alice.handler()); err != nil {
		t.Fatal(err)
	}

	inv, f := r.sock.RecvRequest()

	reliable := func(rseq int) {
		res := sip.NewResponse(inv, 180, "")
		_ = res.Header.SetToTag("b")
		res.Header.Add("Contact", "<sip:b@"+r.sock.Addr().String()+">")
		res.Header.Add("Require", "100rel")
		res.Header.Add("RSeq", fmt.Sprint(rseq))
		r.sock.Send(sip.UDP, f.Remote, res)
	}

	prack := func() string {
		req, _ := r.sock.RecvRequest()
		return req.Header.Get("RAck")
	}

	reliable(5)

	if rack := prack(); rack != "5 1 INVITE" {
		t.Fatalf("RAck %q", rack)
	}

	reliable(7)
	r.sock.RecvNone(100 * time.Millisecond)

	reliable(6)

	if rack := prack(); rack != "6 1 INVITE" {
		t.Fatalf("RAck %q", rack)
	}
}
