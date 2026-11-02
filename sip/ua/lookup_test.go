package ua_test

import (
	"context"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

// RFC 3891 section 3
func replaces(id dialog.ID, earlyOnly bool) sip.Field {
	return sip.Field{Name: "Replaces", Value: sip.Replaces{CallID: id.CallID, ToTag: id.LocalTag, FromTag: id.RemoteTag, EarlyOnly: earlyOnly}.String()}
}

type replacing struct {
	s    *ua.Session
	code int
	ok   bool
	same bool
}

// RFC 3891 section 3
func TestReplaces(t *testing.T) {
	clock := netOf(t).Clock()
	found := make(chan replacing, 1)
	first := make(chan *ua.Session, 1)

	var bs *ua.Session

	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.Transaction.Clock = clock
		c.Extensions.Replaces = true
		c.OnInvite = func(_ context.Context, r *ua.Request) ua.Handler {
			s, code, ok := r.Replaces()
			if !ok {
				_ = r.Accept(sdpOf("answer-1")...)
				first <- r.Session()

				return ua.Handler{}
			}

			found <- replacing{s: s, code: code, ok: ok, same: bs != nil && r.Strand() == bs.Strand()}

			if code != 0 {
				_ = r.Reject(code)
				return ua.Handler{}
			}

			_ = r.Accept(sdpOf("answer-2")...)

			return ua.Handler{}
		}
	})
	alice := newPeer(t, "alice", clock, ua.SessionTimer{})

	alice.invite(bob, "offer-1")
	alice.next("established")
	alice.next("answer")

	bs = <-first
	id := bs.ID()

	if netOf(t).Quiesce(); bs.State() != ua.Confirmed {
		t.Fatal("bob's session not confirmed")
	}

	if bob.a.Session(id) != bs {
		t.Fatal("Agent.Session does not find the session")
	}

	carol := newRaw(t)

	try := func(header ...sip.Field) replacing {
		t.Helper()

		invite := carol.invite(bob, "offer-2", header...)

		r := <-found

		res := carol.response()
		for res.Header.CallID() != invite.Header.CallID() {
			res = carol.response()
		}

		if r.code != 0 && res.StatusCode != r.code || r.code == 0 && res.StatusCode != 200 {
			t.Fatalf("INVITE %d, Replaces gave %d", res.StatusCode, r.code)
		}

		if res.IsSuccess() {
			carol.ack(invite, res)
		}

		return r
	}

	if r := try(replaces(id, false)); r.s != bs || !r.same {
		t.Errorf("replaces %p (want %p), same strand %v", r.s, bs, r.same)
	}

	if r := try(replaces(id, true)); r.code != 486 {
		t.Errorf("early-only on a confirmed dialog: %d, want 486", r.code)
	}

	unknown := id
	unknown.RemoteTag = "nobody"

	if r := try(replaces(unknown, false)); r.code != 481 {
		t.Errorf("unknown dialog: %d, want 481", r.code)
	}

	if r := try(replaces(id, false), replaces(id, false)); r.code != 400 {
		t.Errorf("two Replaces: %d, want 400", r.code)
	}

	_ = bs.Bye(context.Background(), ua.Change{}, nil)

	alice.next("ended")

	if r := try(replaces(id, false)); r.code != 603 {
		t.Errorf("ended dialog: %d, want 603", r.code)
	}

	advance(clock, 64*transaction.DefaultT1, alice, bob)

	if r := try(replaces(id, false)); r.code != 481 {
		t.Errorf("forgotten dialog: %d, want 481", r.code)
	}
}

// RFC 4538 section 4
func TestTargetDialog(t *testing.T) {
	refers := make(chan *ua.Request, 1)

	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.Extensions.TargetDialog = true
		c.OnRequest = func(_ context.Context, r *ua.Request) { refers <- r }
	})
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})

	_, bs := established(t, alice, bob)
	id := bs.ID()

	carol := newRaw(t)

	refer := siptest.NewRequest("REFER", bob.uri(), sip.UDP, carol.sock.Addr())
	refer.Header.Add("Refer-To", "<sip:dave@example.com>")
	refer.Header.Add("Target-Dialog", sip.TargetDialog{CallID: id.CallID, LocalTag: id.LocalTag, RemoteTag: id.RemoteTag}.String())
	carol.sock.Send(sip.UDP, bob.addr, refer)

	r := <-refers
	if s := bob.a.TargetDialog(r.Message()); s != bs || r.Strand() != bs.Strand() {
		t.Errorf("target %p, want %p; same strand %v", s, bs, r.Strand() == bs.Strand())
	}

	_ = r.Respond(ua.Reply{Code: 202})

	if res := carol.response(); res.StatusCode != 202 {
		t.Fatalf("REFER %d", res.StatusCode)
	}
}
