package ua_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

type change struct {
	event string
	state []ua.Published
}

// compositor is an agent with a store for the "presence" event, its changes recorded.
type compositor struct {
	p       *peer
	store   *ua.PublicationStore
	ready   chan struct{}
	changes chan change
}

func newCompositor(t *testing.T, clock *siptest.Clock) *compositor {
	c := &compositor{changes: make(chan change, 8), ready: make(chan struct{})}

	c.p = newPeerWith(t, "esc", func(cfg *ua.Config) {
		if clock != nil {
			cfg.Transaction.Clock = clock
		}

		cfg.Allow = append(cfg.Allow, "PUBLISH")
		cfg.OnRequest = func(r *ua.Request) {
			<-c.ready
			c.store.Handle(r)
		}
	})

	defer close(c.ready)

	c.store = c.p.a.NewPublicationStore(ua.StoreConfig{
		Events:         []string{"presence"},
		MinExpires:     60 * time.Second,
		DefaultExpires: 3600 * time.Second,
		Changed:        func(_ sip.URI, event string, state []ua.Published) { c.changes <- change{event, state} },
	})

	return c
}

func (c *compositor) next(t *testing.T) change {
	t.Helper()

	select {
	case ch := <-c.changes:
		return ch
	case <-time.After(wait):
		t.Fatal("no change")
		return change{}
	}
}

func publishRequest(to *peer, from string, body string, expires string) *sip.Request {
	uri, _ := sip.ParseURI(to.uri())
	req := sip.NewRequest("PUBLISH", uri)
	req.Header.Add("From", "<"+from+">")
	req.Header.Add("To", "<"+to.uri()+">")
	req.Header.Add("Event", "presence")
	req.Header.Add("Expires", expires)
	req.SetBodies(sip.NewBodyPart("application/pidf+xml", []byte(body)))

	return req
}

// RFC 3903 §4, §6: an initial publication, its modification, its refresh before it expires and its removal; each
// 2xx with a new entity-tag; the compositor reports the state of the resource as it changes.
func TestPublication(t *testing.T) {
	clock := netOf(t).Clock()
	esc := newCompositor(t, clock)
	alice := newPeer(t, "alice", clock, ua.SessionTimer{})

	published := make(chan *sip.Response, 4)
	ended := make(chan ua.End, 1)

	p, err := alice.a.Publish(publishRequest(esc.p, alice.uri(), "open", "120"), ua.PublicationHandler{
		Published: func(_ *ua.Publication, res *sip.Response) { published <- res },
		Ended:     func(_ *ua.Publication, e ua.End) { ended <- e },
	})
	if err != nil {
		t.Fatal(err)
	}

	if ch := esc.next(t); len(ch.state) != 1 || string(ch.state[0].Bodies[0].Body) != "open" {
		t.Fatalf("state %+v", ch.state)
	}

	res := <-published
	first, _, _ := res.Header.SIPETag()

	if res.Header.Get("Expires") != "120" || first == "" || p.ETag() != first {
		t.Fatalf("2xx Expires %q SIP-ETag %q, publication %q", res.Header.Get("Expires"), first, p.ETag())
	}

	done, f := results()

	if err := p.Modify([]sip.BodyPart{sip.NewBodyPart("application/pidf+xml", []byte("closed"))}, f); err != nil {
		t.Fatal(err)
	}

	recv(t, done)
	<-published

	if ch := esc.next(t); len(ch.state) != 1 || string(ch.state[0].Bodies[0].Body) != "closed" {
		t.Fatalf("state %+v", ch.state)
	}

	modified := p.ETag()
	if modified == first {
		t.Error("the same entity-tag after a modification")
	}

	// The refresh at half the interval keeps the state, with a new entity-tag, and changes nothing.
	advance(clock, 61*time.Second, alice, esc.p)
	<-published

	if p.ETag() == modified {
		t.Error("no new entity-tag after the refresh")
	}

	netOf(t).Quiesce()

	select {
	case ch := <-esc.changes:
		t.Fatalf("a refresh reported as a change: %+v", ch)
	default:
	}

	if err := p.Remove(f); err != nil {
		t.Fatal(err)
	}

	recv(t, done)

	if ch := esc.next(t); len(ch.state) != 0 {
		t.Fatalf("state after the removal %+v", ch.state)
	}

	if e := <-ended; e.Cause != ua.EndRemoved {
		t.Errorf("ended %+v", e)
	}
}

// RFC 3903 §5: a 412 (the compositor lost the state) makes the publication initial again, with its last state; the
// PUBLISHes go one at a time, a change made meanwhile replacing the one waiting.
func TestPublicationRecovery(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	esc := newUAS(t, alice)

	uri, _ := sip.ParseURI(esc.uri())
	req := sip.NewRequest("PUBLISH", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+esc.uri()+">")
	req.Header.Add("Event", "presence")
	req.Header.Add("Expires", "3600")
	req.SetBodies(sip.NewBodyPart("application/pidf+xml", []byte("s1")))

	p, err := alice.a.Publish(req, ua.PublicationHandler{})
	if err != nil {
		t.Fatal(err)
	}

	initial := esc.nextRequest("PUBLISH")
	if initial.Header.Has("SIP-If-Match") {
		t.Fatal("an initial PUBLISH with SIP-If-Match")
	}

	// Two changes while the PUBLISH is in flight: the second replaces the first.
	stale, f1 := results()
	done, f2 := results()

	_ = p.Modify([]sip.BodyPart{sip.NewBodyPart("application/pidf+xml", []byte("s2"))}, f1)
	_ = p.Modify([]sip.BodyPart{sip.NewBodyPart("application/pidf+xml", []byte("s3"))}, f2)

	if r := recv(t, stale); !errors.Is(r.err, ua.ErrStale) {
		t.Errorf("replaced change: %+v", r)
	}

	esc.noRequest()
	esc.answer(initial, 200, "", sip.Field{Name: "SIP-ETag", Value: "e1"}, sip.Field{Name: "Expires", Value: "3600"})

	modify := esc.nextRequest("PUBLISH")
	if modify.Header.Get("SIP-If-Match") != "e1" || string(modify.Body) != "s3" {
		t.Fatalf("modification If-Match %q body %q", modify.Header.Get("SIP-If-Match"), modify.Body)
	}

	esc.answer(modify, 412, "")
	recv(t, done)

	again := esc.nextRequest("PUBLISH")
	if again.Header.Has("SIP-If-Match") || string(again.Body) != "s3" {
		t.Fatalf("after the 412: If-Match %q body %q, want an initial publication of the last state",
			again.Header.Get("SIP-If-Match"), again.Body)
	}
}

// RFC 3903 §6: the compositor refuses an unknown event package (489), a duration too brief (423), an unknown
// entity-tag (412), and an initial publication without state (400).
func TestPublicationStoreRefusals(t *testing.T) {
	esc := newCompositor(t, nil)
	alice := newRaw(t)

	for _, tc := range []struct {
		name string
		edit func(req *sip.Request)
		code int
	}{
		{"unknown event", func(req *sip.Request) { req.Header.Set("Event", "dialog") }, 489},
		{"too brief", func(req *sip.Request) { req.Header.Set("Expires", "10") }, 423},
		{"unknown entity-tag", func(req *sip.Request) { req.Header.Set("SIP-If-Match", "nope") }, 412},
		{"no state", func(req *sip.Request) { req.SetBodies() }, 400},
	} {
		req := siptest.NewRequest("PUBLISH", esc.p.uri(), sip.UDP, alice.sock.Addr())
		req.Header.Set("Event", "presence")
		req.Header.Set("Expires", "3600")
		req.SetBodies(sip.NewBodyPart("application/pidf+xml", []byte("open")))
		tc.edit(req)

		alice.sock.Send(sip.UDP, esc.p.addr, req)

		res := alice.response()
		if res.StatusCode != tc.code {
			t.Errorf("%s: %d, want %d", tc.name, res.StatusCode, tc.code)
		}

		if tc.code == 423 && res.Header.Get("Min-Expires") != "60" {
			t.Errorf("423 Min-Expires %q", res.Header.Get("Min-Expires"))
		}
	}
}

// RFC 3903 §6: a publication not refreshed in time expires, and the compositor reports the state without it.
func TestPublicationStoreExpiry(t *testing.T) {
	clock := netOf(t).Clock()
	esc := newCompositor(t, clock)
	alice := newRaw(t)

	req := siptest.NewRequest("PUBLISH", esc.p.uri(), sip.UDP, alice.sock.Addr())
	req.Header.Set("Event", "presence")
	req.Header.Set("Expires", "60")
	req.SetBodies(sip.NewBodyPart("application/pidf+xml", []byte("open")))
	alice.sock.Send(sip.UDP, esc.p.addr, req)

	if res := alice.response(); res.StatusCode != 200 || res.Header.Get("Expires") != "60" {
		t.Fatalf("PUBLISH %d Expires %q", res.StatusCode, res.Header.Get("Expires"))
	}

	if ch := esc.next(t); len(ch.state) != 1 {
		t.Fatalf("state %+v", ch.state)
	}

	advance(clock, 61*time.Second, esc.p)

	if ch := esc.next(t); len(ch.state) != 0 {
		t.Fatalf("state after the expiry %+v", ch.state)
	}

	if got := esc.store.State(req.URI, "presence"); len(got) != 0 {
		t.Errorf("stored %+v", got)
	}
}

// RFC 3903 §4.3, §4.5, TS 24.379 §9.2.1.2 step 2: the bodies of WithBodies go with each PUBLISH beside the state: the
// initial one with the state, the refresh and the removal without.
func TestPublicationWithBodies(t *testing.T) {
	clock := netOf(t).Clock()
	got := make(chan *sip.Request, 4)

	esc := newPeerWith(t, "esc", func(cfg *ua.Config) {
		cfg.Transaction.Clock = clock
		cfg.Allow = append(cfg.Allow, "PUBLISH")
		cfg.OnRequest = func(r *ua.Request) {
			got <- r.Message()

			var h sip.Header

			seq, _, _ := strings.Cut(r.Message().Header.Get("CSeq"), " ")
			h.Set("SIP-ETag", "e"+seq)
			h.Set("Expires", r.Message().Header.Get("Expires"))

			_ = r.Respond(ua.Reply{Code: 200, Header: h})
		}
	})
	alice := newPeer(t, "alice", clock, ua.SessionTimer{})

	info := sip.NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte("info"))

	p, err := alice.a.Publish(publishRequest(esc, alice.uri(), "open", "120"), ua.PublicationHandler{}, ua.WithBodies(info))
	if err != nil {
		t.Fatal(err)
	}

	bodies := func(req *sip.Request) []string {
		parts, err := req.Content().Parts()
		if err != nil {
			t.Fatal(err)
		}

		var out []string
		for _, b := range parts {
			out = append(out, b.Header.Get("Content-Type")+"="+string(b.Body))
		}

		return out
	}

	if b := bodies(<-got); len(b) != 2 || b[0] != "application/pidf+xml=open" || b[1] != "application/vnd.3gpp.mcptt-info+xml=info" {
		t.Fatalf("initial PUBLISH bodies %v", b)
	}

	advance(clock, 61*time.Second, alice, esc)

	if b := bodies(<-got); len(b) != 1 || b[0] != "application/vnd.3gpp.mcptt-info+xml=info" {
		t.Fatalf("refresh bodies %v", b)
	}

	done, f := results()
	if err := p.Remove(f); err != nil {
		t.Fatal(err)
	}

	recv(t, done)

	req := <-got
	if b := bodies(req); req.Header.Get("Expires") != "0" || len(b) != 1 || b[0] != "application/vnd.3gpp.mcptt-info+xml=info" {
		t.Fatalf("removal Expires %q bodies %v", req.Header.Get("Expires"), b)
	}
}
