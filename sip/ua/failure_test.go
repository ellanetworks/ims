package ua_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// RFC 3261 §13.2.2.4, §13.3.1.4: a 2xx the agent cannot acknowledge, its Prepare failing for the ACK, loses the
// session: the agent ends it with a BYE rather than keep a session its peer is about to end.
func TestAckNotPrepared(t *testing.T) {
	errNoFlow := errors.New("no flow")

	alice := newPeerWith(t, "alice", func(c *ua.Config) {
		prepare := c.Prepare
		c.Prepare = func(req *sip.Request) error {
			if req.Method == "ACK" {
				return errNoFlow
			}

			return prepare(req)
		}
	})
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})

	alice.invite(bob, "offer-1")

	if err := bob.next("invite").req.Accept(sdpOf("answer-1")...); err != nil {
		t.Fatal(err)
	}

	got := alice.nextAll("established", "answer", "ended")
	if e := got["ended"].end; e.Cause != ua.EndLost || !errors.Is(e.Err, errNoFlow) {
		t.Errorf("alice ended %+v", e)
	}

	if e := bob.next("ended").end; e.Cause != ua.EndBye || !e.Remote {
		t.Errorf("bob ended %+v", e)
	}
}

// RFC 6665 §4.1.3: a change the application fails to render ends the subscription, with a terminated NOTIFY without
// state for the subscriber to retry later, rather than leave it with a state the notifier no longer has.
func TestNotifyNotRendered(t *testing.T) {
	errRender := errors.New("render")

	cfg := rosterConfig(false, 0)
	full := cfg.Full
	cfg.Full = func(value any, seq uint32, state roster) (ua.Notification, error) {
		if slices.Contains(state, "bad") {
			return ua.Notification{}, errRender
		}

		return full(value, seq, state)
	}

	res := ua.NewResource(roster{"a"}, cfg)
	n := newResourceNotifier(t, res, 0)

	r := newRaw(t)
	_, _, first := rawSubscribe(t, r, n.p, "carol", "600")
	<-n.subs

	r.sock.Send(sip.UDP, n.p.addr, sip.NewResponse(first, 200, ""))
	netOf(t).Quiesce()

	if err := res.Change(roster{"a", "bad"}, "bad"); err != nil {
		t.Fatal(err)
	}

	last := r.nextRequest("NOTIFY")
	if ss := last.Header.Get("Subscription-State"); !strings.HasPrefix(ss, "terminated") ||
		!strings.Contains(ss, "reason=probation") || len(last.Body) != 0 {
		t.Fatalf("NOTIFY %s, %q", ss, last.Body)
	}

	r.sock.Send(sip.UDP, n.p.addr, sip.NewResponse(last, 200, ""))
	netOf(t).Quiesce()

	if e := <-n.ended; e.Cause != ua.EndLost || !errors.Is(e.Err, errRender) {
		t.Errorf("ended %+v", e)
	}

	if len(res.Subscriptions()) != 0 {
		t.Error("subscription still bound to the resource")
	}
}
