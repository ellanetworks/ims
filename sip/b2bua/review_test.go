package b2bua_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

type rawCaller struct {
	t      *testing.T
	p      *siptest.Peer
	to     *party
	invite *sip.Request
}

func callRaw(t *testing.T, n *siptest.Net, to *party, sdp string, header ...sip.Field) *rawCaller {
	c := &rawCaller{t: t, p: n.Peer(), to: to}

	c.invite = siptest.NewRequest("INVITE", to.uri(), sip.UDP, c.p.Addr())
	for _, f := range header {
		c.invite.Header.Add(f.Name, f.Value)
	}

	if sdp != "" {
		c.invite.SetBodies(sdpOf(sdp)...)
	}

	c.p.Send(sip.UDP, to.addr, c.invite)

	return c
}

func (c *rawCaller) response(least int) *sip.Response {
	c.t.Helper()

	for {
		res, _ := c.p.RecvResponse()
		if res.StatusCode >= least {
			return res
		}
	}
}

func (c *rawCaller) inDialog(method string, res *sip.Response, seq string) *sip.Request {
	req := siptest.NewRequest(method, c.to.uri(), sip.UDP, c.p.Addr())
	req.Header.Set("From", c.invite.Header.Get("From"))
	req.Header.Set("To", res.Header.Get("To"))
	req.Header.Set("Call-ID", c.invite.Header.CallID())
	req.Header.Set("CSeq", seq+" "+method)

	return req
}

// RFC 3261 section 15
func TestEndBeforeTheAck(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{})

	alice := callRaw(t, n, b.party, "offer-1")

	in := bob.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	ok := alice.response(200)
	bs := bob.next("established").s

	_ = bs.Bye(context.Background(), ua.Change{}, nil)

	bob.next("ended")

	ack := alice.inDialog("ACK", ok, "1")
	alice.p.Send(sip.UDP, b.addr, ack)

	for {
		req, _ := alice.p.RecvRequest()
		if req.Method == "BYE" {
			alice.p.Send(sip.UDP, b.addr, sip.NewResponse(req, 200, ""))
			break
		}
	}

	b.callEnded(t)
	settled(t, n, bob, b.party)
}

// TS 24.379 clause 6.3.2.2.5.2
func TestEndWith2xxHeld(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{AfterPrack: true})

	alice := callRaw(t, n, b.party, "offer-1", sip.Field{Name: "Supported", Value: "100rel"})

	in := bob.next("invite").req
	_ = in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")})

	if res := alice.response(101); res.StatusCode != 183 || !res.Header.HasOptionTag("Require", sip.OptionTag100rel) {
		t.Fatalf("alice got %d", res.StatusCode)
	}

	_ = in.Accept()

	bs := bob.next("established").s
	_ = bs.Bye(context.Background(), ua.Change{}, nil)

	bob.next("ended")

	if res := alice.response(200); res.StatusCode != 487 {
		t.Fatalf("alice's INVITE %d, want 487", res.StatusCode)
	}

	b.callEnded(t)
}

// RFC 3261 section 13.2.1
func TestAnswerRepeatedIn2xx(t *testing.T) {
	n := newNet(t)
	bob := n.Peer()
	b := newB2B(t, n, func() *party { return &party{name: "bob", addr: bob.Addr()} }, b2bua.RelayConfig{})
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	invite, _ := bob.RecvRequest()
	tag := sip.NewTag()

	respond := func(code int, sdp string) {
		res := sip.NewResponse(invite, code, "")
		_ = res.Header.SetToTag(tag)
		res.Header.Set("Contact", "<sip:bob@"+bob.Addr().String()+">")

		if sdp != "" {
			res.SetBodies(sdpOf(sdp)...)
		}

		bob.Send(sip.UDP, invite.Flow.Remote, res)
	}

	respond(183, "answer-1")
	alice.next("progress")
	respond(200, "")

	if e := alice.next("established"); !hasSDP(e.msg, "answer-1") {
		t.Fatalf("alice's 200 without the answer: %q", e.msg.Env().Body)
	}
}

func TestEarlyByeOfTheCaller(t *testing.T) {
	alice, bob, first, fork, a1, a2 := forkedCall(t)

	_ = a1.Bye(context.Background(), ua.Change{}, nil)

	if e := bob.next("ended"); e.s != first.Session() {
		t.Fatalf("bob's other dialog ended: %+v", e.end)
	}

	alice.next("ended")
	bob.none()

	_ = fork.Accept()

	if e := alice.next("established"); e.s != a2 {
		t.Error("another dialog confirmed")
	}

	bob.next("established")
}

// RFC 4028
func TestPlainLegLives(t *testing.T) {
	n := newNet(t)

	server := newParty(t, n, "server", func(cfg *ua.Config) { cfg.Policy = ua.Policy{} })
	server.mu.Lock()
	server.onInvite = func(r *ua.Request) ua.Handler {
		call := b2bua.NewCall(server.a, r.Strand(), b2bua.CallHandler{})
		_, _ = call.Incoming(r, b2bua.LegHandler{})
		_ = r.Accept(sdpOf("server-answer")...)

		return ua.Handler{}
	}
	server.mu.Unlock()

	alice := newParty(t, n, "alice", func(c *ua.Config) { c.SessionTimer = ua.SessionTimer{Interval: 90 * time.Second} })
	alice.invite(server.uri(), "offer-1", alice.handler())

	as := alice.next("established").s
	alice.next("answer")

	for range 4 {
		n.Advance(60 * time.Second)
	}

	if as.State() != ua.Confirmed {
		t.Fatalf("the session %s after its intervals", as.State())
	}

	done := make(chan *sip.Response, 1)
	_ = as.Send(context.Background(), "OPTIONS", ua.Change{}, func(res *sip.Response, _ error) { done <- res })

	n.Quiesce()

	if res := <-done; res == nil || res.StatusCode != 200 {
		t.Fatalf("OPTIONS %+v", res)
	}
}

// RFC 3311 section 5.1
func TestEarlyUpdateInItsMirror(t *testing.T) {
	f := forkedCallOf(t)
	alice, fork, a2 := f.alice, f.fork, f.a2

	_ = fork.Session().Update(context.Background(), ua.Change{Bodies: sdpOf("offer-3")}, nil)

	e := alice.next("request")
	if e.s != a2 || e.req.Message().Method != "UPDATE" {
		t.Fatalf("the UPDATE reached the other dialog (%v) or is a %s", e.s == a2, e.req.Message().Method)
	}

	_ = e.req.Respond(ua.Reply{Code: 200, Bodies: sdpOf("answer-3")})
}

func TestOwnOfferOnARelayedLeg(t *testing.T) {
	alice, bob, b := newCall(t, b2bua.RelayConfig{})
	alice.invite(b.uri(), "offer-1", alice.handler())
	bridged(t, alice, bob)

	rel := <-b.relays
	st := b.strands[0]

	offers := make(chan *ua.Offer, 1)
	done := make(chan struct{})

	st.Post(func() {
		defer close(done)

		rel.B().SetHandler(b2bua.LegHandler{Offer: func(_ context.Context, _ *b2bua.Leg, _ *ua.Session, o *ua.Offer) { offers <- o }})

		if err := rel.B().Session().Reinvite(context.Background(), ua.Change{}, nil); err != nil {
			t.Error(err)
		}
	})

	b.net.Quiesce()
	<-done

	select {
	case o := <-offers:
		if string(o.SDP()) != "answer-1" {
			t.Errorf("offer %q", o.SDP())
		}

		_ = o.Answer(context.Background(), ua.Change{Bodies: sdpOf("offer-1")})
	default:
		t.Fatal("the application did not get the offer")
	}

	alice.none()
}

// RFC 3326
func TestReasonOfTheBye(t *testing.T) {
	alice, bob, b := newCall(t, b2bua.RelayConfig{})
	alice.invite(b.uri(), "offer-1", alice.handler())
	as, _ := bridged(t, alice, bob)

	reason, _ := sip.NewReason("SIP", 200, "user ended")
	_ = as.Bye(context.Background(), ua.Reasons(reason), nil)

	e := bob.next("ended")
	if e.end.Request == nil || e.end.Request.Header.Get("Reason") == "" {
		t.Fatalf("bob's BYE without the Reason: %+v", e.end.Request)
	}
}

// TS 24.379 clause 6.3.2.1.4
func TestBridgeKeepsTheApplicationBodies(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)

	own := sip.NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte("<mcpttinfo>server</mcpttinfo>"))

	var b *party

	b = newParty(t, n, "b2b", func(c *ua.Config) {
		c.Policy = ua.Policy{}
		c.OnInvite = func(_ context.Context, r *ua.Request) ua.Handler {
			call := b2bua.NewCall(b.a, r.Strand(), b2bua.CallHandler{})

			uri, _ := sip.ParseURI(bob.uri())
			out := sip.NewRequest("INVITE", uri)
			out.Header.Add("From", "<sip:alice@example.com>")
			out.Header.Add("To", "<"+bob.uri()+">")
			out.SetBodies(own)

			_, _ = b2bua.Bridge(context.Background(), call, r, out, b2bua.RelayConfig{})

			return ua.Handler{}
		}
	})

	alice := newParty(t, n, "alice", nil)

	uri, _ := sip.ParseURI(b.uri())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+b.uri()+">")

	lists := sip.NewBodyPart("application/resource-lists+xml", []byte("<resource-lists/>"))
	req.SetBodies(ua.SDP([]byte("offer-1")),
		sip.NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte("<mcpttinfo>client</mcpttinfo>")), lists)

	_, _ = alice.a.Invite(context.Background(), req, alice.handler())

	parts, _ := bob.next("invite").req.Message().Content().Parts()

	var got []string
	for _, p := range parts {
		got = append(got, string(p.Body))
	}

	if len(parts) != 3 || !bytes.Equal(parts[0].Body, []byte("offer-1")) || !bytes.Equal(parts[1].Body, own.Body) ||
		!bytes.Equal(parts[2].Body, lists.Body) {
		t.Fatalf("bob's parts %q", got)
	}
}

func TestMediaErrorEndsTheCall(t *testing.T) {
	n := newNet(t)
	bob := newParty(t, n, "bob", nil)
	b := newB2B(t, n, func() *party { return bob }, b2bua.RelayConfig{Media: failing{}})
	alice := newParty(t, n, "alice", nil)

	alice.invite(b.uri(), "offer-1", alice.handler())

	if e := alice.next("failed"); e.res == nil || e.res.StatusCode != 488 {
		t.Fatalf("alice's INVITE %+v", e.res)
	}

	b.callEnded(t)
	bob.none()
	settled(t, n, b.party, bob)
}

type failing struct{}

func (failing) Offer(*b2bua.Exchange) ([]byte, error) {
	return nil, errors.New("no media")
}

func (failing) Answer(*b2bua.Exchange, *ua.Session, []byte) ([]byte, error) {
	return nil, errors.New("no media")
}

func (failing) End(*b2bua.Exchange, *ua.Session) {}
