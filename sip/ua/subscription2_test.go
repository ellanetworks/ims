package ua_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

// active brings a subscription of sb to n up: its 200 with Expires, then an active NOTIFY.
func active(t *testing.T, sb *subscriber, n *notifier, expires string, notifyState string) *ua.Subscription {
	t.Helper()

	s := sb.subscribe(n)
	n.respond(n.recvSubscribe(), 200, expires)
	sb.next("accepted")

	if res := n.notify(n.tag, notifyState); res.StatusCode != 200 {
		t.Fatalf("NOTIFY %d", res.StatusCode)
	}

	sb.next("notify")

	return s
}

// RFC 6665 §4.1.3: a NOTIFY's expires moves the expiry, without pushing the refresh toward it: the refresh still
// comes halfway through the duration the 2xx granted.
func TestRefreshDoesNotDrift(t *testing.T) {
	clock := siptest.NewClock()
	sb, n := newSubscriber(t, clock), newNotifier(t)
	active(t, sb, n, "600", "active;expires=600")

	for elapsed := 30; elapsed < 300; elapsed += 30 {
		advance(clock, 30*time.Second, sb.p)
		n.notify(n.tag, "active;expires="+strconv.Itoa(600-elapsed))
		sb.next("notify")
	}

	// 270 s in: the refresh is due at 300 s, not later.
	advance(clock, 31*time.Second, sb.p)

	if req := n.recvSubscribe(); req.Header.Get("Expires") != "600" {
		t.Fatalf("refresh %s", req)
	}
}

// TS 24.229 §5.1.1.3: a subscription granted for more than 1200 s is refreshed 600 s before it expires, also once a
// NOTIFY shortened it.
func TestRefreshBeforeShortenedExpiry(t *testing.T) {
	clock := siptest.NewClock()
	sb, n := newSubscriber(t, clock), newNotifier(t)
	s := active(t, sb, n, "600000", "active;expires=1000")

	if got := s.Expires().Sub(clock.Now()); got != 1000*time.Second {
		t.Errorf("expires in %v", got)
	}

	advance(clock, 399*time.Second, sb.p)
	n.sock.RecvNone(50 * time.Millisecond)

	advance(clock, 2*time.Second, sb.p)
	n.recvSubscribe()
}

// RFC 6665 §4.1.2.4: a terminated NOTIFY overtaking the 2xx ends the subscription; the 2xx then reports nothing.
func TestNothingAfterEnded(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	sb.subscribe(n)

	req := n.recvSubscribe()

	n.notify(n.tag, "terminated;reason=rejected")
	sb.next("notify")

	if e := sb.next("ended"); e.end.Cause != ua.EndTerminated {
		t.Errorf("ended %+v", e.end)
	}

	n.respond(req, 200, "600")
	sb.none()
}

// RFC 6665 §4.1.2.2: a refresh repeats the bodies of the initial SUBSCRIBE, or those of its Change; an unsubscribe
// has none.
func TestRefreshBodies(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	filter := sip.NewBodyPart("application/simple-filter+xml", []byte("<filter-set/>"))

	s := sb.subscribeWith(n, []sip.BodyPart{filter})
	n.respond(n.recvSubscribe(), 200, "600")
	sb.next("accepted")
	n.notify(n.tag, "active")
	sb.next("notify")

	refresh := func(c ua.Change) *sip.Request {
		t.Helper()

		if err := s.Refresh(c, nil); err != nil {
			t.Fatal(err)
		}

		req := n.recvSubscribe()
		n.respond(req, 200, "600")
		n.notify(n.tag, "active")
		sb.next("notify")

		return req
	}

	if req := refresh(ua.Change{}); string(req.Body) != "<filter-set/>" || req.Header.ContentType() != "application/simple-filter+xml" {
		t.Errorf("refresh body %q of type %q", req.Body, req.Header.ContentType())
	}

	other := sip.NewBodyPart("application/simple-filter+xml", []byte("<filter-set other=''/>"))
	if req := refresh(ua.Change{Bodies: []sip.BodyPart{other}}); string(req.Body) != "<filter-set other=''/>" ||
		req.Header.Count("Content-Type") != 1 {
		t.Errorf("refresh with a Change: body %q, %d Content-Type", req.Body, req.Header.Count("Content-Type"))
	}

	if err := s.Unsubscribe(ua.Change{}); err != nil {
		t.Fatal(err)
	}

	if req := n.recvSubscribe(); len(req.Body) != 0 || req.Header.Has("Content-Type") {
		t.Errorf("unsubscribe body %q, Content-Type %q", req.Body, req.Header.ContentType())
	}
}

// RFC 3261 §22.2: a challenged refresh is built anew, Prepare's header fields once.
func TestChallengedRefresh(t *testing.T) {
	sb, n := newSubscriberWith(t, nil, func(c *ua.Config) {
		prepare := c.Prepare
		c.Prepare = func(req *sip.Request) error {
			if err := prepare(req); err != nil {
				return err
			}

			req.Header.Add("P-Access-Network-Info", "3GPP-E-UTRAN-FDD")

			return nil
		}
		c.Authenticate = func(req *sip.Request, _ *sip.Response) error {
			req.Header.Set("Authorization", `Digest username="alice"`)
			return nil
		}
	}), newNotifier(t)
	s := active(t, sb, n, "600", "active")

	_ = s.Refresh(ua.Change{}, nil)

	n.respond(n.recvSubscribe(), 401, "")

	req := n.recvSubscribe()
	if req.Header.Count("P-Access-Network-Info") != 1 || req.Header.Count("Authorization") != 1 || req.Header.Count("Via") != 1 {
		t.Errorf("challenged refresh:\n%s", req)
	}
}

// RFC 6665 §4.1.2.3: an unsubscribe waits for the terminated NOTIFY, not for another one; one that fails ends the
// subscription.
func TestUnsubscribeWaitsAndFails(t *testing.T) {
	clock := siptest.NewClock()
	sb, n := newSubscriber(t, clock), newNotifier(t)
	s := active(t, sb, n, "600", "active")

	_ = s.Unsubscribe(ua.Change{})

	n.respond(n.recvSubscribe(), 200, "0")

	// A NOTIFY crossing the unsubscribe does not end the wait for the terminated one.
	n.notify(n.tag, "active;expires=600")
	sb.next("notify")
	advance(clock, 20*time.Second, sb.p)
	sb.none()

	advance(clock, 13*time.Second, sb.p)

	if e := sb.next("ended"); e.end.Cause != ua.EndUnsubscribed {
		t.Errorf("ended %+v", e.end)
	}

	sb2, n2 := newSubscriber(t, nil), newNotifier(t)
	s2 := active(t, sb2, n2, "600", "active")

	_ = s2.Unsubscribe(ua.Change{})

	n2.respond(n2.recvSubscribe(), 500, "")

	if e := sb2.next("ended"); e.end.Cause != ua.EndUnsubscribed || e.end.Response.StatusCode != 500 {
		t.Errorf("ended %+v", e.end)
	}
}

// RFC 6665 §4.1.3, §4.2.2: a NOTIFY the application refuses with 481 ends the subscription; one it refuses with 400
// leaves it.
func TestNotifyVerdict(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	s := active(t, sb, n, "600", "active")

	sb.verdict.Store(400)

	if res := n.notify(n.tag, "active"); res.StatusCode != 400 {
		t.Fatalf("NOTIFY %d", res.StatusCode)
	}

	sb.next("notify")
	sb.none()

	if s.State() != ua.SubscriptionActive {
		t.Errorf("state %s after a 400", s.State())
	}

	sb.verdict.Store(481)

	if res := n.notify(n.tag, "active"); res.StatusCode != 481 {
		t.Fatalf("NOTIFY %d", res.StatusCode)
	}

	sb.next("notify")

	if e := sb.next("ended"); e.end.Cause != ua.EndDropped {
		t.Errorf("ended %+v", e.end)
	}
}

// RFC 6665 §4.1.3: expires 0 leaves the subscription to its terminated NOTIFY, or to expire.
func TestNotifyExpiresZero(t *testing.T) {
	clock := siptest.NewClock()
	sb, n := newSubscriber(t, clock), newNotifier(t)
	active(t, sb, n, "600", "active")

	n.notify(n.tag, "active;expires=0")
	sb.next("notify")

	advance(clock, 33*time.Second, sb.p)

	if e := sb.next("ended"); e.end.Cause != ua.EndExpired {
		t.Errorf("ended %+v", e.end)
	}
}

// RFC 6665 §4.2.1.1: without Expires in the 2xx, the duration asked for.
func TestNoExpiresInTheResponse(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)

	if s := active(t, sb, n, "", "active"); s.Duration() != 600*time.Second {
		t.Errorf("duration %v", s.Duration())
	}
}

// RFC 6665 §4.2.1.1: a 423 is answered with its Min-Expires, initial SUBSCRIBE and refresh alike.
func TestIntervalTooBriefSubscription(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	s := sb.subscribe(n)

	tooBrief := func(req *sip.Request) {
		res := sip.NewResponse(req, 423, "")
		_ = res.Header.SetToTag(n.tag)
		res.Header.Add("Min-Expires", "4294967295")
		n.sock.Send(sip.UDP, n.flow.Remote, res)
	}

	tooBrief(n.recvSubscribe())

	if req := n.recvSubscribe(); req.Header.Get("Expires") != "4294967295" {
		t.Fatalf("SUBSCRIBE after the 423:\n%s", req)
	} else {
		n.respond(req, 200, "4294967295")
	}

	sb.next("accepted")
	n.notify(n.tag, "active")
	sb.next("notify")

	_ = s.Refresh(ua.Change{}, nil)

	tooBrief(n.recvSubscribe())

	if req := n.recvSubscribe(); req.Header.Get("Expires") != "4294967295" {
		t.Fatalf("refresh after the 423:\n%s", req)
	}
}

// RFC 6665 §4.1.2.2, RFC 5839: a 204 to a refresh brings no NOTIFY; Timer N stops.
func TestRefreshWithoutNotify(t *testing.T) {
	clock := siptest.NewClock()
	sb, n := newSubscriber(t, clock), newNotifier(t)
	s := active(t, sb, n, "600", "active")

	done, f := results()

	_ = s.Refresh(ua.Change{}, f)

	n.respond(n.recvSubscribe(), 204, "600")

	// The 204 is taken before the clock passes Timer N.
	if res := recv(t, done).res; res.StatusCode != 204 {
		t.Fatalf("refresh %d", res.StatusCode)
	}

	advance(clock, 40*time.Second, sb.p)
	sb.none()
}

// A notifier whose fork was refused gets a 481 to its next NOTIFYs, Forked not asked again.
func TestRefusedForkStaysRefused(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	active(t, sb, n, "600", "active")

	for range 2 {
		if res := n.notify("n2", "active"); res.StatusCode != 481 {
			t.Errorf("forked NOTIFY %d", res.StatusCode)
		}
	}

	sb.fork.Store(true)

	if res := n.notify("n2", "active"); res.StatusCode != 481 {
		t.Errorf("refused fork NOTIFY %d", res.StatusCode)
	}

	sb.none()
}

// Drop ends a subscription without a SUBSCRIBE; its NOTIFYs then get a 481.
func TestDrop(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	s := active(t, sb, n, "600", "active")

	s.Drop()

	if e := sb.next("ended"); e.end.Cause != ua.EndDropped {
		t.Errorf("ended %+v", e.end)
	}

	n.sock.RecvNone(100 * time.Millisecond)

	if res := n.notify(n.tag, "active"); res.StatusCode != 481 {
		t.Errorf("NOTIFY after Drop %d", res.StatusCode)
	}
}

// RFC 5057 §5.3, §5.6: in the dialog of a subscription, another event's NOTIFY gets a 489, BYE a 481, OPTIONS a 200,
// SUBSCRIBE a 603; a refresh failure that destroys the dialog ends the subscription (RFC 5057 §5.1).
func TestSubscriptionDialogRequests(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	s := active(t, sb, n, "600", "active")

	inDialog := func(method string) *sip.Request {
		req := n.notifyRequest(n.tag, "reg", "active")
		req.Method = method
		req.Header.Set("CSeq", strconv.Itoa(int(n.seq))+" "+method)

		return req
	}

	for _, c := range []struct {
		req  *sip.Request
		code int
	}{
		{n.notifyRequest(n.tag, "presence", "active"), 489},
		{inDialog("BYE"), 481},
		{inDialog("OPTIONS"), 200},
		{inDialog("SUBSCRIBE"), 603},
	} {
		if res := n.send(c.req); res.StatusCode != c.code {
			t.Errorf("%s %s: %d, want %d", c.req.Method, c.req.Header.Get("Event"), res.StatusCode, c.code)
		}
	}

	sb.none()

	_ = s.Refresh(ua.Change{}, nil)

	n.respond(n.recvSubscribe(), 404, "")

	if e := sb.next("ended"); e.end.Cause != ua.EndLost {
		t.Errorf("ended %+v", e.end)
	}
}

// Close unsubscribes, and answers the terminated NOTIFY that follows with a 200.
func TestCloseWaitsForTheTerminatedNotify(t *testing.T) {
	sb, n := newSubscriber(t, nil), newNotifier(t)
	active(t, sb, n, "600", "active")

	answered := make(chan int, 1)

	go func() {
		req := n.recvSubscribe()
		if req.Header.Get("Expires") != "0" {
			t.Errorf("unsubscribe Expires %q", req.Header.Get("Expires"))
		}

		n.respond(req, 200, "0")

		answered <- n.notify(n.tag, "terminated;reason=timeout").StatusCode
	}()

	ctx, cancel := contextWithTimeout(2 * time.Second)
	defer cancel()

	_ = sb.p.a.Close(ctx)

	select {
	case code := <-answered:
		if code != 200 {
			t.Errorf("terminated NOTIFY %d", code)
		}
	case <-time.After(wait):
		t.Fatal("the notifier got no unsubscribe, or no answer to its NOTIFY")
	}

	sb.next("notify")

	if e := sb.next("ended"); e.end.Cause != ua.EndClosed {
		t.Errorf("ended %+v", e.end)
	}
}
