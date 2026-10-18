package ua_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

// roster is the state of a resource in the tests: its members, a change adding one.
type roster []string

// rosterConfig renders the full state as "full <value> <members> seq=<seq>", a change as "add <value> <member>
// seq=<seq>": the value of each subscription, its filter, shows in what it gets.
func rosterConfig(partial bool, interval time.Duration) ua.ResourceConfig[roster, string] {
	cfg := ua.ResourceConfig[roster, string]{
		Full: func(value any, seq uint32, state roster) (ua.Notification, error) {
			return textNotification(fmt.Sprintf("full %v %v seq=%d", value, state, seq)), nil
		},
		MinInterval: interval,
	}

	if partial {
		cfg.Partial = func(value any, seq uint32, _ roster, delta string) (ua.Notification, error) {
			return textNotification(fmt.Sprintf("add %v %s seq=%d", value, delta, seq)), nil
		}
	}

	return cfg
}

func textNotification(s string) ua.Notification {
	return ua.Notification{Bodies: []sip.BodyPart{sip.NewBodyPart("text/plain", []byte(s))}}
}

// newResourceNotifier is an agent binding the SUBSCRIBEs of the "test" event to res, the From user of each its value.
func newResourceNotifier(t *testing.T, res ua.Source, minExpires time.Duration) *notifierPeer {
	n := &notifierPeer{subs: make(chan *ua.ServerSubscription, 4), ended: make(chan ua.End, 4)}

	n.p = newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnRequest = func(r *ua.Request) {
			from, _ := r.Message().Header.From()

			s, err := r.AcceptSubscription(ua.Acceptance{
				Expires:    3600 * time.Second,
				MinExpires: minExpires,
				Resource:   res,
				Value:      from.URI.User,
				Handler:    ua.ServerSubscriptionHandler{Ended: func(_ *ua.ServerSubscription, e ua.End) { n.ended <- e }},
			})
			if err != nil {
				return
			}

			n.subs <- s
		}
	})

	return n
}

// rawSubscribe subscribes the raw peer to the "test" event of n, and returns the SUBSCRIBE, its response, and the first
// NOTIFY.
func rawSubscribe(t *testing.T, r *raw, n *peer, user, expires string) (*sip.Request, *sip.Response, *sip.Request) {
	t.Helper()

	req := siptest.NewRequest("SUBSCRIBE", n.uri(), sip.UDP, r.sock.Addr())
	req.Header.Set("From", "<sip:"+user+"@"+r.sock.Addr().String()+">;tag="+sip.NewTag())
	req.Header.Add("Event", "test")
	req.Header.Add("Expires", expires)
	r.sock.Send(sip.UDP, n.addr, req)

	res := r.response()
	if res.StatusCode != 200 {
		return req, res, nil
	}

	return req, res, r.nextRequest("NOTIFY")
}

// RFC 6665 §4.2.2, RFC 4575 §4.4: each change reaches every subscription, rendered for it; a subscriber whose NOTIFY
// is in flight holds back no other, and gets the changes made meanwhile together, as one NOTIFY of the full state,
// then partial notifications again.
func TestResourceFanOut(t *testing.T) {
	res := ua.NewResource(roster{"a"}, rosterConfig(true, 0))
	n := newResourceNotifier(t, res, 0)

	fast := newSubscriberOf(t, nil)
	fast.subscribe(t, n.p, "600")
	<-n.subs

	checkNotify(t, nextNotify(t, fast.notifys), "active", "full alice [a] seq=0")
	netOf(t).Quiesce()

	slow := newRaw(t)
	_, _, first := rawSubscribe(t, slow, n.p, "carol", "600")
	<-n.subs

	if string(first.Body) != "full carol [a] seq=0" {
		t.Fatalf("first NOTIFY %q", first.Body)
	}

	// Carol leaves her NOTIFY unanswered: the changes reach alice one by one.
	state := roster{"a"}

	for _, m := range []string{"b", "c", "d"} {
		state = append(state[:len(state):len(state)], m)
		if err := res.Change(state, m); err != nil {
			t.Fatal(err)
		}

		netOf(t).Quiesce()
	}

	for i, m := range []string{"b", "c", "d"} {
		checkNotify(t, nextNotify(t, fast.notifys), "active", fmt.Sprintf("add alice %s seq=%d", m, i+1))
	}

	slow.noRequest()

	// Answered, carol gets the full state, once.
	slow.sock.Send(sip.UDP, n.p.addr, sip.NewResponse(first, 200, ""))

	full := slow.nextRequest("NOTIFY")
	if string(full.Body) != "full carol [a b c d] seq=1" {
		t.Fatalf("NOTIFY after the backlog %q", full.Body)
	}

	slow.sock.Send(sip.UDP, n.p.addr, sip.NewResponse(full, 200, ""))
	slow.noRequest()

	// Up to date again, she gets the next change as a partial notification.
	state = append(state[:len(state):len(state)], "e")
	_ = res.Change(state, "e")

	checkNotify(t, nextNotify(t, fast.notifys), "active", "add alice e seq=4")

	if r := slow.nextRequest("NOTIFY"); string(r.Body) != "add carol e seq=2" {
		t.Fatalf("NOTIFY %q", r.Body)
	}

	if got := len(res.Subscriptions()); got != 2 {
		t.Errorf("%d subscriptions", got)
	}
}

// Without partial notifications, each change sends the full state; a refresh sends it too (RFC 6665 §4.2.1.2), and
// the subscription ends detached from the resource.
func TestResourceFullOnly(t *testing.T) {
	res := ua.NewResource(roster{"a"}, rosterConfig(false, 0))
	n := newResourceNotifier(t, res, 0)

	sb := newSubscriberOf(t, nil)
	sub := sb.subscribe(t, n.p, "600")
	<-n.subs

	checkNotify(t, nextNotify(t, sb.notifys), "active", "full alice [a] seq=0")

	_ = res.Change(roster{"a", "b"}, "b")

	checkNotify(t, nextNotify(t, sb.notifys), "active", "full alice [a b] seq=1")

	if err := sub.Refresh(ua.Change{}, nil); err != nil {
		t.Fatal(err)
	}

	checkNotify(t, nextNotify(t, sb.notifys), "active", "full alice [a b] seq=2")

	if err := sub.Unsubscribe(ua.Change{}); err != nil {
		t.Fatal(err)
	}

	checkNotify(t, nextNotify(t, sb.notifys), "terminated;reason=timeout", "full alice [a b] seq=3")
	nextEnd(t, n.ended)

	if got := len(res.Subscriptions()); got != 0 {
		t.Errorf("%d subscriptions left", got)
	}
}

// RFC 4575 §3.9: the changes within MinInterval of the last one notified come together, at its end, as the full
// state; the first ones of a subscription go at once.
func TestResourceRate(t *testing.T) {
	clock := netOf(t).Clock()
	res := ua.NewResource(roster{"a"}, rosterConfig(true, 5*time.Second))
	n := newResourceNotifier(t, res, 0)

	sb := newSubscriberOf(t, clock)
	sb.subscribe(t, n.p, "600")
	<-n.subs

	checkNotify(t, nextNotify(t, sb.notifys), "active", "full alice [a] seq=0")

	// The first NOTIFY answered, the first change goes at once.
	netOf(t).Quiesce()

	_ = res.Change(roster{"a", "b"}, "b")

	checkNotify(t, nextNotify(t, sb.notifys), "active", "add alice b seq=1")

	_ = res.Change(roster{"a", "b", "c"}, "c")
	_ = res.Change(roster{"a", "b", "c", "d"}, "d")

	advance(clock, 4*time.Second)

	select {
	case r := <-sb.notifys:
		t.Fatalf("NOTIFY within the interval: %q", r.Message().Body)
	default:
	}

	advance(clock, time.Second)

	checkNotify(t, nextNotify(t, sb.notifys), "active", "full alice [a b c d] seq=2")

	advance(clock, 10*time.Second)

	select {
	case r := <-sb.notifys:
		t.Fatalf("NOTIFY without a change: %q", r.Message().Body)
	default:
	}
}

// RFC 6665 §4.2.1.1, TS 24.379 §9.2.2.3.4 step 3: a SUBSCRIBE, or a refresh, asking for less than the minimum gets a
// 423 with Min-Expires; a fetch is no subscription, and takes none.
func TestMinExpires(t *testing.T) {
	res := ua.NewResource(roster{"a"}, rosterConfig(false, 0))
	n := newResourceNotifier(t, res, 4294967295*time.Second)
	r := newRaw(t)

	_, tooBrief, _ := rawSubscribe(t, r, n.p, "carol", "3600")
	if tooBrief.StatusCode != 423 || tooBrief.Header.Get("Min-Expires") != "4294967295" {
		t.Fatalf("%d Min-Expires %q", tooBrief.StatusCode, tooBrief.Header.Get("Min-Expires"))
	}

	sub, ok, notify := rawSubscribe(t, r, n.p, "carol", "4294967295")
	if ok.StatusCode != 200 || ok.Header.Get("Expires") != "3600" {
		t.Fatalf("%d Expires %q", ok.StatusCode, ok.Header.Get("Expires"))
	}

	<-n.subs
	r.sock.Send(sip.UDP, n.p.addr, sip.NewResponse(notify, 200, ""))

	refresh := r.inDialog("SUBSCRIBE", sub, ok, 2)
	refresh.Header.Set("Event", "test")
	refresh.Header.Set("Expires", "60")
	r.sock.Send(sip.UDP, n.p.addr, refresh)

	if res := r.response(); res.StatusCode != 423 {
		t.Fatalf("refresh %d", res.StatusCode)
	}

	if _, fetch, _ := rawSubscribe(t, r, n.p, "dave", "0"); fetch.StatusCode != 200 {
		t.Fatalf("fetch %d", fetch.StatusCode)
	}
}

// RFC 6665 §4.1.3: a resource gone terminates its subscriptions with reason noresource, and takes no more.
func TestResourceTerminate(t *testing.T) {
	res := ua.NewResource(roster{"a"}, rosterConfig(true, 0))
	n := newResourceNotifier(t, res, 0)

	sb := newSubscriberOf(t, nil)
	sb.subscribe(t, n.p, "600")
	<-n.subs
	nextNotify(t, sb.notifys)

	res.Terminate(sip.SubscriptionNoResource, 0)

	checkNotify(t, nextNotify(t, sb.notifys), "terminated;reason=noresource", "")

	if e := nextEnd(t, n.ended); e.Cause != ua.EndTerminated {
		t.Errorf("notifier ended %+v", e)
	}

	if err := res.Change(roster{}, ""); err == nil {
		t.Error("a change to a terminated resource")
	}

	// A SUBSCRIBE after it gets its subscription terminated at once.
	r := newRaw(t)
	if _, _, notify := rawSubscribe(t, r, n.p, "carol", "600"); notify == nil ||
		notify.Header.Get("Subscription-State") != "terminated;reason=noresource" {
		t.Fatalf("NOTIFY %v", notify)
	}
}

// A resource fans out to the subscriptions of several agents; under concurrent changes, each subscriber, applying the
// partial notifications to the last full state, ends with the state of the resource.
func TestResourceConverges(t *testing.T) {
	res := ua.NewResource(roster{}, rosterConfig(true, 0))
	n1, n2 := newResourceNotifier(t, res, 0), newResourceNotifier(t, res, 0)

	subs := []*subscriberOf{newSubscriberOf(t, nil), newSubscriberOf(t, nil), newSubscriberOf(t, nil)}
	for i, sb := range subs {
		sb.notifys = make(chan *ua.Request, 256)
		sb.subscribe(t, []*peer{n1.p, n2.p}[i%2], "600")
	}

	for range subs {
		select {
		case <-n1.subs:
		case <-n2.subs:
		}
	}

	slow := newRaw(t)
	_, _, first := rawSubscribe(t, slow, n2.p, "carol", "600")
	<-n2.subs

	var (
		mu    sync.Mutex
		state roster
		wg    sync.WaitGroup
	)

	for g := range 4 {
		wg.Go(func() {
			for i := range 10 {
				mu.Lock()
				m := fmt.Sprintf("m%d.%d", g, i)
				state = append(state[:len(state):len(state)], m)
				_ = res.Change(state, m)
				mu.Unlock()
			}
		})
	}

	wg.Wait()
	slow.sock.Send(sip.UDP, n2.p.addr, sip.NewResponse(first, 200, ""))
	netOf(t).Quiesce()

	want := fmt.Sprint(res.State())

	for i, sb := range subs {
		var got roster

		for done := false; !done; {
			select {
			case r := <-sb.notifys:
				// "full <user> [<members>] seq=n" or "add <user> <member> seq=n".
				body := string(r.Message().Body)

				if strings.HasPrefix(body, "full ") {
					got = strings.Fields(body[strings.Index(body, "[")+1 : strings.Index(body, "]")])
				} else {
					got = append(got, strings.Fields(body)[2])
				}
			default:
				done = true
			}
		}

		if fmt.Sprint(got) != want {
			t.Errorf("subscriber %d: %v, want %s", i, got, want)
		}
	}

	if r := slow.nextRequest("NOTIFY"); !strings.Contains(string(r.Body), want[1:len(want)-1]) {
		t.Errorf("slow subscriber: %q, want %s", r.Body, want)
	}
}
