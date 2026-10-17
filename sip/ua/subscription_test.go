package ua_test

import (
	"context"
	"fmt"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

type subEvent struct {
	kind   string
	s      *ua.Subscription
	res    *sip.Response
	notify *sip.Request
	end    ua.End
}

// subscriber is an agent subscribing to a raw notifier.
type subscriber struct {
	t      *testing.T
	p      *peer
	events chan subEvent
	fork   atomic.Bool
}

func newSubscriber(t *testing.T, clock transaction.Clock) *subscriber {
	t.Helper()

	return &subscriber{t: t, p: newPeer(t, "alice", clock, ua.SessionTimer{}), events: make(chan subEvent, 64)}
}

func (sb *subscriber) record(e subEvent) {
	select {
	case sb.events <- e:
	default:
		sb.t.Errorf("subscription event queue full, %s dropped", e.kind)
	}
}

func (sb *subscriber) handler() ua.SubscriptionHandler {
	return ua.SubscriptionHandler{
		Accepted: func(s *ua.Subscription, res *sip.Response) { sb.record(subEvent{kind: "accepted", s: s, res: res}) },
		Notify: func(s *ua.Subscription, r *ua.Request) {
			sb.record(subEvent{kind: "notify", s: s, notify: r.Message()})
		},
		Forked: func(*ua.Subscription, *sip.Request) bool { return sb.fork.Load() },
		Ended:  func(s *ua.Subscription, e ua.End) { sb.record(subEvent{kind: "ended", s: s, end: e}) },
	}
}

func (sb *subscriber) next(kind string) subEvent {
	sb.t.Helper()

	select {
	case e := <-sb.events:
		if e.kind != kind {
			sb.t.Fatalf("%s event, want %s (%+v)", e.kind, kind, e)
		}

		return e
	case <-time.After(wait):
		sb.t.Fatalf("no %s event", kind)
		return subEvent{}
	}
}

func (sb *subscriber) none() {
	sb.t.Helper()

	select {
	case e := <-sb.events:
		sb.t.Fatalf("unexpected %s event (%+v)", e.kind, e)
	case <-time.After(100 * time.Millisecond):
	}
}

// notifier is a raw notifier of the reg event package.
type notifier struct {
	t    *testing.T
	sock *siptest.Socket
	tag  string
	seq  uint32

	sub  *sip.Request
	flow sip.Flow
}

func newNotifier(t *testing.T) *notifier {
	return &notifier{t: t, sock: siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0)), tag: "n1"}
}

func (sb *subscriber) subscribe(n *notifier) *ua.Subscription {
	sb.t.Helper()

	uri, _ := sip.ParseURI("sip:alice@" + n.sock.Addr().String())
	req := sip.NewRequest("SUBSCRIBE", uri)
	req.Header.Add("From", "<"+sb.p.uri()+">")
	req.Header.Add("To", "<"+uri.String()+">")
	req.Header.Add("Event", "reg")
	req.Header.Add("Accept", "application/reginfo+xml")
	req.Header.Add("Expires", "600")

	s, err := sb.p.a.Subscribe(req, sb.handler())
	if err != nil {
		sb.t.Fatal(err)
	}

	return s
}

// recvSubscribe is the next SUBSCRIBE the notifier receives.
func (n *notifier) recvSubscribe() *sip.Request {
	n.t.Helper()

	for {
		req, f := n.sock.RecvRequest()
		if req.Method == "SUBSCRIBE" {
			if n.sub == nil {
				n.sub = req
			}

			n.flow = f

			return req
		}
	}
}

func (n *notifier) respond(req *sip.Request, code int, expires string) {
	res := sip.NewResponse(req, code, "")
	_ = res.Header.SetToTag(n.tag)

	if expires != "" {
		res.Header.Add("Expires", expires)
	}

	n.sock.Send(sip.UDP, n.flow.Remote, res)
}

// notifyRequest is the next NOTIFY of the notifier tag for the subscription.
func (n *notifier) notifyRequest(tag, event, state string) *sip.Request {
	n.seq++

	contact, _ := n.sub.Header.Contacts()
	from, _ := n.sub.Header.From()

	req := siptest.NewRequest("NOTIFY", contact[0].URI.String(), sip.UDP, n.sock.Addr())
	req.Header.Set("From", "<sip:reg@"+n.sock.Addr().String()+">;tag="+tag)
	req.Header.Set("To", from.String())
	req.Header.Set("Call-ID", n.sub.Header.CallID())
	req.Header.Set("CSeq", fmt.Sprintf("%d NOTIFY", n.seq))
	req.Header.Set("Contact", "<sip:reg@"+n.sock.Addr().String()+">")
	req.Header.Add("Event", event)
	req.Header.Add("Subscription-State", state)
	req.SetBodies(sip.NewBodyPart("application/reginfo+xml", []byte("<reginfo/>")))

	return req
}

// notify sends a NOTIFY of the notifier tag, and returns its response.
func (n *notifier) notify(tag, state string) *sip.Response {
	n.t.Helper()

	return n.send(n.notifyRequest(tag, "reg", state))
}

func (n *notifier) send(req *sip.Request) *sip.Response {
	n.t.Helper()

	n.sock.Send(sip.UDP, n.flow.Remote, req)

	cseq, _ := req.Header.CSeq()

	for {
		res, _ := n.sock.RecvResponse()
		if c, _ := res.Header.CSeq(); c == cseq {
			return res
		}
	}
}

// RFC 6665 §4.1: the 2xx accepts the subscription, its first NOTIFY creates the dialog; refreshed at half its
// duration, ended by a terminated NOTIFY.
func TestSubscription(t *testing.T) {
	clock := siptest.NewClock()
	sb, n := newSubscriber(t, clock), newNotifier(t)
	s := sb.subscribe(n)

	if s.State() != ua.SubscriptionNeutral {
		t.Errorf("state %s before any NOTIFY", s.State())
	}

	req := n.recvSubscribe()
	if req.Header.Get("Event") != "reg" || req.Header.Get("Expires") != "600" || !req.Header.Has("Contact") {
		t.Fatalf("SUBSCRIBE %s", req)
	}

	n.respond(req, 200, "600")
	sb.next("accepted")

	if res := n.notify(n.tag, "active;expires=600"); res.StatusCode != 200 {
		t.Fatalf("NOTIFY %d", res.StatusCode)
	}

	if e := sb.next("notify"); e.s != s || s.State() != ua.SubscriptionActive || s.Duration() != 600*time.Second {
		t.Errorf("after the NOTIFY: %s, %v", s.State(), s.Duration())
	}

	if _, ok := s.Dialog(); !ok {
		t.Error("no dialog")
	}

	advance(clock, 300*time.Second, sb.p)

	refresh := n.recvSubscribe()
	if to, _ := refresh.Header.To(); to.Tag() != n.tag || refresh.Header.Get("Expires") != "600" || refresh.Header.Get("Event") != "reg" {
		t.Fatalf("refresh %s", refresh)
	}

	if c1, _ := req.Header.CSeq(); func() bool { c2, _ := refresh.Header.CSeq(); return c2.Seq <= c1.Seq }() {
		t.Error("refresh CSeq not above the initial one")
	}

	n.respond(refresh, 200, "600")
	n.notify(n.tag, "active;expires=600")
	sb.next("notify")

	n.notify(n.tag, "terminated;reason=deactivated")
	sb.next("notify")

	e := sb.next("ended")
	if e.end.Cause != ua.EndTerminated || e.end.State.Reason != sip.SubscriptionDeactivated || !e.end.Remote {
		t.Errorf("ended %+v", e.end)
	}

	if ok, _ := e.end.State.Resubscribe(); !ok {
		t.Error("deactivated: no resubscribe")
	}

	if d, _, _, _ := ua.Tables(sb.p.a); d != 0 {
		t.Errorf("%d dialogs left", d)
	}
}

// RFC 6665 §4.1.2.4: a NOTIFY may come before the 2xx.
func TestNotifyBeforeTheResponse(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	s := sb.subscribe(n)

	req := n.recvSubscribe()

	if res := n.notify(n.tag, "pending"); res.StatusCode != 200 {
		t.Fatalf("NOTIFY %d", res.StatusCode)
	}

	sb.next("notify")

	if s.State() != ua.SubscriptionPending {
		t.Errorf("state %s", s.State())
	}

	n.respond(req, 202, "300")
	sb.next("accepted")

	if s.Duration() != 300*time.Second {
		t.Errorf("duration %v, want the Expires of the 2xx", s.Duration())
	}
}

// RFC 6665 §4.1.2.4: without a NOTIFY within Timer N, the subscription fails; a NOTIFY afterwards gets a 481.
func TestNoNotify(t *testing.T) {
	clock := siptest.NewClock()
	sb, n := newSubscriber(t, clock), newNotifier(t)
	sb.subscribe(n)

	n.respond(n.recvSubscribe(), 200, "600")
	sb.next("accepted")

	advance(clock, 33*time.Second, sb.p)

	if e := sb.next("ended"); e.end.Cause != ua.EndNoNotify {
		t.Errorf("ended %+v", e.end)
	}

	if res := n.notify(n.tag, "active"); res.StatusCode != 481 {
		t.Errorf("late NOTIFY %d, want 481", res.StatusCode)
	}

	if ua.Subscribing(sb.p.a) != 0 {
		t.Error("the SUBSCRIBE is still waiting")
	}
}

// RFC 6665 §4.1.2.1: a SUBSCRIBE refused ends the subscription.
func TestSubscriptionRejected(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	sb.subscribe(n)

	n.respond(n.recvSubscribe(), 489, "")

	if e := sb.next("ended"); e.end.Cause != ua.EndRejected || e.end.Response.StatusCode != 489 {
		t.Errorf("ended %+v", e.end)
	}
}

// RFC 6665 §4.1.3, §8.2.1: a NOTIFY of another event gets a 489; one of no subscription a 481.
func TestNotifyMismatch(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	sb.subscribe(n)

	n.respond(n.recvSubscribe(), 200, "600")
	sb.next("accepted")
	n.notify(n.tag, "active")
	sb.next("notify")

	if res := n.send(n.notifyRequest(n.tag, "presence", "active")); res.StatusCode != 489 {
		t.Errorf("NOTIFY of another event %d", res.StatusCode)
	}

	unknown := n.notifyRequest(n.tag, "reg", "active")
	unknown.Header.Set("Call-ID", "unknown")

	if res := n.send(unknown); res.StatusCode != 481 {
		t.Errorf("NOTIFY of no subscription %d", res.StatusCode)
	}

	sb.none()
}

// RFC 6665 §4.1.2.3: Unsubscribe sends Expires 0; the terminated NOTIFY ends the subscription.
func TestUnsubscribe(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	s := sb.subscribe(n)

	n.respond(n.recvSubscribe(), 200, "600")
	sb.next("accepted")
	n.notify(n.tag, "active;expires=600")
	sb.next("notify")

	if err := s.Unsubscribe(); err != nil {
		t.Fatal(err)
	}

	req := n.recvSubscribe()
	if req.Header.Get("Expires") != "0" {
		t.Fatalf("unsubscribe Expires %q", req.Header.Get("Expires"))
	}

	n.respond(req, 200, "0")
	n.notify(n.tag, "terminated;reason=timeout")
	sb.next("notify")

	if e := sb.next("ended"); e.end.Cause != ua.EndTerminated || e.end.State.Reason != sip.SubscriptionTimeout {
		t.Errorf("ended %+v", e.end)
	}

	if err := s.Unsubscribe(); err == nil {
		t.Error("Unsubscribe of an ended subscription")
	}
}

// RFC 6665 §4.1.2.2: a refresh refused with 481 ends the subscription; with 500 the subscription lasts until its
// expiry, the refresh tried again meanwhile.
func TestRefreshFailures(t *testing.T) {
	for _, code := range []int{481, 500} {
		clock := siptest.NewClock()
		sb, n := newSubscriber(t, clock), newNotifier(t)
		s := sb.subscribe(n)

		n.respond(n.recvSubscribe(), 200, "600")
		sb.next("accepted")
		n.notify(n.tag, "active;expires=600")
		sb.next("notify")

		done, f := results()

		if err := s.Refresh(f); err != nil {
			t.Fatal(err)
		}

		n.respond(n.recvSubscribe(), code, "")

		if res := recv(t, done); res.code() != code {
			t.Fatalf("refresh %d", res.code())
		}

		if code == 481 {
			if e := sb.next("ended"); e.end.Cause != ua.EndRejected {
				t.Errorf("ended %+v", e.end)
			}

			continue
		}

		// Timer N does not end it: the 500 brings no NOTIFY.
		advance(clock, 40*time.Second, sb.p)
		sb.none()

		// The automatic refreshes, at 300 s, then halfway to the expiry, fail too; the subscription expires.
		for _, step := range []time.Duration{260 * time.Second, 150 * time.Second} {
			advance(clock, step, sb.p)
			n.respond(n.recvSubscribe(), 500, "")
		}

		advance(clock, 160*time.Second, sb.p)

		if e := sb.next("ended"); e.end.Cause != ua.EndExpired {
			t.Errorf("ended %+v", e.end)
		}
	}
}

// RFC 6665 §4.1.4: a NOTIFY of another notifier gets a 481, unless Handler.Forked keeps its subscription.
func TestForkedSubscription(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	first := sb.subscribe(n)

	n.respond(n.recvSubscribe(), 200, "600")
	sb.next("accepted")
	n.notify(n.tag, "active")
	sb.next("notify")

	if res := n.notify("n2", "active"); res.StatusCode != 481 {
		t.Errorf("forked NOTIFY %d, want 481", res.StatusCode)
	}

	sb.none()

	sb.fork.Store(true)

	if res := n.notify("n3", "active;expires=120"); res.StatusCode != 200 {
		t.Errorf("forked NOTIFY %d, want 200", res.StatusCode)
	}

	e := sb.next("notify")
	if e.s == first || e.s.State() != ua.SubscriptionActive || e.s.Duration() != 120*time.Second {
		t.Errorf("fork %p (first %p) %s %v", e.s, first, e.s.State(), e.s.Duration())
	}

	if d, _, _, _ := ua.Tables(sb.p.a); d != 2 {
		t.Errorf("%d dialogs, want 2", d)
	}
}

// Close unsubscribes, and ends the subscriptions.
func TestCloseUnsubscribes(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	sb.subscribe(n)

	n.respond(n.recvSubscribe(), 200, "600")
	sb.next("accepted")
	n.notify(n.tag, "active")
	sb.next("notify")

	go func() {
		req := n.recvSubscribe()
		n.respond(req, 200, "0")
	}()

	ctx, cancel := contextWithTimeout(time.Second)
	defer cancel()

	if err := sb.p.a.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if e := sb.next("ended"); e.end.Cause != ua.EndClosed {
		t.Errorf("ended %+v", e.end)
	}
}

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// Unsubscribe before the first NOTIFY ends the subscription at once; its NOTIFY then gets a 481.
func TestUnsubscribeBeforeNotify(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	s := sb.subscribe(n)

	n.respond(n.recvSubscribe(), 200, "600")
	sb.next("accepted")

	if err := s.Unsubscribe(); err != nil {
		t.Fatal(err)
	}

	if e := sb.next("ended"); e.end.Cause != ua.EndUnsubscribed {
		t.Errorf("ended %+v", e.end)
	}

	if res := n.notify(n.tag, "active"); res.StatusCode != 481 {
		t.Errorf("NOTIFY %d, want 481", res.StatusCode)
	}
}
