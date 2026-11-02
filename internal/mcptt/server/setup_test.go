package server_test

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/participating"
	"github.com/ellanetworks/ims/internal/mcptt/server"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

func refuse(w *world, name string) {
	server.InterposeTerminate(w.s, func(req *sip.Request, t participating.Termination) participating.Termination {
		if strings.Contains(req.Header.Get("To"), name+"@") {
			return participating.Termination{Code: 404, Header: sip.Header{identity.Warning(domain, identity.WarnUserUnknown)}}
		}

		return t
	})
}

func (w *world) failed(u *user, group string) *sip.Response {
	t := w.t
	t.Helper()

	if _, err := u.Call(context.Background(), mustURI(t, group)); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	res := event(u.expect(t, "failed"), client.EventFailed).Response
	if res == nil {
		t.Fatalf("%s: no final response", u.name)
	}

	return res
}

func (w *world) established(u *user, group string) (*client.Call, *sip.Response) {
	t := w.t
	t.Helper()

	c, err := u.Call(context.Background(), mustURI(t, group))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	return c, event(u.expect(t, "established"), client.EventEstablished).Response
}

func (w *world) request(method, target string, header ...string) int {
	p := w.n.Peer()
	req := siptest.NewRequest(method, target, sip.UDP, p.Addr())
	req.Header.Set("From", "<sip:alice@ims.example.com>;tag="+sip.NewTag())
	req.Header.Set("To", "<"+target+">")
	req.Header.Set("Call-ID", sip.NewTag()+"@peer")
	req.Header.Set("CSeq", "1 "+method)
	req.Header.Set("Contact", "<sip:peer@"+p.Addr().String()+">")

	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}

	p.Send(sip.UDP, w.addr, req)

	for {
		res, _ := p.RecvResponse()
		if res.StatusCode != 100 {
			return res.StatusCode
		}
	}
}

// TS 24.379 clause 10.1.1.4.2
func TestSetupRefusedAtOnce(t *testing.T) {
	w := newWorld(t, nil)
	refuse(w, "bob")

	ca, res := w.established(w.users["alice"], fire)
	if res.StatusCode != 200 {
		t.Fatalf("alice's response %v", res)
	}

	w.users["carol"].expect(t, "incoming", "established")

	ca.Leave(context.Background())
	w.n.Quiesce()
}

// TS 24.379 clause 10.1.1.4.2
// TS 24.379 clause 10.1.1.3.1.1
// RFC 3261 section 16.7
func TestSetupNoneAccepts(t *testing.T) {
	w := newWorld(t, nil)
	refuse(w, "bob")
	w.users["carol"].onInvite = func(r *ua.Request) ua.Handler {
		_ = r.Reject(486)
		return ua.Handler{}
	}

	if res := w.failed(w.users["alice"], fire); res.StatusCode != 404 || !warned(res, identity.WarnUserUnknown) {
		t.Fatalf("alice's response %d %v", res.StatusCode, res.Header.Values("Warning"))
	}
}

// TS 24.379 clause 6.3.3.3
// TS 24.379 clause 6.3.8.1
func TestSetupLeave(t *testing.T) {
	w := newWorld(t, nil)
	alice, bob := w.users["alice"], w.users["bob"]
	silent(w.users["carol"])

	ca, err := alice.Call(context.Background(), mustURI(t, rescue))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Advance(time.Second)
	event(bob.expect(t, "incoming", "established"), client.EventIncoming).Call.Leave(context.Background())
	w.n.Advance(time.Second)

	alice.expectNot(t, "failed", "established")

	if got := w.trace.take(); slices.Contains(got, "carol>server 487 INVITE") {
		t.Fatalf("carol cancelled:\n%s", strings.Join(got, "\n"))
	}

	w.n.Advance(5 * time.Second)
	alice.expectNot(t, "failed", "established")

	cb, err := bob.Call(context.Background(), mustURI(t, rescue))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	if res := event(bob.expect(t, "established"), client.EventEstablished).Response; !warned(res, identity.WarnSessionExists) {
		t.Errorf("bob's 200 warnings %v", res.Header.Values("Warning"))
	}

	if res := event(alice.expect(t, "established"), client.EventEstablished).Response; !warned(res, identity.WarnProceededWithoutRequired) {
		t.Errorf("alice's 200 warnings %v", res.Header.Values("Warning"))
	}

	cb.Leave(context.Background())
	ca.Leave(context.Background())
	w.n.Quiesce()
}

// TS 24.379 clause 6.3.3.3
func TestRequiredRefusedAtOnce(t *testing.T) {
	w := newWorld(t, nil)
	refuse(w, "carol")

	res := w.failed(w.users["alice"], hazmat)
	if res.StatusCode != 404 || !warned(res, identity.WarnUserUnknown) || !warned(res, identity.WarnAbandonedRequired) {
		t.Fatalf("alice's response %d %v", res.StatusCode, res.Header.Values("Warning"))
	}

	w.users["bob"].expectNot(t, "incoming")
}

// TS 24.379 clause 6.3.3.3
// TS 24.379 clause 6.3.3.1.5
func TestRequiredUnusableAnswer(t *testing.T) {
	w := newWorld(t, nil)
	w.users["carol"].onInvite = func(r *ua.Request) ua.Handler {
		offer, _ := r.Offer()
		answer := regexp.MustCompile(`m=application \d+`).ReplaceAll(offer, []byte("m=application 0"))
		_ = r.Respond(ua.Reply{Code: 200, Bodies: []sip.BodyPart{ua.SDP(answer)}})

		return ua.Handler{}
	}

	if res := w.failed(w.users["alice"], hazmat); res.StatusCode != 488 || !warned(res, identity.WarnAbandonedRequired) {
		t.Fatalf("alice's response %d %v", res.StatusCode, res.Header.Values("Warning"))
	}
}

// TS 24.379 clause 6.3.3.3
func TestRequiredAccepts(t *testing.T) {
	w := newWorld(t, nil)

	ca, _, _ := w.call(rescue)
	if res := event(w.last, client.EventEstablished).Response; len(res.Header.Values("Warning")) != 0 {
		t.Errorf("200 warnings %v", res.Header.Values("Warning"))
	}

	ca.Leave(context.Background())
	w.n.Quiesce()
}

// TS 24.379 clause 6.3.5.5
// TS 24.379 clause 10.1.1.4.2
func TestMaxParticipants(t *testing.T) {
	w := newWorld(t, nil)

	ca, res := w.established(w.users["alice"], small)
	if !warned(res, identity.WarnTooManyParticipants) {
		t.Errorf("200 warnings %v", res.Header.Values("Warning"))
	}

	w.users["bob"].expect(t, "incoming", "established")
	w.users["carol"].expectNot(t, "incoming")

	if res := w.failed(w.users["carol"], small); res.StatusCode != 486 || !warned(res, identity.WarnTooManyParticipants) {
		t.Errorf("carol's response %d %v", res.StatusCode, res.Header.Values("Warning"))
	}

	ca.Leave(context.Background())
	w.n.Quiesce()
}

// TS 24.379 clause 10.1.1.4.2
func TestAffiliationsRequired(t *testing.T) {
	w := newWorld(t, nil)

	for _, g := range []string{quorum, escort} {
		if res := w.failed(w.users["alice"], g); res.StatusCode != 480 || !warned(res, identity.WarnAbandonedRequired) {
			t.Errorf("%s: alice's response %d %v", g, res.StatusCode, res.Header.Values("Warning"))
		}
	}

	w.users["bob"].expectNot(t, "incoming")
}

// TS 24.379 clause 6.3.3.3
// TS 24.379 clause 10.1.1.4.2
func TestMinimumToStart(t *testing.T) {
	w := newWorld(t, nil)

	ca, cb, _ := w.call(two)

	got := w.trace.take()
	ok := slices.Index(got, "server>alice 200 INVITE")

	if ok < slices.Index(got, "bob>server 200 INVITE") || ok < slices.Index(got, "carol>server 200 INVITE") {
		t.Errorf("alice answered before both:\n%s", strings.Join(got, "\n"))
	}

	ca.Leave(context.Background())
	cb.Leave(context.Background())
	w.n.Quiesce()
	w.n.Advance(time.Second)

	for _, n := range []string{"alice", "bob", "carol"} {
		w.users[n].take()
	}

	w.users["carol"].onInvite = func(r *ua.Request) ua.Handler {
		_ = r.Reject(486)
		return ua.Handler{}
	}

	if res := w.failed(w.users["alice"], two); res.StatusCode != 486 {
		t.Errorf("alice's response %d", res.StatusCode)
	}

	w.users["bob"].expect(t, "incoming", "established", "ended")
}

// TS 24.379 clause 10.1.1.4.4.2
func TestOriginatorCancels(t *testing.T) {
	w := newWorld(t, nil)
	silent(w.users["carol"])

	ca, err := w.users["alice"].Call(context.Background(), mustURI(t, rescue))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Advance(time.Second)
	ca.Leave(context.Background())
	w.n.Advance(time.Second)

	if got := w.trace.take(); !slices.Contains(got, "carol>server 487 INVITE") || !slices.Contains(got, "server>bob BYE") {
		t.Errorf("no CANCEL to carol, or BYE to bob:\n%s", strings.Join(got, "\n"))
	}

	w.users["bob"].expect(t, "incoming", "established", "ended")
}

// TS 24.379 clause 10.1.1.4.2
func TestJoinNotMember(t *testing.T) {
	w := newWorld(t, nil)

	ca, _, _ := w.call(fire)

	w.user("dave2", 6, func(_ string, c *client.Config) {
		c.MCPTTID = mustURI(t, "sip:dave@mcptt.example.com")
		c.IMPU = mustURI(t, "sip:dave@ims.example.com")
	})
	dave := w.users["dave2"]

	if err := dave.Affiliate(context.Background(), []sip.URI{mustURI(t, fire)}, func(*sip.Response, error) {}); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	cfg := callConfiguration()
	cfg.Groups[0].List.Entries = cfg.Groups[0].List.Entries[:3]

	snap, err := settings.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	w.src.p.Store(snap)

	if res := w.failed(dave, fire); res.StatusCode != 403 || !warned(res, identity.WarnNotAuthorisedToJoin) {
		t.Errorf("dave's response %d %v", res.StatusCode, res.Header.Values("Warning"))
	}

	ca.Leave(context.Background())
	w.n.Quiesce()
}

// TS 24.379 clause 10.1.1.3.1.1
func TestN6(t *testing.T) {
	w := newWorld(t, nil)

	ca, _, cc := w.call(fire)

	w.user("carol2", 6, func(_ string, c *client.Config) {
		c.MCPTTID = mustURI(t, "sip:carol@mcptt.example.com")
		c.IMPU = mustURI(t, "sip:carol@ims.example.com")
	})
	carol := w.users["carol2"]

	if res := w.failed(carol, police); res.StatusCode != 486 || !warned(res, identity.WarnTooManyCalls) {
		t.Errorf("police: carol's response %d %v", res.StatusCode, res.Header.Values("Warning"))
	}

	c2, res := w.established(carol, fire)
	if !warned(res, identity.WarnSessionExists) {
		t.Errorf("fire: carol's 200 warnings %v", res.Header.Values("Warning"))
	}

	c2.Leave(context.Background())
	cc.Leave(context.Background())
	ca.Leave(context.Background())
	w.n.Quiesce()
}

// TS 24.379 clause 6.3.2.2.8.1
// TS 24.379 clause 10.1.1.4.5.1
// TS 24.379 clause 6.3.7.1
func TestReleasedSession(t *testing.T) {
	w := newWorld(t, nil)
	bob := w.users["bob"]

	var bye *sip.Request

	bob.onInvite = func(r *ua.Request) ua.Handler {
		h := bob.OnInvite(r)
		inner := h.Bye
		h.Bye = func(_ context.Context, s *ua.Session, br *ua.Request) {
			bye = br.Message()

			if inner != nil {
				inner(context.Background(), s, br)
			} else {
				_ = br.Respond(ua.Reply{Code: 200})
			}
		}

		return h
	}

	ca, _, _ := w.call(police)

	cs, _ := event(w.last, client.EventEstablished).Response.Header.Contacts()
	if len(cs) != 1 {
		t.Fatal("no session identity")
	}

	ca.Leave(context.Background())
	w.n.Quiesce()

	if bye == nil || bye.Header.Get("P-Asserted-Identity") != "<"+psi+">" {
		t.Errorf("BYE to bob %v", bye)
	}

	session := cs[0].URI.String()

	if code := w.request("INVITE", session); code != 404 {
		t.Errorf("INVITE to the released session identity: %d", code)
	}

	if code := w.request("SUBSCRIBE", session, "Event", "conference", "Expires", "600"); code != 404 {
		t.Errorf("SUBSCRIBE to the released session identity: %d", code)
	}
}
