package ua_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// A BYE carries the change of the application (TS 24.379 §6.3.2.1.6: P-Asserted-Identity, mcptt-info), and its done
// receives the final response. The End of the peer has its Reason (RFC 3326).
func TestByeWithChange(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	as, _ := established(t, alice, bob)

	reason, _ := sip.NewReason("SIP", 200, "Call completed elsewhere")
	c := ua.Reasons(reason)
	c.Header.Add("P-Asserted-Identity", "<sip:alice@example.com>")
	c.Bodies = []sip.BodyPart{sip.NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte("<mcpttinfo/>"))}

	done, f := results()

	if err := as.Bye(c, f); err != nil {
		t.Fatal(err)
	}

	alice.next("ended")

	e := bob.next("ended")
	if e.end.Cause != ua.EndBye || e.end.Request.Header.Get("P-Asserted-Identity") == "" {
		t.Fatalf("bob ended %+v", e.end)
	}

	if rs := e.end.Reasons(); len(rs) != 1 || rs[0].String() != reason.String() {
		t.Errorf("reasons %v", rs)
	}

	if res := recv(t, done).res; res.StatusCode != 200 {
		t.Fatalf("BYE %d", res.StatusCode)
	}

	var uerr *ua.Error
	if err := as.Bye(ua.Change{}, nil); !errors.As(err, &uerr) || uerr.Op != "Bye" || uerr.State != ua.Terminated || !errors.Is(err, ua.ErrState) {
		t.Errorf("Bye after the end: %v", err)
	}
}

// Handler.Bye: the application answers the BYE, once the other leg ended, and the session ends with the response;
// left unanswered, the agent answers after 32*T1, before the peer gives up (TS 24.379 §6.3.2.1.6).
func TestByeHandler(t *testing.T) {
	clock := siptest.NewClock()
	byes := make(chan *ua.Request, 2)

	alice := newPeer(t, "alice", clock, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.Transaction.Clock = clock
		c.OnInvite = func(r *ua.Request) ua.Handler {
			_ = r.Accept(sdpOf("answer-1")...)

			h := ua.Handler{Ended: func(s *ua.Session, e ua.End) { byes <- nil }}
			h.Bye = func(s *ua.Session, r *ua.Request) { byes <- r }

			return h
		}
	})

	for _, answer := range []bool{true, false} {
		alice.invite(bob, "offer-1")

		as := alice.next("established").s
		alice.next("answer")

		done, f := results()

		if err := as.Bye(ua.Change{}, f); err != nil {
			t.Fatal(err)
		}

		alice.next("ended")

		r := <-byes
		if r == nil || r.Message().Method != "BYE" {
			t.Fatal("no BYE for the application")
		}

		if s := r.Session(); s.State() == ua.Terminated {
			t.Error("the session ended before the response to the BYE")
		}

		select {
		case <-done:
			t.Fatal("the BYE answered before the application")
		case <-time.After(100 * time.Millisecond):
		}

		if answer {
			_ = r.Respond(ua.Reply{Code: 200})
		} else {
			advance(clock, 32*transaction.DefaultT1, alice, bob)
		}

		if <-byes != nil {
			t.Fatal("no end after the response")
		}

		if res := recv(t, done); res.res == nil || res.res.StatusCode != 200 {
			t.Fatalf("BYE answer=%v: %+v", answer, res)
		}
	}
}

// Handler.Decorate completes every message of the session, those the agent makes on its own included.
func TestDecorate(t *testing.T) {
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(r *ua.Request) ua.Handler {
			h := ua.Handler{Decorate: func(s *ua.Session, m sip.Message) {
				m.Env().Header.Set("X-Leg", "b")
			}}

			r.Session().SetHandler(h)

			_ = r.Progress(ua.Reply{Code: 180})
			_ = r.Accept(sdpOf("answer-1")...)

			return h
		}
	})
	alice := newRaw(t)

	invite := alice.invite(bob, "offer-1")

	var ok *sip.Response

	for _, want := range []int{180, 200} {
		res := alice.response()
		if res.StatusCode != want || res.Header.Get("X-Leg") != "b" {
			t.Fatalf("%d with X-Leg %q, want %d decorated", res.StatusCode, res.Header.Get("X-Leg"), want)
		}

		ok = res
	}

	alice.ack(invite, ok)

	options := alice.inDialog("OPTIONS", invite, ok, 2)
	alice.sock.Send(sip.UDP, bob.addr, options)

	if res := alice.response(); res.StatusCode != 200 || res.Header.Get("X-Leg") != "b" {
		t.Fatalf("OPTIONS %d with X-Leg %q", res.StatusCode, res.Header.Get("X-Leg"))
	}
}
