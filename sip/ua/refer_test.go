package ua_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// referee is an agent accepting REFERs with accept, its subscriptions recorded.
type referee struct {
	p     *peer
	subs  chan *ua.ServerSubscription
	ended chan ua.End
}

func newReferee(t *testing.T, accept func(r *ua.Request) ua.ReferAcceptance) *referee {
	rf := &referee{subs: make(chan *ua.ServerSubscription, 4), ended: make(chan ua.End, 4)}

	rf.p = newPeerWith(t, "bob", func(c *ua.Config) {
		c.Supported = []string{sip.OptionTagNoReferSub, sip.OptionTagExplicitSub}
		c.OnRequest = func(r *ua.Request) {
			o := accept(r)
			o.Handler.Ended = func(_ *ua.ServerSubscription, e ua.End) { rf.ended <- e }

			s, err := r.AcceptRefer(o)
			if err != nil {
				t.Errorf("AcceptRefer: %v", err)
			}

			rf.subs <- s
		}
	})

	return rf
}

func (s *subscriberOf) refer(t *testing.T, to *peer, header ...sip.Field) *ua.Subscription {
	t.Helper()

	uri, _ := sip.ParseURI(to.uri())
	req := sip.NewRequest("REFER", uri)
	req.Header.Add("From", "<"+s.p.uri()+">")
	req.Header.Add("To", "<"+to.uri()+">")
	req.Header.Add("Refer-To", "<sip:carol@example.com>")

	for _, f := range header {
		req.Header.Add(f.Name, f.Value)
	}

	sub, err := s.p.a.Refer(req, ua.SubscriptionHandler{
		Notify: func(_ *ua.Subscription, r *ua.Request) { s.notifys <- r },
		Ended:  func(_ *ua.Subscription, e ua.End) { s.ended <- e },
	})
	if err != nil {
		t.Fatal(err)
	}

	return sub
}

// RFC 3515 §2.4.4–§2.4.7: a REFER outside a dialog creates an implicit subscription to its refer event, whose
// NOTIFYs report the referred request in sipfrag: 100 Trying at once, then its progress, its final status
// terminating the subscription with reason noresource.
func TestRefer(t *testing.T) {
	rf := newReferee(t, func(*ua.Request) ua.ReferAcceptance { return ua.ReferAcceptance{} })
	sb := newSubscriberOf(t, nil)

	sb.refer(t, rf.p)
	s := <-rf.subs

	if ev := s.Event(); ev.Type() != "refer" || ev.ID == "" {
		t.Errorf("event %s, want refer with the id of the REFER's CSeq", ev)
	}

	n := nextNotify(t, sb.notifys)
	checkNotify(t, n, "active;expires=240", "SIP/2.0 100 Trying\r\n")

	if n.Header.Get("Content-Type") != "message/sipfrag" {
		t.Errorf("Content-Type %q", n.Header.Get("Content-Type"))
	}

	if err := s.ReferStatus(180, ""); err != nil {
		t.Fatal(err)
	}

	checkNotify(t, nextNotify(t, sb.notifys), "active", "SIP/2.0 180 Ringing\r\n")

	if err := s.ReferStatus(200, ""); err != nil {
		t.Fatal(err)
	}

	checkNotify(t, nextNotify(t, sb.notifys), "terminated;reason=noresource", "SIP/2.0 200 OK\r\n")

	if e := nextEnd(t, sb.ended); e.Cause != ua.EndTerminated {
		t.Errorf("referrer ended %+v", e)
	}

	if e := nextEnd(t, rf.ended); e.Cause != ua.EndTerminated {
		t.Errorf("referee ended %+v", e)
	}
}

// RFC 4488: a REFER with Refer-Sub: false, accepted without a subscription, gets a 200 with Refer-Sub: false; RFC 7614:
// one requiring explicitsub gets the Refer-Events-At of its state, and creates none. RFC 7647 §5: never a 202.
func TestReferWithoutSubscription(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header []sip.Field
		accept ua.ReferAcceptance
		check  func(res *sip.Response) bool
	}{
		{
			name:   "norefersub",
			header: []sip.Field{{Name: "Refer-Sub", Value: "false"}, {Name: "Require", Value: "norefersub"}},
			accept: ua.ReferAcceptance{NoSubscription: true},
			check:  func(res *sip.Response) bool { return res.Header.Get("Refer-Sub") == "false" },
		},
		{
			name:   "explicitsub",
			header: []sip.Field{{Name: "Require", Value: "explicitsub"}},
			accept: ua.ReferAcceptance{EventsAt: "sip:refer-1@bob.example.com;gr"},
			check: func(res *sip.Response) bool {
				return strings.Contains(res.Header.Get("Refer-Events-At"), "refer-1@bob.example.com")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rf := newReferee(t, func(*ua.Request) ua.ReferAcceptance { return tc.accept })

			accepted := make(chan *sip.Response, 1)
			ended := make(chan ua.End, 1)

			uri, _ := sip.ParseURI(rf.p.uri())
			req := sip.NewRequest("REFER", uri)
			req.Header.Add("From", "<sip:alice@example.com>")
			req.Header.Add("To", "<"+rf.p.uri()+">")
			req.Header.Add("Refer-To", "<sip:carol@example.com>")

			for _, f := range tc.header {
				req.Header.Add(f.Name, f.Value)
			}

			alice := newPeer(t, "alice", nil, ua.SessionTimer{})

			if _, err := alice.a.Refer(req, ua.SubscriptionHandler{
				Accepted: func(_ *ua.Subscription, res *sip.Response) { accepted <- res },
				Ended:    func(_ *ua.Subscription, e ua.End) { ended <- e },
			}); err != nil {
				t.Fatal(err)
			}

			if s := <-rf.subs; s != nil {
				t.Fatal("a subscription")
			}

			if res := <-accepted; res.StatusCode != 200 || !tc.check(res) {
				t.Fatalf("REFER %d %v", res.StatusCode, res.Header)
			}

			if e := <-ended; e.Cause != ua.EndNoSubscription {
				t.Errorf("ended %+v", e)
			}
		})
	}
}

// A REFER within the dialog of a session creates its implicit subscription in that dialog, with the id of its CSeq
// (RFC 3515 §2.4.6); a BYE ends the INVITE usage only, the subscription going on until its final NOTIFY (RFC 5057).
func TestReferInTheSessionDialog(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})

	refers := make(chan *ua.ServerSubscription, 1)
	sent := make(chan string, 16)

	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		prepare := c.Prepare
		c.Prepare = func(req *sip.Request) error {
			sent <- req.Method + " " + req.Header.Get("Subscription-State")
			return prepare(req)
		}

		c.Allow = append(slices.Clone(ua.Methods), "REFER")
		c.OnInvite = func(r *ua.Request) ua.Handler {
			_ = r.Accept(sdpOf("answer-1")...)

			return ua.Handler{Request: func(_ *ua.Session, r *ua.Request) {
				s, err := r.AcceptRefer(ua.ReferAcceptance{})
				if err != nil {
					t.Errorf("AcceptRefer: %v", err)
				}

				refers <- s
			}}
		}
	})

	alice.invite(bob, "offer-1")

	as := alice.next("established").s
	alice.next("answer")

	done, f := results()

	if err := as.Send("REFER", ua.Change{Header: sip.Header{{Name: "Refer-To", Value: "<sip:carol@example.com>"}}}, f); err != nil {
		t.Fatal(err)
	}

	s := <-refers

	if res := recv(t, done).res; res.StatusCode != 200 {
		t.Fatalf("REFER %d", res.StatusCode)
	}

	// The NOTIFYs come in the session's dialog, to alice's Handler.Request.
	n := alice.next("request").req
	if ev := n.Message().Header.Get("Event"); !strings.HasPrefix(ev, "refer;id=") {
		t.Fatalf("Event %q", ev)
	}

	_ = n.Respond(ua.Reply{Code: 200})

	if err := as.Bye(ua.Change{}, nil); err != nil {
		t.Fatal(err)
	}

	alice.next("ended")

	for len(sent) > 0 {
		<-sent
	}

	if err := s.ReferStatus(200, ""); err != nil {
		t.Fatal(err)
	}

	if got := <-sent; got != "NOTIFY terminated;reason=noresource" {
		t.Errorf("after the BYE, bob sent %q, want the final NOTIFY in the dialog", got)
	}
}
