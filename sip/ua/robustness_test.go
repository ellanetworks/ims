package ua_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

// R1: a re-INVITE the application never answers gets a 500 with Retry-After after Policy.AnswerTimeout; an INFO left
// open when the session ends gets a 487 (RFC 3261 §13.3.1, §15.1.2).
func TestUnansweredRequests(t *testing.T) {
	clock := netOf(t).Clock()
	alice := newPeer(t, "alice", clock, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.Transaction.Clock = clock
		c.Policy = ua.Policy{AnswerTimeout: 20 * time.Second}
	})
	as, _ := established(t, alice, bob)

	done, f := results()

	if err := as.Reinvite(ua.Change{Bodies: sdpOf("offer-2")}, f); err != nil {
		t.Fatal(err)
	}

	bob.next("request")

	advance(clock, 21*time.Second, alice, bob)

	if res := recv(t, done).res; res.StatusCode != 500 || !res.Header.Has("Retry-After") {
		t.Fatalf("unanswered re-INVITE %d", res.StatusCode)
	}

	// An INFO of a raw peer left open as bob ends the session.
	carol := newRaw(t)

	invite := carol.invite(bob, "offer-1")
	_ = bob.next("invite").req.Accept(sdpOf("answer-1")...)

	ok := carol.response()
	carol.ack(invite, ok)

	bs := bob.next("established").s

	carol.sock.Send(sip.UDP, bob.addr, carol.inDialog("INFO", invite, ok, 2))
	bob.next("request")

	_ = bs.Bye(ua.Change{}, nil)

	for {
		m, got := carol.sock.RecvWithin(wait)
		if !got {
			t.Fatal("no response to the INFO")
		}

		if res, isRes := m.Msg.(*sip.Response); isRes && strings.HasSuffix(res.Header.Get("CSeq"), "INFO") {
			if res.StatusCode != 487 {
				t.Fatalf("INFO open at the end: %d, want 487", res.StatusCode)
			}

			break
		}
	}
}

// R3: a BYE Handler.Bye holds gets its 200 when the agent closes, and the session ends by that BYE.
func TestHeldByeAnsweredOnClose(t *testing.T) {
	ended := make(chan ua.End, 1)
	held := make(chan *ua.Request, 1)

	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnInvite = func(r *ua.Request) ua.Handler {
			_ = r.Accept(sdpOf("answer-1")...)

			return ua.Handler{
				Bye:   func(_ *ua.Session, r *ua.Request) { held <- r },
				Ended: func(_ *ua.Session, e ua.End) { ended <- e },
			}
		}
	})

	alice.invite(bob, "offer-1")

	as := alice.next("established").s
	alice.next("answer")

	done, f := results()

	_ = as.Bye(ua.Change{}, f)

	<-held

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := bob.a.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if res := recv(t, done).res; res == nil || res.StatusCode != 200 {
		t.Fatalf("held BYE: %+v", res)
	}

	if e := <-ended; e.Cause != ua.EndBye || !e.Remote {
		t.Errorf("ended %+v", e)
	}
}

// R4: Decorate completes a request sent again after a challenge once, not twice (RFC 3325 §9.1).
func TestDecorateOnceAfterAChallenge(t *testing.T) {
	alice := newPeerWith(t, "alice", func(c *ua.Config) {
		c.Authenticate = func(req *sip.Request, _ *sip.Response) error {
			req.Header.Set("Authorization", `Digest username="alice"`)
			return nil
		}
	})
	bob := newUAS(t, alice)

	h := alice.handler()
	h.Decorate = func(_ *ua.Session, m sip.Message) {
		if req, ok := m.(*sip.Request); ok && req.Method == "BYE" {
			req.Header.Add("P-Asserted-Identity", "<sip:alice@example.com>")
		}
	}

	_, invite := inviteRaw(t, alice, bob, "offer-1", h)
	bob.answer(invite, 200, "answer-1")
	bob.nextRequest("ACK")

	as := alice.next("established").s
	alice.next("answer")

	_ = as.Bye(ua.Change{}, nil)

	bye := bob.nextRequest("BYE")
	bob.answer(bye, 401, "", sip.Field{Name: "WWW-Authenticate", Value: `Digest realm="x", nonce="y"`})

	bye = bob.nextRequest("BYE")
	if n := bye.Header.Count("P-Asserted-Identity"); n != 1 {
		t.Fatalf("BYE sent again with %d P-Asserted-Identity", n)
	}
}

// R5: refreshes turned off with SkipRefreshes resume once it is off again (RFC 4028 §10).
func TestRefreshesResume(t *testing.T) {
	clock := netOf(t).Clock()
	alice := newPeer(t, "alice", clock, ua.SessionTimer{Interval: 120 * time.Second})
	bob := newPeer(t, "bob", clock, ua.SessionTimer{Interval: 120 * time.Second, Refresher: sip.RefresherUAC})
	as, _ := established(t, alice, bob)

	skip := as.Policy()
	skip.SkipRefreshes = true
	as.SetPolicy(skip)

	advance(clock, 65*time.Second, alice, bob)

	skip.SkipRefreshes = false
	as.SetPolicy(skip)

	for range 6 {
		advance(clock, 10*time.Second, alice, bob)
	}

	alice.none()
	bob.none()
}

// R6: once closed, the agent refuses new usages; Close waits for the final response of an INVITE it cancels, a 2xx
// crossing the CANCEL acknowledged and ended with a BYE (RFC 3261 §15).
func TestCloseWaitsForTheCancelledInvite(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newUAS(t, alice)

	_, invite := inviteRaw(t, alice, bob, "offer-1", alice.handler())
	bob.answer(invite, 180, "")
	alice.next("progress")

	closed := make(chan error, 1)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		closed <- alice.a.Close(ctx)
	}()

	cancel := bob.nextRequest("CANCEL")
	bob.sock.Send(sip.UDP, bob.to, sip.NewResponse(cancel, 200, ""))

	// The 2xx crosses the CANCEL.
	bob.answer(invite, 200, "answer-1")
	bob.nextRequest("ACK")

	bye := bob.nextRequest("BYE")
	bob.sock.Send(sip.UDP, bob.to, sip.NewResponse(bye, 200, ""))

	if err := <-closed; err != nil {
		t.Fatal(err)
	}

	uri, _ := sip.ParseURI(bob.uri())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+bob.uri()+">")

	if _, err := alice.a.Invite(req, ua.Handler{}); !errors.Is(err, ua.ErrClosed) {
		t.Errorf("Invite after Close: %v", err)
	}
}

// R7: an extension of Config.Extensions is advertised and accepted in Require; one off gets a 420 (RFC 3261 §8.2.2.3).
func TestExtensions(t *testing.T) {
	for _, on := range []bool{true, false} {
		bob := newPeerWith(t, "bob", func(c *ua.Config) { c.Extensions.Replaces = on })
		alice := newRaw(t)

		alice.invite(bob, "offer-1", sip.Field{Name: "Require", Value: "replaces"})

		if on {
			r := bob.next("invite").req
			_ = r.Accept(sdpOf("answer-1")...)

			res := alice.response()
			if res.StatusCode != 200 || !res.Header.HasOptionTag("Supported", "replaces") {
				t.Fatalf("with replaces: %d, Supported %v", res.StatusCode, res.Header.Elements("Supported"))
			}

			continue
		}

		if res := alice.response(); res.StatusCode != 420 || res.Header.Get("Unsupported") != "replaces" {
			t.Fatalf("without replaces: %d %q", res.StatusCode, res.Header.Get("Unsupported"))
		}
	}
}

// R8: a re-INVITE that overtakes the ACK of the initial 2xx leaves that 2xx awaiting its ACK, which confirms the
// session (RFC 3261 §13.3.1.4).
func TestReinviteOvertakingTheAck(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	alice := newRaw(t)

	invite := alice.invite(bob, realSDP(4000, 1))

	r := bob.next("invite").req
	_ = r.Accept(ua.SDP([]byte(realSDP(5000, 1))))

	ok := alice.response()

	reinvite := alice.inDialog("INVITE", invite, ok, 2)
	reinvite.SetBodies(ua.SDP([]byte(realSDP(4000, 1))))
	alice.sock.Send(sip.UDP, bob.addr, reinvite)

	// Bob, with ClientPolicy, answers the refresh; the first 2xx is retransmitted still.
	for {
		res := alice.response()
		if strings.HasSuffix(res.Header.Get("CSeq"), "2 INVITE") {
			break
		}
	}

	alice.ack(invite, ok)

	if e := bob.next("established"); e.s.State() != ua.Confirmed {
		t.Fatalf("session %s after the ACK of the initial 2xx", e.s.State())
	}
}

// R2: Config.StrandFor puts the initial requests the application chooses on one strand, as the INVITEs of a group's
// participants (TS 24.379 §10.1.1.4.5.1).
func TestStrandFor(t *testing.T) {
	strands := make(chan *ua.Strand, 2)

	var group atomic.Pointer[ua.Strand]

	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.StrandFor = func(req *sip.Request) *ua.Strand {
			if req.URI.User == "group" {
				return group.Load()
			}

			return nil
		}
		c.OnInvite = func(r *ua.Request) ua.Handler {
			strands <- r.Strand()

			_ = r.Accept(sdpOf("answer")...)

			return ua.Handler{}
		}
	})

	group.Store(bob.a.NewStrand())

	for range 2 {
		alice := newRaw(t)

		req := siptest.NewRequest("INVITE", "sip:group@"+bob.addr.String(), sip.UDP, alice.sock.Addr())
		req.SetBodies(sdpOf("offer")...)
		alice.sock.Send(sip.UDP, bob.addr, req)

		if st := <-strands; st != group.Load() {
			t.Fatal("an INVITE to the group on another strand")
		}
	}
}

// R12: the 200 to the NOTIFY that creates the dialog carries the Contact of the agent (RFC 3261 §12.1.1).
func TestContactInTheFirstNotifyResponse(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	sb.subscribe(n)
	n.recvSubscribe()

	res := n.notify("n1", "active;expires=600")
	if res.StatusCode != 200 || !res.Header.Has("Contact") {
		t.Fatalf("200 to the first NOTIFY %d, Contact %q", res.StatusCode, res.Header.Get("Contact"))
	}
}
