package b2bua_test

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/ua"
)

// recorder is a Media anchoring nothing that records the life of the exchanges.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (m *recorder) log(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.events = append(m.events, fmt.Sprintf(format, args...))
}

func (m *recorder) Offer(x *b2bua.Exchange) ([]byte, error) {
	m.log("offer %s %s", x.Method, x.Offer)
	return x.Offer, nil
}

func (m *recorder) Answer(_ *b2bua.Exchange, _ *ua.Session, sdp []byte) ([]byte, error) {
	m.log("answer %s", sdp)
	return sdp, nil
}

func (m *recorder) End(x *b2bua.Exchange, s *ua.Session) {
	m.log("end %s %s %v", x.Method, x.Offer, s != nil)
}

func (m *recorder) take() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := m.events
	m.events = nil

	return out
}

func checkEvents(t *testing.T, m *recorder, want ...string) {
	t.Helper()

	if got := m.take(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("media events %q, want %q", got, want)
	}
}

// Each exchange of a call goes to the Media, and ends once: in the dialog it took effect in, or in none.
func TestMediaExchanges(t *testing.T) {
	m := &recorder{}
	alice, bob, b := newCall(t, b2bua.RelayConfig{Media: m})

	alice.invite(b.uri(), "offer-1", alice.handler())
	as, _ := bridged(t, alice, bob)

	checkEvents(t, m, "offer INVITE offer-1", "answer answer-1", "end INVITE offer-1 true")

	// A re-INVITE answered, then one refused.
	_ = as.Reinvite(ua.Change{Bodies: sdpOf("offer-2")}, nil)
	_ = bob.next("request").req.Respond(ua.Reply{Code: 200, Bodies: sdpOf("answer-2")})
	alice.next("answer")

	checkEvents(t, m, "offer INVITE offer-2", "answer answer-2", "end INVITE offer-2 true")

	_ = as.Reinvite(ua.Change{Bodies: sdpOf("offer-3")}, nil)
	_ = bob.next("request").req.Reject(488)

	b.net.Quiesce()
	checkEvents(t, m, "offer INVITE offer-3", "end INVITE offer-3 false")

	// A failed call ends its exchange without effect.
	alice.invite(b.uri(), "offer-9", alice.handler())

	_ = bob.next("invite").req.Reject(486)
	alice.next("failed")

	checkEvents(t, m, "offer INVITE offer-9", "end INVITE offer-9 false")
}

// RFC 3261 §16.6: past Timer C without a final response, the outgoing INVITE is cancelled and the incoming one gets a
// 408.
func TestTimerC(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{TimerC: 30 * time.Second})
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	in := bob.next("invite").req
	_ = in.Progress(ua.Reply{Code: 180})

	alice.next("progress")

	n.Advance(20 * time.Second)

	_ = in.Progress(ua.Reply{Code: 183})

	alice.next("progress")

	// Timer C restarts with each provisional response.
	n.Advance(20 * time.Second)
	alice.none()

	n.Advance(11 * time.Second)

	if e := alice.next("failed"); e.res == nil || e.res.StatusCode != 408 {
		t.Fatalf("alice's INVITE %+v", e.res)
	}

	if e := bob.next("ended"); e.end.Cause != ua.EndCancelled {
		t.Fatalf("bob ended %+v", e.end)
	}
}

// RFC 3891 §3: a Replaces naming a dialog of the B2BUA names, on the outgoing leg, the dialog paired with it, as its
// recipient knows it.
func TestReplacesMapped(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", func(c *ua.Config) { c.Extensions.Replaces = true })
	b := newB2BWith(t, n, func() *party { return bob }, b2bua.RelayConfig{}, func(c *ua.Config) { c.Extensions.Replaces = true })
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())
	as, bs := bridged(t, alice, bob)

	// Carol replaces alice: her INVITE names the dialog of alice and the B2BUA, as the B2BUA knows it.
	carol := newParty(t, n, "carol", nil)
	id := as.ID()
	rep := sip.Replaces{CallID: id.CallID, ToTag: id.RemoteTag, FromTag: id.LocalTag}

	carol.invite(b.uri(), "offer-c", carol.handler(), sip.Field{Name: "Replaces", Value: rep.String()})

	in := bob.next("invite").req

	replaced, code, ok := in.Replaces()
	if !ok || replaced != bs {
		t.Fatalf("bob's INVITE replaces %v (code %d, ok %v), want his session with the B2BUA; Replaces %q",
			replaced, code, ok, in.Message().Header.Get("Replaces"))
	}
}

// RFC 3725 §4.2: Join makes the sessions of two legs agree: the offer of one, fetched with a re-INVITE without one,
// reaches the other, whose answer comes back in the ACK.
func TestJoin(t *testing.T) {
	n := newNet(t)
	alice, bob := newParty(t, n, "alice", nil), newParty(t, n, "bob", nil)

	var (
		call *b2bua.Call
		la   *b2bua.Leg
		st   *ua.Strand
	)

	server := newParty(t, n, "server", func(cfg *ua.Config) { cfg.Policy = ua.Policy{} })
	server.mu.Lock()
	server.onInvite = func(r *ua.Request) ua.Handler {
		st = r.Strand()
		call = b2bua.NewCall(server.a, st, b2bua.CallHandler{})
		la, _ = call.Incoming(r, b2bua.LegHandler{})
		_ = r.Accept(sdpOf("server-a")...)

		return ua.Handler{}
	}
	server.mu.Unlock()

	alice.invite(server.uri(), "offer-a", alice.handler())
	alice.next("established")
	alice.next("answer")

	var lb *b2bua.Leg

	n.Quiesce()

	st.Post(func() {
		uri, _ := sip.ParseURI(bob.uri())
		out := sip.NewRequest("INVITE", uri)
		out.Header.Add("From", "<"+server.uri()+">")
		out.Header.Add("To", "<"+bob.uri()+">")
		out.SetBodies(sdpOf("server-b")...)

		lb, _ = call.Dial(out, b2bua.LegHandler{})
	})

	_ = bob.next("invite").req.Accept(sdpOf("answer-b")...)
	bob.next("established")

	joined := make(chan error, 1)

	n.Quiesce()

	st.Post(func() {
		if _, err := b2bua.Join(la, lb, b2bua.RelayConfig{}, func(err error) { joined <- err }); err != nil {
			joined <- err
		}
	})

	// Alice, ClientPolicy, answers the re-INVITE without an offer herself, with her session.
	r := bob.next("request").req
	if offer, _ := r.Offer(); string(offer) != "offer-a" {
		t.Fatalf("bob got offer %q, want alice's", offer)
	}

	_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf("answer-b2")})

	if e := alice.next("answer"); string(e.sdp) != "answer-b2" {
		t.Fatalf("alice got answer %q, want bob's", e.sdp)
	}

	n.Quiesce()

	select {
	case err := <-joined:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("Join did not complete")
	}
}

// RFC 3264 §8: a 2xx of the callee to a relayed re-INVITE that crossed its CANCEL is undone: the callee is offered
// again the session of the caller.
func TestCancelledReinviteUndone(t *testing.T) {
	n := newNet(t)
	bob := n.Peer()
	b := newB2B(t, n, func() *party { return &party{name: "bob", addr: bob.Addr()} }, b2bua.RelayConfig{})
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	invite, _ := bob.RecvRequest()
	tag := sip.NewTag()

	respond := func(req *sip.Request, code int, sdp string) {
		res := sip.NewResponse(req, code, "")
		_ = res.Header.SetToTag(tag)
		res.Header.Set("Contact", "<sip:bob@"+bob.Addr().String()+">")

		if sdp != "" {
			res.SetBodies(sdpOf(sdp)...)
		}

		bob.Send(sip.UDP, req.Flow.Remote, res)
	}

	respond(invite, 200, "answer-1")

	as := alice.next("established").s
	alice.next("answer")

	if ack, _ := bob.RecvRequest(); ack.Method != "ACK" {
		t.Fatalf("bob got %s", ack.Method)
	}

	_ = as.Reinvite(ua.Change{Bodies: sdpOf("offer-2")}, nil)

	reinvite, _ := bob.RecvRequest()
	if !hasSDP(reinvite, "offer-2") {
		t.Fatalf("bob's re-INVITE %q", reinvite.Body)
	}

	// A CANCEL waits for a provisional response (RFC 3261 §9.1): bob's 100, and the B2BUA's to alice.
	respond(reinvite, 100, "")
	n.Advance(time.Second)

	_ = as.CancelReinvite(ua.Change{})

	cancel, _ := bob.RecvRequest()
	if cancel.Method != "CANCEL" {
		t.Fatalf("bob got %s", cancel.Method)
	}

	// The 2xx crosses the CANCEL.
	respond(cancel, 200, "")
	respond(reinvite, 200, "answer-2")

	for {
		req, _ := bob.RecvRequest()
		if req.Method == "ACK" {
			continue
		}

		if req.Method != "INVITE" || !hasSDP(req, "offer-1") {
			t.Fatalf("bob got %s %q, want the session of alice offered again", req.Method, req.Body)
		}

		break
	}
}

// RFC 3261 §15: the callee ending before the caller's ACK of a 2xx sent in a fork ends that dialog, with a BYE once
// the ACK comes.
func TestEndBeforeTheAckOfAFork(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{})

	alice := callRaw(t, n, b.party, "offer-1")

	first := bob.next("invite").req
	fork, _ := first.Fork(bob.handler())
	_ = first.Progress(ua.Reply{Code: 180})
	_ = fork.Progress(ua.Reply{Code: 180})

	alice.response(180)
	alice.response(180)

	_ = fork.Accept(sdpOf("answer-f")...)

	ok := alice.response(200)

	got := bob.nextAll("ended", "established")
	_ = got["established"].s.Bye(ua.Change{}, nil)

	bob.next("ended")

	alice.p.Send(sip.UDP, b.addr, alice.inDialog("ACK", ok, "1"))

	for {
		req, _ := alice.p.RecvRequest()
		if req.Method != "BYE" {
			continue
		}

		from, _ := req.Header.From()
		to, _ := ok.Header.To()

		if from.Tag() != to.Tag() {
			t.Fatalf("BYE in dialog %q, want the 2xx's %q", from.Tag(), to.Tag())
		}

		alice.p.Send(sip.UDP, b.addr, sip.NewResponse(req, 200, ""))

		break
	}

	b.callEnded(t)
}

// RFC 3261 §14.1: re-INVITEs of both sides crossing get 491s, and succeed when retried.
func TestGlareRetried(t *testing.T) {
	alice, bob, b := newCall(t, b2bua.RelayConfig{})
	alice.invite(b.uri(), "offer-1", alice.handler())
	as, bs := bridged(t, alice, bob)

	type result struct {
		who  string
		code int
		err  error
	}

	results := make(chan result, 8)

	_ = as.Reinvite(ua.Change{Bodies: sdpOf("offer-a")}, func(res *sip.Response, err error) {
		results <- result{"alice", codeOf(res), err}
	})
	_ = bs.Reinvite(ua.Change{Bodies: sdpOf("offer-b")}, func(res *sip.Response, err error) {
		results <- result{"bob", codeOf(res), err}
	})

	// Each answers the other's re-INVITE once it gets one, after the retries.
	answer := func(p *party, sdp string) {
		for {
			select {
			case e := <-p.events:
				if e.kind == "request" {
					_ = e.req.Respond(ua.Reply{Code: 200, Bodies: sdpOf(sdp)})
				}
			default:
				return
			}
		}
	}

	got := map[string]result{}

	for range 10 {
		b.net.Advance(time.Second)
		answer(alice, "answer-a")
		answer(bob, "answer-b")

		for len(results) > 0 {
			r := <-results
			got[r.who] = r
		}
	}

	// The first retried goes through; the other, offering the session as it was before, goes stale (ua.ErrStale)
	// or through, retried after it.
	through := 0

	for _, who := range []string{"alice", "bob"} {
		switch r, ok := got[who]; {
		case !ok:
			t.Fatalf("%s's re-INVITE did not complete", who)
		case r.code == 200:
			through++
		case !errors.Is(r.err, ua.ErrStale):
			t.Fatalf("%s's re-INVITE: %d %v", who, r.code, r.err)
		}
	}

	if through == 0 {
		t.Fatal("no re-INVITE through")
	}
}

func codeOf(res *sip.Response) int {
	if res == nil {
		return 0
	}

	return res.StatusCode
}

// TS 24.379 §6.3.2.1.5: the parts of the requests and responses within the call cross byte-exact.
func TestBodiesByteExact(t *testing.T) {
	alice, bob, b := newCall(t, b2bua.RelayConfig{})
	alice.invite(b.uri(), "offer-1", alice.handler())
	as, bs := bridged(t, alice, bob)

	info := sip.NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte("<mcpttinfo>\n\t<a/> </mcpttinfo>"))
	lists := sip.NewBodyPart("application/resource-lists+xml", []byte("<resource-lists>  </resource-lists>\r\n"))

	done := make(chan *sip.Response, 2)
	_ = as.Reinvite(ua.Change{Bodies: []sip.BodyPart{ua.SDP([]byte("offer-2")), info, lists}}, func(res *sip.Response, _ error) {
		done <- res
	})

	r := bob.next("request").req

	parts, _ := r.Message().Content().Parts()
	if len(parts) != 3 || !bytes.Equal(parts[1].Body, info.Body) || !bytes.Equal(parts[2].Body, lists.Body) {
		t.Fatalf("bob's re-INVITE parts %+v", parts)
	}

	_ = r.Respond(ua.Reply{Code: 200, Bodies: []sip.BodyPart{ua.SDP([]byte("answer-2")), info}})

	alice.next("answer")

	b.net.Quiesce()

	res := <-done
	if rparts, _ := res.Content().Parts(); len(rparts) != 2 || !bytes.Equal(rparts[1].Body, info.Body) {
		t.Fatalf("alice's 2xx parts %+v", rparts)
	}

	_ = bs.Bye(ua.Change{Bodies: []sip.BodyPart{info}}, nil)

	e := alice.next("ended")
	if e.end.Request == nil || !bytes.Equal(e.end.Request.Body, info.Body) {
		t.Fatalf("alice's BYE %+v", e.end.Request)
	}
}

// A member's 2xx coming after the caller of a 1:N call left is acknowledged and ended with a BYE; nothing is left.
func TestOneToManyLate2xx(t *testing.T) {
	n := newNet(t)
	bob, carol := newParty(t, n, "bob", nil), newParty(t, n, "carol", nil)
	g := newController(t, n, bob, carol)
	alice := newParty(t, n, "alice", nil)

	o := alice.invite(g.uri(), "offer-1", alice.handler())

	rb, rc := bob.next("invite").req, carol.next("invite").req
	_ = rb.Progress(ua.Reply{Code: 180})
	_ = rc.Progress(ua.Reply{Code: 180})

	_ = o.Cancel(ua.Change{})

	alice.next("failed")

	// Carol's 200 crosses the CANCEL of her INVITE.
	_ = rc.Accept(sdpOf("answer-c")...)

	n.Quiesce()

	for _, p := range []*party{bob, carol} {
		for {
			e := p.next(p.peekKind())
			if e.kind == "ended" {
				break
			}
		}
	}

	settled(t, n, alice, bob, carol, g.party)
}

// RFC 3264 §8: a refresh answered on its leg whose answer changes the session carries the change to the other leg.
func TestRefreshAnswerPropagated(t *testing.T) {
	alice, bob, b := newCall(t, b2bua.RelayConfig{})
	alice.invite(b.uri(), "offer-1", alice.handler())
	as, _ := bridged(t, alice, bob)

	_ = as.Reinvite(ua.Change{}, nil)

	o := alice.next("offer")
	_ = o.offer.Answer(ua.Change{Bodies: sdpOf("offer-1-moved")})

	// Bob, ClientPolicy, answers the re-INVITE as a change the application decides on.
	e := bob.next("request")
	if offer, _ := e.req.Offer(); string(offer) != "offer-1-moved" {
		t.Fatalf("bob got %q", offer)
	}

	_ = e.req.Respond(ua.Reply{Code: 200, Bodies: sdpOf("answer-1")})

	// Bob's answer is the one alice has: nothing more goes to her.
	alice.none()
}
