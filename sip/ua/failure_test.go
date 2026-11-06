package ua_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// RFC 3261 section 13.2.2.4
// RFC 3261 section 13.3.1.4
func TestAckNotPrepared(t *testing.T) {
	errNoFlow := errors.New("no flow")

	alice := newPeerWith(t, "alice", func(c *ua.Config) {
		prepare := c.Prepare
		c.Prepare = func(ctx context.Context, req *sip.Request) error {
			if req.Method == "ACK" {
				return errNoFlow
			}

			return prepare(ctx, req)
		}
	})
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})

	alice.invite(bob, "offer-1")

	if err := bob.next("invite").req.Accept(sdpOf("answer-1")...); err != nil {
		t.Fatal(err)
	}

	got := alice.nextAll("established", "answer", "ended")
	if e := got["ended"].end; e.Cause != ua.EndFailed || !errors.Is(e.Err, errNoFlow) {
		t.Errorf("alice ended %+v", e)
	}

	if e := bob.next("ended").end; e.Cause != ua.EndBye || !e.Remote {
		t.Errorf("bob ended %+v", e)
	}
}

// RFC 4575 section 4.4
// RFC 6665 section 4.1.3
func TestNotifyNotRendered(t *testing.T) {
	errRender := errors.New("render")

	cfg := rosterConfig(true, 0)
	full, partial := cfg.Full, cfg.Partial
	cfg.Full = func(value any, seq uint32, state roster) (ua.Notification, error) {
		if slices.Contains(state, "worse") {
			return ua.Notification{}, errRender
		}

		return full(value, seq, state)
	}
	cfg.Partial = func(value any, seq uint32, state roster, delta string) (ua.Notification, error) {
		if delta == "bad" {
			return ua.Notification{}, errRender
		}

		return partial(value, seq, state, delta)
	}

	res := ua.NewResource(roster{"a"}, cfg)
	n := newResourceNotifier(t, res, 0)

	r := newRaw(t)
	_, _, first := rawSubscribe(t, r, n.p, "carol", "600")
	<-n.subs

	r.sock.Send(sip.UDP, n.p.addr, sip.NewResponse(first, 200, ""))
	netOf(t).Quiesce()

	if err := res.Change(context.Background(), roster{"a", "bad"}, "bad"); err != nil {
		t.Fatal(err)
	}

	got := r.nextRequest("NOTIFY")
	if string(got.Body) != "full carol [a bad] seq=1" {
		t.Fatalf("NOTIFY after a failed partial %q", got.Body)
	}

	r.sock.Send(sip.UDP, n.p.addr, sip.NewResponse(got, 200, ""))
	netOf(t).Quiesce()

	if err := res.Change(context.Background(), roster{"a", "bad", "worse"}, "worse"); err != nil {
		t.Fatal(err)
	}

	if got := r.nextRequest("NOTIFY"); string(got.Body) != "add carol worse seq=2" {
		t.Fatalf("NOTIFY %q", got.Body)
	} else {
		r.sock.Send(sip.UDP, n.p.addr, sip.NewResponse(got, 200, ""))
	}

	if err := res.Change(context.Background(), roster{"a", "bad", "worse", "bad"}, "bad"); err != nil {
		t.Fatal(err)
	}

	last := r.nextRequest("NOTIFY")

	ss, _, err := last.Header.SubscriptionState()
	if err != nil || ss.State != sip.SubscriptionTerminated || ss.Reason != "probation" || ss.RetryAfter == nil ||
		*ss.RetryAfter == 0 || len(last.Body) != 0 {
		t.Fatalf("NOTIFY %s, %q", last.Header.Get("Subscription-State"), last.Body)
	}

	r.sock.Send(sip.UDP, n.p.addr, sip.NewResponse(last, 200, ""))
	netOf(t).Quiesce()

	if e := <-n.ended; e.Cause != ua.EndFailed || !errors.Is(e.Err, errRender) {
		t.Errorf("ended %+v", e)
	}

	if len(res.Subscriptions()) != 0 {
		t.Error("subscription still bound to the resource")
	}
}
