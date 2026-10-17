package ua_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

var loopback = netip.MustParseAddr("127.0.0.1")

const wait = 5 * time.Second

type event struct {
	kind  string
	s     *ua.Session
	res   *sip.Response
	sdp   []byte
	end   ua.End
	req   *ua.Request
	offer *ua.Offer
	err   error
}

// peer is an agent on loopback UDP, recording the events of its sessions.
type peer struct {
	t      *testing.T
	name   string
	a      *ua.Agent
	addr   netip.AddrPort
	events chan event

	// tr is the transport of the requests of the peer, UDP unless set before any.
	tr sip.Transport

	mu       sync.Mutex
	onInvite func(r *ua.Request)
}

func freePort(t *testing.T) netip.AddrPort {
	t.Helper()

	c, err := new(net.ListenConfig).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := netip.MustParseAddrPort(c.LocalAddr().String())
	_ = c.Close()

	return addr
}

func newPeer(t *testing.T, name string, clock transaction.Clock, timer ua.SessionTimer) *peer {
	t.Helper()

	return newPeerWith(t, name, func(c *ua.Config) {
		c.Transaction.Clock = clock
		c.SessionTimer = timer
	})
}

func newPeerWith(t *testing.T, name string, configure func(*ua.Config)) *peer {
	t.Helper()

	// The free port may be taken again before the agent listens on it: another one is tried.
	for range 5 {
		if p := tryPeer(t, name, configure); p != nil {
			return p
		}
	}

	t.Fatalf("%s: no free port", name)

	return nil
}

func tryPeer(t *testing.T, name string, configure func(*ua.Config)) *peer {
	t.Helper()

	p := &peer{t: t, name: name, events: make(chan event, 64), addr: freePort(t), tr: sip.UDP}

	cfg := ua.Config{
		Prepare: p.prepare,
		Contact: sip.Address{URI: sip.URI{Scheme: "sip", User: name, Host: p.addr.Addr().String(), Port: p.addr.Port()}},
		OnInvite: func(r *ua.Request) ua.Handler {
			p.record(event{kind: "invite", req: r})

			p.mu.Lock()
			f := p.onInvite
			p.mu.Unlock()

			if f != nil {
				f(r)
			}

			return p.handler()
		},
	}

	configure(&cfg)

	a, err := ua.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Layer().Listen(t.Context(), p.addr); err != nil {
		_ = a.Close(context.Background())

		if errors.Is(err, syscall.EADDRINUSE) {
			return nil
		}

		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		_ = a.Close(ctx)
	})

	p.a = a

	return p
}

// prepare sends to the host and port of the top Route, or else of the Request-URI.
func (p *peer) prepare(req *sip.Request) error {
	target := req.URI
	if r, err := req.Header.TopRoute(); err == nil {
		target = r.URI
	}

	addr, err := netip.ParseAddr(target.Host)
	if err != nil {
		return err
	}

	req.Flow = sip.Flow{Transport: p.tr, Local: p.addr, Remote: netip.AddrPortFrom(addr, target.Port)}
	req.Header.Prepend("Via", sip.NewVia(p.tr, p.addr).String())

	return nil
}

func (p *peer) record(e event) {
	select {
	case p.events <- e:
	default:
		p.t.Errorf("%s: event queue full, %s dropped", p.name, e.kind)
	}
}

func (p *peer) handler() ua.Handler {
	return ua.Handler{
		Progress:    func(s *ua.Session, res *sip.Response) { p.record(event{kind: "progress", s: s, res: res}) },
		Established: func(s *ua.Session, _ sip.Message) { p.record(event{kind: "established", s: s}) },
		Answer:      func(s *ua.Session, sdp []byte) { p.record(event{kind: "answer", s: s, sdp: sdp}) },
		Offer:       func(s *ua.Session, o *ua.Offer) { p.record(event{kind: "offer", s: s, offer: o, sdp: o.SDP()}) },
		Request:     func(s *ua.Session, r *ua.Request) { p.record(event{kind: "request", s: s, req: r}) },
		Cancelled: func(s *ua.Session, r *ua.Request, _ *sip.Request) {
			p.record(event{kind: "cancelled", s: s, req: r})
		},
		Ended: func(s *ua.Session, e ua.End) { p.record(event{kind: "ended", s: s, end: e}) },
		Failed: func(o *ua.Outgoing, res *sip.Response, err error) {
			p.record(event{kind: "failed", res: res, err: err})
		},
	}
}

// advance advances the clock, and waits for the agents of peers to handle the timers that fired: a timer posts to the
// loop of its agent, which may arm the next one.
func advance(clock *siptest.Clock, d time.Duration, peers ...*peer) {
	clock.Advance(d)

	for _, p := range peers {
		ua.Sync(p.a)
	}
}

func (p *peer) next(kind string) event {
	p.t.Helper()

	select {
	case e := <-p.events:
		if e.kind != kind {
			p.t.Fatalf("%s: %s event, want %s (%+v)", p.name, e.kind, kind, e)
		}

		return e
	case <-time.After(wait):
		p.t.Fatalf("%s: no %s event", p.name, kind)
		return event{}
	}
}

// nextAll takes events of the given kinds in any order, as the events of different sessions or of an outgoing INVITE
// are.
func (p *peer) nextAll(kinds ...string) map[string]event {
	p.t.Helper()

	got := map[string]event{}

	for range kinds {
		select {
		case e := <-p.events:
			got[e.kind] = e
		case <-time.After(wait):
			p.t.Fatalf("%s: events %v, want %v", p.name, got, kinds)
		}
	}

	for _, k := range kinds {
		if _, ok := got[k]; !ok {
			p.t.Fatalf("%s: no %s event among %v", p.name, k, got)
		}
	}

	return got
}

func (p *peer) none() {
	p.t.Helper()

	select {
	case e := <-p.events:
		p.t.Fatalf("%s: unexpected %s event (%+v)", p.name, e.kind, e)
	case <-time.After(100 * time.Millisecond):
	}
}

func (p *peer) uri() string {
	return fmt.Sprintf("sip:%s@%s", p.name, p.addr)
}

func (p *peer) invite(to *peer, sdp string) *ua.Outgoing {
	p.t.Helper()

	uri, _ := sip.ParseURI(to.uri())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+p.uri()+">")
	req.Header.Add("To", "<"+to.uri()+">")

	if sdp != "" {
		req.SetBodies(ua.SDP([]byte(sdp)))
	}

	o, err := p.a.Invite(req, p.handler())
	if err != nil {
		p.t.Fatal(err)
	}

	return o
}

func sdpOf(s string) []sip.BodyPart {
	return []sip.BodyPart{ua.SDP([]byte(s))}
}

// established sets up a session from alice to bob, offer in the INVITE.
func established(t *testing.T, alice, bob *peer) (*ua.Session, *ua.Session) {
	t.Helper()

	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	if err := r.Accept(sdpOf("answer-1")...); err != nil {
		t.Fatal(err)
	}

	as := alice.next("established").s
	alice.next("answer")

	bs := bob.next("established").s

	return as, bs
}

func TestCall(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	if offer, ok := r.Offer(); !ok || string(offer) != "offer-1" {
		t.Fatalf("offer %q, %v", offer, ok)
	}

	if err := r.Progress(ua.Reply{Code: 180}); err != nil {
		t.Fatal(err)
	}

	p := alice.next("progress")
	if p.res.StatusCode != 180 || p.s == nil || p.s.State() != ua.Early {
		t.Fatalf("progress %d, session %v", p.res.StatusCode, p.s)
	}

	if err := r.Accept(sdpOf("answer-1")...); err != nil {
		t.Fatal(err)
	}

	as := alice.next("established").s
	if as != p.s {
		t.Error("the 2xx confirmed another session than the early one")
	}

	if e := alice.next("answer"); string(e.sdp) != "answer-1" {
		t.Errorf("answer %q", e.sdp)
	}

	bs := bob.next("established").s

	for _, s := range []*ua.Session{as, bs} {
		if s.State() != ua.Confirmed {
			t.Errorf("state %s", s.State())
		}
	}

	if l, rm, ok := as.Negotiated(); !ok || string(l) != "offer-1" || string(rm) != "answer-1" {
		t.Errorf("alice negotiated %q %q", l, rm)
	}

	if l, rm, ok := bs.Negotiated(); !ok || string(l) != "answer-1" || string(rm) != "offer-1" {
		t.Errorf("bob negotiated %q %q", l, rm)
	}

	if as.ID().CallID != bs.ID().CallID || as.ID().LocalTag != bs.ID().RemoteTag {
		t.Errorf("dialog IDs %+v %+v", as.ID(), bs.ID())
	}

	if err := bs.Bye(); err != nil {
		t.Fatal(err)
	}

	if e := bob.next("ended"); e.end.Cause != ua.EndBye || e.end.Remote {
		t.Errorf("bob ended %+v", e.end)
	}

	if e := alice.next("ended"); e.end.Cause != ua.EndBye || !e.end.Remote {
		t.Errorf("alice ended %+v", e.end)
	}
}

// RFC 3262: an answer in a reliable 183, the 2xx held until its PRACK.
func TestReliableProvisional(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	if err := r.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")}); err != nil {
		t.Fatal(err)
	}

	if err := r.Accept(); err != nil {
		t.Fatal(err)
	}

	if e := alice.next("answer"); string(e.sdp) != "answer-1" {
		t.Errorf("answer %q", e.sdp)
	}

	p := alice.next("progress")
	if p.res.StatusCode != 183 || !p.res.Header.HasOptionTag("Require", "100rel") {
		t.Fatalf("progress %s", p.res.StartLine())
	}

	alice.next("established")
	bob.next("established")
	alice.none()
}

// RFC 3261 §13.2.1: an INVITE without an offer gets it in the 2xx, and the answer in the ACK.
func TestLateOffer(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	alice.invite(bob, "")

	r := bob.next("invite").req
	if _, ok := r.Offer(); ok {
		t.Fatal("offer in an INVITE without SDP")
	}

	if err := r.Accept(); err == nil {
		t.Fatal("2xx without the offer accepted")
	}

	if err := r.Accept(sdpOf("offer-b")...); err != nil {
		t.Fatal(err)
	}

	alice.next("established")

	o := alice.next("offer")
	if string(o.sdp) != "offer-b" {
		t.Fatalf("offer %q", o.sdp)
	}

	if err := o.offer.Answer(sdpOf("answer-a")...); err != nil {
		t.Fatal(err)
	}

	if e := bob.next("answer"); string(e.sdp) != "answer-a" {
		t.Errorf("answer %q", e.sdp)
	}

	bs := bob.next("established").s

	if l, rm, ok := bs.Negotiated(); !ok || string(l) != "offer-b" || string(rm) != "answer-a" {
		t.Errorf("bob negotiated %q %q", l, rm)
	}
}

func TestCancel(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	o := alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	_ = r.Progress(ua.Reply{Code: 180})

	alice.next("progress")

	if err := o.Cancel(sip.Reason{Protocol: "SIP", Params: sip.Params{{Name: "cause", Value: "200"}}}); err != nil {
		t.Fatal(err)
	}

	e := bob.next("ended")
	if e.end.Cause != ua.EndCancelled || !e.end.Remote || !e.end.Request.Header.Has("Reason") {
		t.Errorf("bob ended %+v", e.end)
	}

	got := alice.nextAll("ended", "failed")

	if e := got["ended"]; e.end.Cause != ua.EndCancelled || e.end.Remote {
		t.Errorf("alice ended %+v", e.end)
	}

	if f := got["failed"]; f.res == nil || f.res.StatusCode != 487 {
		t.Errorf("failed %+v", f)
	}
}

func TestReject(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	_ = r.Progress(ua.Reply{Code: 180})

	alice.next("progress")

	if err := r.Reject(486); err != nil {
		t.Fatal(err)
	}

	if e := bob.next("ended"); e.end.Cause != ua.EndRejected || e.end.Remote || e.end.Response.StatusCode != 486 {
		t.Errorf("bob ended %+v", e.end)
	}

	got := alice.nextAll("ended", "failed")

	if e := got["ended"]; e.end.Cause != ua.EndRejected || !e.end.Remote {
		t.Errorf("alice ended %+v", e.end)
	}

	if f := got["failed"]; f.res.StatusCode != 486 {
		t.Errorf("failed %d", f.res.StatusCode)
	}
}

func TestReinviteAndUpdate(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	as, bs := established(t, alice, bob)

	done := make(chan *sip.Response, 1)

	if err := bs.Reinvite(ua.Change{Bodies: sdpOf("offer-2")}, func(res *sip.Response, err error) { done <- res }); err != nil {
		t.Fatal(err)
	}

	r := alice.next("request").req
	if offer, ok := r.Offer(); !ok || string(offer) != "offer-2" || r.Message().Method != "INVITE" {
		t.Fatalf("request %s offer %q", r.Message().Method, offer)
	}

	if err := r.Accept(sdpOf("answer-2")...); err != nil {
		t.Fatal(err)
	}

	if e := bob.next("answer"); string(e.sdp) != "answer-2" {
		t.Errorf("answer %q", e.sdp)
	}

	if res := <-done; res.StatusCode != 200 {
		t.Fatalf("re-INVITE %d", res.StatusCode)
	}

	// An UPDATE without an offer is answered by the agent.
	if err := as.Update(ua.Change{}, func(res *sip.Response, err error) { done <- res }); err != nil {
		t.Fatal(err)
	}

	if res := <-done; res.StatusCode != 200 {
		t.Fatalf("UPDATE %d", res.StatusCode)
	}

	bob.none()

	// An UPDATE with an offer goes to the application.
	if err := as.Update(ua.Change{Bodies: sdpOf("offer-3")}, func(res *sip.Response, err error) { done <- res }); err != nil {
		t.Fatal(err)
	}

	if err := as.Update(ua.Change{Bodies: sdpOf("offer-4")}, nil); err != ua.ErrPending {
		t.Errorf("second offer: %v, want ErrPending", err)
	}

	u := bob.next("request").req
	if offer, ok := u.Offer(); !ok || string(offer) != "offer-3" {
		t.Fatalf("UPDATE offer %q", offer)
	}

	_ = u.Accept(sdpOf("answer-3")...)

	alice.next("answer")

	if res := <-done; res.StatusCode != 200 {
		t.Fatalf("UPDATE %d", res.StatusCode)
	}

	if l, rm, _ := as.Negotiated(); string(l) != "offer-3" || string(rm) != "answer-3" {
		t.Errorf("alice negotiated %q %q", l, rm)
	}
}

// A request the application does not handle: 405 with Allow.
func TestOtherRequests(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	as, _ := established(t, alice, bob)

	done := make(chan *sip.Response, 1)

	if err := as.Send("INFO", ua.Change{}, func(res *sip.Response, err error) { done <- res }); err != nil {
		t.Fatal(err)
	}

	r := bob.next("request").req
	if r.Message().Method != "INFO" {
		t.Fatalf("request %s", r.Message().Method)
	}

	_ = r.Respond(ua.Reply{Code: 200})

	if res := <-done; res.StatusCode != 200 {
		t.Fatalf("INFO %d", res.StatusCode)
	}

	if err := as.Send("BYE", ua.Change{}, nil); err == nil {
		t.Error("BYE through Send")
	}
}

// RFC 3261 §8.2.2.3, RFC 4028 §9.
func TestInviteRefused(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{MinSE: 1800 * time.Second})

	for _, tc := range []struct {
		header sip.Field
		code   int
		want   sip.Field
	}{
		{sip.Field{Name: "Require", Value: "foo, bar"}, 420, sip.Field{Name: "Unsupported", Value: "foo, bar"}},
		{sip.Field{Name: "Session-Expires", Value: "100"}, 422, sip.Field{Name: "Min-SE", Value: "1800"}},
	} {
		sock := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
		req := siptest.NewRequest("INVITE", bob.uri(), sip.UDP, sock.Addr())
		req.Header.Add(tc.header.Name, tc.header.Value)
		req.Header.Add("Supported", "timer")
		sock.Send(sip.UDP, bob.addr, req)

		res, _ := sock.RecvResponse()
		if res.StatusCode == 100 {
			res, _ = sock.RecvResponse()
		}

		if res.StatusCode != tc.code || res.Header.Get(tc.want.Name) != tc.want.Value {
			t.Errorf("%s: %s %s=%q", tc.header.Name, res.StartLine(), tc.want.Name, res.Header.Get(tc.want.Name))
		}
	}

	bob.none()
}

// RFC 3261 §13.2.2.4: the 2xx of a second dialog of a forked INVITE is acknowledged, and the dialog ended with a BYE.
func TestForking(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	fork := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	uri, _ := sip.ParseURI("sip:bob@" + fork.Addr().String())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+uri.String()+">")
	req.SetBodies(ua.SDP([]byte("offer-1")))

	if _, err := alice.a.Invite(req, alice.handler()); err != nil {
		t.Fatal(err)
	}

	inv, f := fork.RecvRequest()

	respond := func(code int, tag, sdp string) {
		res := sip.NewResponse(inv, code, "")
		_ = res.Header.SetToTag(tag)
		res.Header.Add("Contact", "<sip:"+tag+"@"+fork.Addr().String()+">")

		if sdp != "" {
			res.SetBodies(ua.SDP([]byte(sdp)))
		}

		fork.Send(sip.UDP, f.Remote, res)
	}

	respond(180, "one", "")
	respond(180, "two", "")

	// The events of two sessions come in any order: each is told apart by its tag.
	byTag := map[string]*ua.Session{}

	for range 2 {
		p := alice.next("progress")
		to, _ := p.res.Header.To()
		byTag[to.Tag()] = p.s
	}

	one, two := byTag["one"], byTag["two"]
	if one == nil || two == nil || one == two {
		t.Fatalf("sessions %v, want one for each early dialog", byTag)
	}

	respond(200, "one", "answer-1")

	if alice.next("established").s != one {
		t.Error("the 2xx confirmed the wrong session")
	}

	alice.next("answer")

	if ack, _ := fork.RecvRequest(); ack.Method != "ACK" || !strings.Contains(ack.Header.Get("To"), "one") {
		t.Fatalf("got %s to %s, want the ACK of one", ack.Method, ack.Header.Get("To"))
	}

	respond(200, "two", "answer-2")

	ack, _ := fork.RecvRequest()
	bye, _ := fork.RecvRequest()

	if ack.Method != "ACK" || bye.Method != "BYE" || !strings.Contains(bye.Header.Get("To"), "two") {
		t.Fatalf("got %s and %s, want the ACK and BYE of two", ack.Method, bye.Method)
	}

	if e := alice.next("ended"); e.s != two || e.end.Cause != ua.EndNotChosen {
		t.Errorf("ended %+v", e.end)
	}
}

// RFC 3261 §13.3.1.4: the 2xx is retransmitted until the ACK; without one, the session ends with a BYE.
func TestNoAck(t *testing.T) {
	clock := siptest.NewClock()
	bob := newPeer(t, "bob", clock, ua.SessionTimer{})
	sock := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	req := siptest.NewRequest("INVITE", bob.uri(), sip.UDP, sock.Addr())
	req.SetBodies(ua.SDP([]byte("offer-1")))
	sock.Send(sip.UDP, bob.addr, req)

	r := bob.next("invite").req
	_ = r.Accept(sdpOf("answer-1")...)

	recvFinal := func() *sip.Response {
		t.Helper()

		for {
			res, _ := sock.RecvResponse()
			if res.StatusCode != 100 {
				return res
			}
		}
	}

	if res := recvFinal(); res.StatusCode != 200 {
		t.Fatalf("got %d", res.StatusCode)
	}

	advance(clock, 500*time.Millisecond, bob)

	if res := recvFinal(); res.StatusCode != 200 {
		t.Fatalf("retransmission %d", res.StatusCode)
	}

	for range 40 {
		advance(clock, 4*time.Second, bob)
	}

	for {
		m := sock.Recv()
		if req, ok := m.Msg.(*sip.Request); ok {
			if req.Method != "BYE" {
				t.Fatalf("got %s, want the BYE", req.Method)
			}

			break
		}
	}

	if e := bob.next("ended"); e.end.Cause != ua.EndTimeout {
		t.Errorf("ended %+v", e.end)
	}
}

// RFC 3262 §3: the reliable 1xx is retransmitted until its PRACK; without one, the INVITE gets a 500.
func TestNoPrack(t *testing.T) {
	clock := siptest.NewClock()
	bob := newPeer(t, "bob", clock, ua.SessionTimer{})
	sock := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	req := siptest.NewRequest("INVITE", bob.uri(), sip.UDP, sock.Addr())
	req.Header.Add("Supported", "100rel")
	req.SetBodies(ua.SDP([]byte("offer-1")))
	sock.Send(sip.UDP, bob.addr, req)

	r := bob.next("invite").req
	if err := r.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")}); err != nil {
		t.Fatal(err)
	}

	if err := r.Accept(); err != nil {
		t.Fatal(err)
	}

	var codes []int

	for len(codes) < 2 {
		res, _ := sock.RecvResponse()
		if res.StatusCode != 100 {
			codes = append(codes, res.StatusCode)
		}

		if len(codes) == 1 {
			advance(clock, 500*time.Millisecond, bob)
		}
	}

	if codes[0] != 183 || codes[1] != 183 {
		t.Fatalf("got %v, want the 183 and its retransmission, the 2xx held", codes)
	}

	for range 70 {
		advance(clock, time.Second, bob)
	}

	for {
		res, _ := sock.RecvResponse()
		if res.StatusCode >= 200 {
			if res.StatusCode != 500 {
				t.Fatalf("final %d, want 500", res.StatusCode)
			}

			break
		}
	}

	if e := bob.next("ended"); e.end.Cause != ua.EndTimeout {
		t.Errorf("ended %+v", e.end)
	}
}

// RFC 3261 §14.1: re-INVITEs crossing each other both get a 491, and are retried after random delays.
func TestGlare(t *testing.T) {
	clock := siptest.NewClock()
	alice, bob := newPeer(t, "alice", clock, ua.SessionTimer{}), newPeer(t, "bob", clock, ua.SessionTimer{})
	as, bs := established(t, alice, bob)

	results := make(chan string, 2)
	result := func(who string) func(*sip.Response, error) {
		return func(res *sip.Response, err error) {
			if err != nil {
				results <- fmt.Sprint(who, " ", err)
				return
			}

			results <- fmt.Sprint(who, " ", res.StatusCode)
		}
	}

	if err := as.Reinvite(ua.Change{Bodies: sdpOf("offer-a")}, result("alice")); err != nil {
		t.Fatal(err)
	}

	if err := bs.Reinvite(ua.Change{Bodies: sdpOf("offer-b")}, result("bob")); err != nil {
		t.Fatal(err)
	}

	answer := func(p *peer, sdp string) {
		r := p.next("request").req
		_ = r.Accept(sdpOf(sdp)...)
	}

	// Each re-INVITE meets the other in progress, gets a 491, and is retried; the callee's retry comes first.
	deadline := time.Now().Add(wait)
	for ua.Reinviting(as) || ua.Reinviting(bs) {
		if time.Now().After(deadline) {
			t.Fatal("no 491")
		}

		time.Sleep(10 * time.Millisecond)
	}

	advance(clock, 2*time.Second, alice, bob)
	answer(alice, "answer-b")
	bob.next("answer")
	advance(clock, 2*time.Second, alice, bob)
	answer(bob, "answer-a")
	alice.next("answer")

	got := []string{<-results, <-results}
	if got[0] != "bob 200" || got[1] != "alice 200" {
		t.Errorf("results %v", got)
	}
}

// RFC 4028: the session interval negotiated, refreshed by the refresher at half; without refreshes the session
// expires.
func TestSessionTimer(t *testing.T) {
	clock := siptest.NewClock()
	timer := ua.SessionTimer{Interval: 120 * time.Second}
	alice, bob := newPeer(t, "alice", clock, timer), newPeer(t, "bob", clock, timer)

	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	if se := r.Message().Header.Get("Session-Expires"); se != "120" || !r.Message().Header.HasOptionTag("Supported", "timer") {
		t.Fatalf("INVITE Session-Expires %q", se)
	}

	_ = r.Accept(sdpOf("answer-1")...)

	alice.next("established")
	alice.next("answer")
	bob.next("established")

	// Bob, the UAS, refreshes by default, with an UPDATE since alice allows it. Without the refresh, alice would end
	// the session 88 s in (RFC 4028 §10).
	for range 10 {
		advance(clock, 10*time.Second, alice, bob)
		time.Sleep(20 * time.Millisecond)
	}

	alice.none()
	bob.none()

	// Without refreshes, the session expires.
	_ = bob.a.Layer().Close()

	advance(clock, 120*time.Second, alice, bob)

	if e := alice.next("ended"); e.end.Cause != ua.EndExpired {
		t.Errorf("alice ended %+v", e.end)
	}
}

// RFC 4028 §7.3: an INVITE refused with a 422 is sent again with the Min-SE.
func TestSessionIntervalTooSmall(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{Interval: 120 * time.Second})
	bob := newPeer(t, "bob", nil, ua.SessionTimer{MinSE: 1800 * time.Second})

	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	if se, mse := r.Message().Header.Get("Session-Expires"), r.Message().Header.Get("Min-SE"); se != "1800" || mse != "1800" {
		t.Fatalf("INVITE Session-Expires %q, Min-SE %q", se, mse)
	}

	if cseq, _ := r.Message().Header.CSeq(); cseq.Seq != 2 {
		t.Errorf("CSeq %d, want the next one", cseq.Seq)
	}

	_ = r.Accept(sdpOf("answer-1")...)

	alice.next("established")
	alice.next("answer")
}
