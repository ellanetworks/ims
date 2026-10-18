package ua_test

import (
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// serverPeer is a peer with the zero Policy, the one of servers: nothing automatic beyond what the RFCs require.
func serverPeer(t *testing.T, name string) *peer {
	return newPeerWith(t, name, func(c *ua.Config) { c.Policy = ua.Policy{} })
}

func realCall(t *testing.T, alice, bob *peer) (as *ua.Session, offer, answer string) {
	t.Helper()

	offer, answer = realSDP(4000, 1), realSDP(5000, 1)

	alice.invite(bob, offer)
	_ = bob.next("invite").req.Accept(ua.SDP([]byte(answer)))

	as = alice.next("established").s
	alice.next("answer")

	bob.next("established")

	return as, offer, answer
}

// With the zero Policy, the session refreshes of the peer reach the application, which may answer them unchanged
// (RFC 3264 §8, RFC 4028 §7.4): an MCPTT server sees the re-INVITE that keeps the session but carries an emergency
// upgrade in its other parts (TS 24.379 §6.3.3.1.6).
func TestServerPolicyDeliversRefreshes(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), serverPeer(t, "bob")
	as, offer, answer := realCall(t, alice, bob)

	done, f := results()

	if err := as.Reinvite(ua.Change{Bodies: sdpOf(offer)}, f); err != nil {
		t.Fatal(err)
	}

	r := bob.next("request").req
	if !r.Refresh() || r.Message().Method != "INVITE" {
		t.Fatalf("%s refresh %v", r.Message().Method, r.Refresh())
	}

	if err := r.AnswerUnchanged(ua.Change{Header: sip.Header{{Name: "X-Seen", Value: "yes"}}}); err != nil {
		t.Fatal(err)
	}

	res := recv(t, done).res
	if res.StatusCode != 200 || string(sessionBody(res)) != answer || res.Header.Get("X-Seen") != "yes" {
		t.Fatalf("re-INVITE %d %q %q", res.StatusCode, sessionBody(res), res.Header.Get("X-Seen"))
	}

	alice.next("answer")

	// An UPDATE without an offer refreshes too; a change of the session is no refresh.
	if err := as.Update(ua.Change{}, f); err != nil {
		t.Fatal(err)
	}

	u := bob.next("request").req
	if !u.Refresh() {
		t.Fatal("UPDATE without an offer: no refresh")
	}

	_ = u.AnswerUnchanged(ua.Change{})

	if res := recv(t, done).res; res.StatusCode != 200 || sessionBody(res) != nil {
		t.Fatalf("UPDATE %d", res.StatusCode)
	}

	if err := as.Reinvite(ua.Change{Bodies: sdpOf(realSDP(4000, 2))}, f); err != nil {
		t.Fatal(err)
	}

	c := bob.next("request").req
	if c.Refresh() {
		t.Fatal("a new version of the session is a refresh")
	}

	if err := c.AnswerUnchanged(ua.Change{}); err == nil {
		t.Fatal("AnswerUnchanged of a change of the session")
	}

	_ = c.Reject(488)

	if res := recv(t, done).res; res.StatusCode != 488 {
		t.Fatalf("re-INVITE %d", res.StatusCode)
	}
}

// ClientPolicy answers the refreshes, unless they carry other bodies than SDP: those reach the application.
func TestClientPolicyLeavesRefreshesWithOtherBodies(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	as, offer, _ := realCall(t, alice, bob)

	done, f := results()

	info := sip.NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte("<mcpttinfo/>"))

	if err := as.Reinvite(ua.Change{Bodies: []sip.BodyPart{ua.SDP([]byte(offer)), info}}, f); err != nil {
		t.Fatal(err)
	}

	r := bob.next("request").req
	if !r.Refresh() {
		t.Fatal("no refresh")
	}

	_ = r.AnswerUnchanged(ua.Change{})

	if res := recv(t, done).res; res.StatusCode != 200 {
		t.Fatalf("re-INVITE %d", res.StatusCode)
	}

	// With the SDP alone, the agent answers.
	if err := as.Reinvite(ua.Change{Bodies: sdpOf(offer)}, f); err != nil {
		t.Fatal(err)
	}

	if res := recv(t, done).res; res.StatusCode != 200 {
		t.Fatalf("re-INVITE %d", res.StatusCode)
	}

	bob.none()
}

// Without Handler.Request, the agent answers the refreshes whatever its Policy, and refuses changes with a 488.
func TestNoRequestHandler(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.Policy = ua.Policy{}
		c.OnInvite = func(r *ua.Request) ua.Handler {
			_ = r.Accept(ua.SDP([]byte(realSDP(5000, 1))))
			return ua.Handler{}
		}
	})

	alice.invite(bob, realSDP(4000, 1))

	as := alice.next("established").s
	alice.next("answer")

	done, f := results()

	if err := as.Update(ua.Change{}, f); err != nil {
		t.Fatal(err)
	}

	if res := recv(t, done).res; res.StatusCode != 200 {
		t.Fatalf("refresh %d", res.StatusCode)
	}

	if err := as.Reinvite(ua.Change{Bodies: sdpOf(realSDP(4000, 2))}, f); err != nil {
		t.Fatal(err)
	}

	if res := recv(t, done).res; res.StatusCode != 488 {
		t.Fatalf("change %d, want 488", res.StatusCode)
	}
}

// RFC 4028 §10: a refresher that does not refresh (Policy.SkipRefreshes, a fault) sees the other side end the session
// with a BYE before the interval ends.
func TestSkipRefreshes(t *testing.T) {
	clock := netOf(t).Clock()
	alice := newPeer(t, "alice", clock, ua.SessionTimer{Interval: 120 * time.Second})
	bob := newPeer(t, "bob", clock, ua.SessionTimer{Interval: 120 * time.Second, Refresher: sip.RefresherUAC})

	uri, _ := sip.ParseURI(bob.uri())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+bob.uri()+">")
	req.SetBodies(sdpOf("offer-1")...)

	skip := ua.ClientPolicy
	skip.SkipRefreshes = true

	if _, err := alice.a.Invite(req, alice.handler(), ua.WithPolicy(skip)); err != nil {
		t.Fatal(err)
	}

	_ = bob.next("invite").req.Accept(sdpOf("answer-1")...)

	as := alice.next("established").s
	alice.next("answer")
	bob.next("established")

	if iv, refresher := as.SessionTimer(); iv != 120*time.Second || !refresher {
		t.Fatalf("alice timer %v refresher %v", iv, refresher)
	}

	// Bob ends the session min(32 s, 40 s) before the 120 s, at 88 s.
	advance(clock, 87*time.Second, alice, bob)
	bob.none()

	advance(clock, 2*time.Second, alice, bob)

	if e := bob.next("ended"); e.end.Cause != ua.EndExpired {
		t.Errorf("bob ended %+v", e.end)
	}

	if e := alice.next("ended"); e.end.Cause != ua.EndBye || !e.end.Remote {
		t.Errorf("alice ended %+v", e.end)
	}
}
