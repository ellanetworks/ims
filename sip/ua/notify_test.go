package ua_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

// notifierPeer is an agent accepting SUBSCRIBEs to the "test" event, its state a counter body.
type notifierPeer struct {
	p     *peer
	subs  chan *ua.ServerSubscription
	ended chan ua.End

	mu      sync.Mutex
	version string
	pending bool
}

func newNotifierAgent(t *testing.T, clock *siptest.Clock) *notifierPeer {
	n := &notifierPeer{subs: make(chan *ua.ServerSubscription, 4), ended: make(chan ua.End, 4), version: "v0"}

	n.p = newPeerWith(t, "bob", func(c *ua.Config) {
		if clock != nil {
			c.Transaction.Clock = clock
		}

		c.OnRequest = func(r *ua.Request) {
			s, err := r.AcceptSubscription(ua.Acceptance{
				Expires: 600 * time.Second,
				Pending: n.isPending(),
				State: func(seq uint32) (ua.Notification, error) {
					return ua.Notification{Bodies: []sip.BodyPart{n.body(seq)}, Full: true}, nil
				},
				Handler: ua.ServerSubscriptionHandler{Ended: func(_ *ua.ServerSubscription, e ua.End) { n.ended <- e }},
			})
			if err != nil {
				t.Errorf("AcceptSubscription: %v", err)
				return
			}

			n.subs <- s
		}
	})

	return n
}

func (n *notifierPeer) isPending() bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	return n.pending
}

func (n *notifierPeer) body(seq uint32) sip.BodyPart {
	n.mu.Lock()
	defer n.mu.Unlock()

	return sip.NewBodyPart("text/plain", fmt.Appendf(nil, "%s seq=%d", n.version, seq))
}

// subscriberOf is a subscriber recording the NOTIFYs of its subscriptions.
type subscriberOf struct {
	p       *peer
	notifys chan *ua.Request
	ended   chan ua.End
}

func (s *subscriberOf) subscribe(t *testing.T, to *peer, expires string) *ua.Subscription {
	t.Helper()

	uri, _ := sip.ParseURI(to.uri())
	req := sip.NewRequest("SUBSCRIBE", uri)
	req.Header.Add("From", "<"+s.p.uri()+">")
	req.Header.Add("To", "<"+to.uri()+">")
	req.Header.Add("Event", "test")
	req.Header.Add("Expires", expires)

	sub, err := s.p.a.Subscribe(req, ua.SubscriptionHandler{
		Notify: func(_ *ua.Subscription, r *ua.Request) { s.notifys <- r },
		Ended:  func(_ *ua.Subscription, e ua.End) { s.ended <- e },
	})
	if err != nil {
		t.Fatal(err)
	}

	return sub
}

func newSubscriberOf(t *testing.T, clock *siptest.Clock) *subscriberOf {
	s := &subscriberOf{notifys: make(chan *ua.Request, 8), ended: make(chan ua.End, 4)}

	if clock != nil {
		s.p = newPeer(t, "alice", clock, ua.SessionTimer{})
	} else {
		s.p = newPeer(t, "alice", nil, ua.SessionTimer{})
	}

	return s
}

func nextNotify(t *testing.T, c chan *ua.Request) *sip.Request {
	t.Helper()

	select {
	case r := <-c:
		return r.Message()
	case <-time.After(wait):
		t.Fatal("no NOTIFY")
		return nil
	}
}

func nextEnd(t *testing.T, c chan ua.End) ua.End {
	t.Helper()

	select {
	case e := <-c:
		return e
	case <-time.After(wait):
		t.Fatal("no end")
		return ua.End{}
	}
}

func checkNotify(t *testing.T, req *sip.Request, state, body string) {
	t.Helper()

	if ss := req.Header.Get("Subscription-State"); !strings.HasPrefix(ss, state) {
		t.Errorf("Subscription-State %q, want %s", ss, state)
	}

	if string(req.Body) != body {
		t.Errorf("body %q, want %q", req.Body, body)
	}
}

// RFC 6665 §4.2: the notifier answers 200 with the duration, sends the state at once, on each change, after each
// refresh, and a terminated NOTIFY when the subscriber unsubscribes.
func TestNotifier(t *testing.T) {
	n := newNotifierAgent(t, nil)
	sb := newSubscriberOf(t, nil)

	sub := sb.subscribe(t, n.p, "3600")

	s := <-n.subs

	// The duration asked for is cut to the one granted.
	checkNotify(t, nextNotify(t, sb.notifys), "active;expires=600", "v0 seq=0")

	if d := sub.Duration(); d != 600*time.Second {
		t.Errorf("duration %v, want 600 s", d)
	}

	n.mu.Lock()
	n.version = "v1"
	n.mu.Unlock()

	if err := s.Notify(ua.Notification{Bodies: []sip.BodyPart{n.body(1)}, Full: true}); err != nil {
		t.Fatal(err)
	}

	checkNotify(t, nextNotify(t, sb.notifys), "active;expires=", "v1 seq=1")

	done, f := results()

	if err := sub.Refresh(ua.Change{}, f); err != nil {
		t.Fatal(err)
	}

	if res := recv(t, done).res; res.StatusCode != 200 || res.Header.Get("Expires") != "600" {
		t.Fatalf("refresh %d Expires %q", res.StatusCode, res.Header.Get("Expires"))
	}

	checkNotify(t, nextNotify(t, sb.notifys), "active", "v1 seq=2")

	if err := sub.Unsubscribe(ua.Change{}); err != nil {
		t.Fatal(err)
	}

	checkNotify(t, nextNotify(t, sb.notifys), "terminated;reason=timeout", "v1 seq=3")

	if e := nextEnd(t, sb.ended); e.Cause != ua.EndTerminated {
		t.Errorf("subscriber ended %+v", e)
	}

	if e := nextEnd(t, n.ended); e.Cause != ua.EndUnsubscribed {
		t.Errorf("notifier ended %+v", e)
	}
}

// RFC 6665 §4.2.1.3, §4.2.2: a pending subscription is activated or rejected with NOTIFYs; one not refreshed
// expires with a terminated NOTIFY, reason timeout (§4.2.1.4).
func TestNotifierPendingAndExpiry(t *testing.T) {
	clock := netOf(t).Clock()
	n := newNotifierAgent(t, clock)
	sb := newSubscriberOf(t, clock)

	n.mu.Lock()
	n.pending = true
	n.mu.Unlock()

	sb.subscribe(t, n.p, "600")
	s := <-n.subs

	checkNotify(t, nextNotify(t, sb.notifys), "pending;expires=600", "v0 seq=0")

	if err := s.Activate(); err != nil {
		t.Fatal(err)
	}

	checkNotify(t, nextNotify(t, sb.notifys), "active", "v0 seq=1")

	// The subscriber refreshes at 300 s; dropping it once its 200 is sent, the notifier sees no refresh.
	netOf(t).Quiesce()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_ = sb.p.a.Layer().Shutdown(ctx)

	// The notifier takes the 200 before the clock jumps past its Timer F.
	netOf(t).Quiesce()

	// Its terminated NOTIFY goes unanswered: the subscription ends with its transaction (Timer F).
	advance(clock, 601*time.Second, sb.p, n.p)
	advance(clock, 33*time.Second, sb.p, n.p)

	if e := nextEnd(t, n.ended); e.Cause != ua.EndExpired {
		t.Errorf("notifier ended %+v", e)
	}

	// Rejection after the fact.
	n2 := newNotifierAgent(t, nil)
	sb2 := newSubscriberOf(t, nil)

	n2.mu.Lock()
	n2.pending = true
	n2.mu.Unlock()

	sb2.subscribe(t, n2.p, "600")
	s2 := <-n2.subs

	nextNotify(t, sb2.notifys)

	if err := s2.Terminate(sip.SubscriptionRejected, 0, ua.Notification{}); err != nil {
		t.Fatal(err)
	}

	checkNotify(t, nextNotify(t, sb2.notifys), "terminated;reason=rejected", "")

	if e := nextEnd(t, sb2.ended); e.Cause != ua.EndTerminated || e.State.Reason != sip.SubscriptionRejected {
		t.Errorf("subscriber ended %+v", e)
	}

	if e := nextEnd(t, n2.ended); e.Cause != ua.EndTerminated {
		t.Errorf("notifier ended %+v", e)
	}
}

// One NOTIFY at a time; a full state queued makes the NOTIFYs queued before it obsolete; a 481 to a NOTIFY ends the
// subscription (RFC 6665 §4.2.2).
func TestNotifierQueue(t *testing.T) {
	n := newNotifierAgent(t, nil)
	alice := newRaw(t)

	req := siptest.NewRequest("SUBSCRIBE", n.p.uri(), sip.UDP, alice.sock.Addr())
	req.Header.Add("Event", "test")
	req.Header.Add("Expires", "600")
	alice.sock.Send(sip.UDP, n.p.addr, req)

	s := <-n.subs

	first := alice.nextRequest("NOTIFY")

	for _, b := range []struct {
		body string
		full bool
	}{{"a", false}, {"b", true}, {"c", false}} {
		if err := s.Notify(ua.Notification{Bodies: []sip.BodyPart{sip.NewBodyPart("text/plain", []byte(b.body))}, Full: b.full}); err != nil {
			t.Fatal(err)
		}
	}

	alice.noRequest()
	alice.sock.Send(sip.UDP, n.p.addr, sip.NewResponse(first, 200, ""))

	for _, want := range []string{"b", "c"} {
		r := alice.nextRequest("NOTIFY")
		if string(r.Body) != want {
			t.Fatalf("NOTIFY %q, want %q", r.Body, want)
		}

		code := 200
		if want == "c" {
			code = 481
		}

		alice.sock.Send(sip.UDP, n.p.addr, sip.NewResponse(r, code, ""))
	}

	if e := nextEnd(t, n.ended); e.Cause != ua.EndLost || e.Response == nil || e.Response.StatusCode != 481 {
		t.Errorf("notifier ended %+v", e)
	}
}

// RFC 6665 §4.4.3: a SUBSCRIBE with Expires 0 fetches the state: the NOTIFY terminates the subscription at once.
func TestNotifierFetch(t *testing.T) {
	n := newNotifierAgent(t, nil)
	sb := newSubscriberOf(t, nil)

	sb.subscribe(t, n.p, "0")

	checkNotify(t, nextNotify(t, sb.notifys), "terminated;reason=timeout", "v0 seq=0")

	if e := nextEnd(t, n.ended); e.Cause != ua.EndExpired {
		t.Errorf("notifier ended %+v", e)
	}
}

// RFC 6665 §4.4.2: closing, the notifier terminates its subscriptions with reason deactivated, for the subscribers
// to subscribe again elsewhere.
func TestNotifierClose(t *testing.T) {
	n := newNotifierAgent(t, nil)
	sb := newSubscriberOf(t, nil)

	sb.subscribe(t, n.p, "600")
	<-n.subs
	nextNotify(t, sb.notifys)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := n.p.a.Close(ctx); err != nil {
		t.Fatal(err)
	}

	checkNotify(t, nextNotify(t, sb.notifys), "terminated;reason=deactivated", "v0 seq=1")

	if e := nextEnd(t, n.ended); e.Cause != ua.EndClosed {
		t.Errorf("notifier ended %+v", e)
	}
}
