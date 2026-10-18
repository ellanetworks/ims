package ua_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

type result struct {
	res *sip.Response
	err error
}

func results() (chan result, func(*sip.Response, error)) {
	c := make(chan result, 64)
	return c, func(res *sip.Response, err error) { c <- result{res, err} }
}

func (r result) code() int {
	if r.res == nil {
		return 0
	}

	return r.res.StatusCode
}

func recv(t *testing.T, c chan result) result {
	t.Helper()

	select {
	case r := <-c:
		return r
	case <-time.After(wait):
		t.Fatal("no result")
		return result{}
	}
}

// RFC 3261 §9: a re-INVITE of the agent cancelled once in progress gets a 487; the session goes on unchanged, on both
// sides.
func TestCancelReinvite(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	as, bs := established(t, alice, bob)

	done, f := results()

	if err := bs.Reinvite(ua.Change{Bodies: sdpOf("offer-2")}, f); err != nil {
		t.Fatal(err)
	}

	r := alice.next("request").req
	if err := r.Progress(ua.Reply{Code: 180}); err != nil {
		t.Fatal(err)
	}

	if e := bob.next("progress"); e.s != bs || e.res.StatusCode != 180 {
		t.Errorf("progress of the re-INVITE %+v", e)
	}

	reason, _ := sip.NewReason("SIP", 487, "")

	if err := bs.CancelReinvite(ua.Reasons(reason)); err != nil {
		t.Fatal(err)
	}

	if e := alice.next("cancelled"); e.req != r || e.s != as {
		t.Errorf("cancelled %+v", e)
	}

	if res := recv(t, done); res.code() != 487 {
		t.Fatalf("re-INVITE %d %v", res.code(), res.err)
	}

	if err := r.Accept(sdpOf("answer-2")...); err == nil {
		t.Error("the cancelled re-INVITE accepted")
	}

	if err := bs.CancelReinvite(ua.Change{}); !errors.Is(err, ua.ErrState) {
		t.Errorf("CancelReinvite without a re-INVITE: %v", err)
	}

	for _, s := range []*ua.Session{as, bs} {
		if l, rm, _ := s.Negotiated(); s.State() != ua.Confirmed || string(l)+string(rm) != "offer-1answer-1" && string(l)+string(rm) != "answer-1offer-1" {
			t.Errorf("after the CANCEL: %s, negotiated %q %q", s.State(), l, rm)
		}
	}

	// The next re-INVITE goes through.
	if err := bs.Reinvite(ua.Change{Bodies: sdpOf("offer-3")}, f); err != nil {
		t.Fatal(err)
	}

	_ = alice.next("request").req.Accept(sdpOf("answer-3")...)
	bob.next("answer")

	if res := recv(t, done); res.code() != 200 {
		t.Fatalf("re-INVITE %d", res.code())
	}
}

// A UAS whose 2xx awaits its ACK sends its re-INVITEs and UPDATEs once the ACK comes; one cancelled meanwhile is not
// sent.
func TestModificationWaitsForAck(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	r := newRaw(t)

	invite := r.invite(bob, realSDP(4000, 1))
	in := bob.next("invite").req
	_ = in.Accept(ua.SDP([]byte(realSDP(5000, 1))))
	ok := r.response()
	bs := in.Session()

	done, f := results()

	if err := bs.Reinvite(ua.Change{Bodies: sdpOf(realSDP(5002, 2))}, f); err != nil {
		t.Fatalf("re-INVITE before the ACK: %v", err)
	}

	if err := bs.CancelReinvite(ua.Change{}); err != nil {
		t.Fatal(err)
	}

	if res := recv(t, done); !errors.Is(res.err, ua.ErrCancelled) {
		t.Errorf("cancelled before sent: %+v", res)
	}

	if err := bs.Reinvite(ua.Change{Bodies: sdpOf(realSDP(5002, 2))}, f); err != nil {
		t.Fatal(err)
	}

	// Only the 2xx, retransmitted, comes before the ACK.
	advance(netOf(t).Clock(), 700*time.Millisecond)

	for {
		m, got := r.sock.RecvWithin(0)
		if !got {
			break
		}

		if _, isReq := m.Msg.(*sip.Request); isReq {
			t.Fatalf("%s before the ACK", m.Msg.StartLine())
		}
	}

	r.ack(invite, ok)
	bob.next("established")

	for {
		req, _ := r.sock.RecvRequest()
		if req.Method == "INVITE" && req.Header.Get("To") != invite.Header.Get("To") {
			if !strings.Contains(string(req.Body), "5002") {
				t.Errorf("re-INVITE offer %q", req.Body)
			}

			break
		}
	}
}

// RFC 3261 §12.2.2: the requests of a dialog reach the peer in the order of their CSeq, without waiting for each
// other; those in progress when the session ends complete with ErrState.
func TestSendInOrder(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	as, _ := established(t, alice, bob)

	done, f := results()

	const n = 20

	for i := range n {
		if err := as.Send("INFO", ua.Change{Bodies: []sip.BodyPart{sip.NewBodyPart("text/plain", []byte(fmt.Sprint(i)))}}, f); err != nil {
			t.Fatal(err)
		}
	}

	var last uint32

	for i := range n {
		r := bob.next("request").req
		cseq, _ := r.Message().Header.CSeq()

		if string(r.Message().Body) != fmt.Sprint(i) || i > 0 && cseq.Seq != last+1 {
			t.Fatalf("INFO %d: body %q, CSeq %d after %d", i, r.Message().Body, cseq.Seq, last)
		}

		last = cseq.Seq

		if i < n-1 {
			_ = r.Respond(ua.Reply{Code: 200})
		}
	}

	for range n - 1 {
		if res := recv(t, done); res.code() != 200 {
			t.Fatalf("INFO %d", res.code())
		}
	}

	_ = as.Bye(ua.Change{}, nil)

	if res := recv(t, done); !errors.Is(res.err, ua.ErrState) {
		t.Errorf("INFO in progress at the BYE: %+v", res)
	}
}

// RFC 6141 §4.6: the Contact of a target refresh request becomes the remote target once a 2xx to it is sent, not
// with an error response.
func TestTargetRefreshedByTheResponse(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	r := newRaw(t)

	invite := r.invite(bob, realSDP(4000, 1))
	in := bob.next("invite").req
	_ = in.Accept(ua.SDP([]byte(realSDP(5000, 1))))
	ok := r.response()
	r.ack(invite, ok)

	bs := bob.next("established").s

	target := func(want string) {
		t.Helper()

		if err := bs.Send("INFO", ua.Change{}, nil); err != nil {
			t.Fatal(err)
		}

		for {
			req, f := r.sock.RecvRequest()
			if req.Method != "INFO" {
				continue
			}

			if user := req.URI.User; user != want {
				t.Errorf("INFO to %s, want %s", req.URI, want)
			}

			r.sock.Send(sip.UDP, f.Remote, sip.NewResponse(req, 200, ""))

			return
		}
	}

	update := func(seq uint32, user string, code int) {
		t.Helper()

		req := r.inDialog("UPDATE", invite, ok, seq)
		req.Header.Set("Contact", "<sip:"+user+"@"+r.sock.Addr().String()+">")
		req.SetBodies(ua.SDP([]byte(realSDP(4000+int(seq), int(seq)))))
		r.sock.Send(sip.UDP, bob.addr, req)

		u := bob.next("request").req

		if code == 200 {
			_ = u.Accept(ua.SDP([]byte(realSDP(5000+int(seq), int(seq)))))
		} else {
			_ = u.Reject(code)
		}

		if res := r.response(); res.StatusCode != code {
			t.Fatalf("UPDATE %d, want %d", res.StatusCode, code)
		}
	}

	update(2, "refused", 488)
	target("ue")
	update(3, "moved", 200)
	target("moved")
}

// A session whose timer expires while a re-INVITE is in progress ends only if it fails: its 2xx refreshes the session.
func TestExpiryDeferredByModification(t *testing.T) {
	for _, accept := range []bool{true, false} {
		clock := netOf(t).Clock()
		timer := ua.SessionTimer{Interval: 120 * time.Second}

		// Alice does not allow UPDATE: bob, the refresher, refreshes with re-INVITEs, which conflict with hers.
		alice := newPeerWith(t, "alice", func(c *ua.Config) {
			c.Transaction.Clock = clock
			c.SessionTimer = timer
			c.Allow = slices.DeleteFunc(slices.Clone(ua.Methods), func(m string) bool { return m == "UPDATE" })
		})
		bob := newPeer(t, "bob", clock, timer)
		as, _ := established(t, alice, bob)

		done, f := results()

		advance(clock, 50*time.Second, alice, bob)

		if err := as.Reinvite(ua.Change{Bodies: sdpOf("offer-2")}, f); err != nil {
			t.Fatal(err)
		}

		r := bob.next("request").req
		_ = r.Progress(ua.Reply{Code: 180})

		alice.next("progress")

		// Past the expiry of both sides: alice at 88 s, bob at 120 s.
		for range 8 {
			advance(clock, 10*time.Second, alice, bob)
		}

		alice.none()
		bob.none()

		if !accept {
			_ = r.Reject(488)

			// Bob's BYE follows his 488, and may overtake it: the session of alice ends with her re-INVITE.
			if res := recv(t, done); res.code() != 488 && !errors.Is(res.err, ua.ErrState) {
				t.Fatalf("re-INVITE %d %v", res.code(), res.err)
			}

			alice.next("ended")
			bob.next("ended")

			continue
		}

		_ = r.Accept(sdpOf("answer-2")...)

		alice.next("answer")

		if res := recv(t, done); res.code() != 200 {
			t.Fatalf("re-INVITE %d", res.code())
		}

		if as.State() != ua.Confirmed {
			t.Errorf("state %s after the 2xx", as.State())
		}

		alice.none()
		bob.none()
	}
}

// RFC 4028 §10: a refresh that conflicts with a transaction of the peer is retried, and refreshes the session.
func TestRefreshRetriedAfterConflict(t *testing.T) {
	clock := netOf(t).Clock()
	timer := ua.SessionTimer{Interval: 120 * time.Second}

	alice := newPeerWith(t, "alice", func(c *ua.Config) {
		c.Transaction.Clock = clock
		c.SessionTimer = timer
		c.Allow = slices.DeleteFunc(slices.Clone(ua.Methods), func(m string) bool { return m == "UPDATE" })
	})
	bob := newPeer(t, "bob", clock, timer)

	alice.invite(bob, realSDP(4000, 1))
	_ = bob.next("invite").req.Accept(ua.SDP([]byte(realSDP(5000, 1))))
	as := alice.next("established").s
	alice.next("answer")
	bob.next("established")

	advance(clock, 55*time.Second, alice, bob)

	// Alice's offer in an UPDATE is pending at bob when he is due to refresh, at 60 s.
	done, f := results()

	if err := as.Update(ua.Change{Bodies: sdpOf(realSDP(4002, 2))}, f); err != nil {
		t.Fatal(err)
	}

	u := bob.next("request").req

	advance(clock, 6*time.Second, alice, bob)

	_ = u.Reject(488)

	if res := recv(t, done); res.code() != 488 {
		t.Fatalf("UPDATE %d", res.code())
	}

	// The retry, 5 s later, is a re-INVITE offering the session unchanged, which alice answers herself.
	advance(clock, 5*time.Second, alice, bob)

	if e := bob.next("answer"); !strings.Contains(string(e.sdp), "4000") {
		t.Errorf("refresh answered with %q", e.sdp)
	}

	// The session lives past its first expiry, refreshed again at half its interval.
	for range 12 {
		advance(clock, 10*time.Second, alice, bob)
	}

	alice.none()

	for {
		select {
		case e := <-bob.events:
			if e.kind != "answer" {
				t.Fatalf("bob: %s event", e.kind)
			}

			continue
		default:
		}

		break
	}

	if as.State() != ua.Confirmed {
		t.Errorf("state %s", as.State())
	}
}

// RFC 3261 §14.1: a re-INVITE without a response times out, and the session ends, lost, with a BYE.
func TestReinviteTimeout(t *testing.T) {
	clock := netOf(t).Clock()
	bob := newPeer(t, "bob", clock, ua.SessionTimer{})
	r := newRaw(t)

	invite := r.invite(bob, realSDP(4000, 1))
	in := bob.next("invite").req
	_ = in.Accept(ua.SDP([]byte(realSDP(5000, 1))))
	ok := r.response()
	r.ack(invite, ok)

	bs := bob.next("established").s

	done, f := results()

	if err := bs.Reinvite(ua.Change{Bodies: sdpOf(realSDP(5002, 2))}, f); err != nil {
		t.Fatal(err)
	}

	for range 20 {
		advance(clock, 4*time.Second, bob)
	}

	if res := recv(t, done); !errors.Is(res.err, transaction.ErrTimeout) {
		t.Errorf("re-INVITE %+v, want a timeout", res)
	}

	if e := bob.next("ended"); e.end.Cause != ua.EndLost {
		t.Errorf("ended %+v", e.end)
	}

	for {
		if req, _ := r.sock.RecvRequest(); req.Method == "BYE" {
			break
		}
	}
}

// RFC 3262 §4, RFC 3261 §13.2.2.4: the RSeq of reliable 1xx and the ACKs of 2xx are kept for 64*T1 after the 2xx,
// then dropped.
func TestKeptForRetransmissions(t *testing.T) {
	clock := netOf(t).Clock()
	alice, bob := newPeer(t, "alice", clock, ua.SessionTimer{}), newPeer(t, "bob", clock, ua.SessionTimer{})

	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	_ = r.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")})

	alice.nextAll("answer", "progress")

	_ = r.Accept()

	as := alice.next("established").s
	bob.next("established")

	if rseq, acks := ua.Kept(as); rseq != 1 || acks != 1 {
		t.Errorf("kept %d RSeq, %d ACKs", rseq, acks)
	}

	advance(clock, 33*time.Second, alice, bob)

	if rseq, acks := ua.Kept(as); rseq != 0 || acks != 0 {
		t.Errorf("after 64*T1: kept %d RSeq, %d ACKs", rseq, acks)
	}
}

// RFC 3311 §5.1: an UPDATE with an offer in an early dialog, once the initial exchange completed.
func TestUpdateInEarlyDialog(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	alice.invite(bob, "offer-1")

	r := bob.next("invite").req
	_ = r.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf("answer-1")})

	early := alice.nextAll("answer", "progress")["progress"].s
	done, f := results()

	if err := early.Update(ua.Change{Bodies: sdpOf("offer-2")}, f); err != nil {
		t.Fatal(err)
	}

	u := bob.next("request").req
	if offer, ok := u.Offer(); !ok || string(offer) != "offer-2" || u.Message().Method != "UPDATE" {
		t.Fatalf("%s offer %q", u.Message().Method, offer)
	}

	_ = u.Accept(sdpOf("answer-2")...)

	alice.next("answer")

	if res := recv(t, done); res.code() != 200 {
		t.Fatalf("UPDATE %d", res.code())
	}

	if err := r.Accept(); err != nil {
		t.Fatal(err)
	}

	if s := alice.next("established").s; s != early {
		t.Error("another session confirmed")
	}

	if l, rm, _ := early.Negotiated(); string(l) != "offer-2" || string(rm) != "answer-2" {
		t.Errorf("negotiated %q %q", l, rm)
	}
}

// RFC 4028 §7.3: a re-INVITE refused with a 422 is sent again with the Min-SE, its Session-Expires raised to it.
func TestReinviteIntervalTooSmall(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeer(t, "bob", nil, ua.SessionTimer{MinSE: 1800 * time.Second})
	as, _ := established(t, alice, bob)

	done, f := results()
	c := ua.Change{Header: sip.Header{{Name: "Session-Expires", Value: "100"}}, Bodies: sdpOf("offer-2")}

	if err := as.Reinvite(c, f); err != nil {
		t.Fatal(err)
	}

	r := bob.next("request").req
	if se, mse := r.Message().Header.Get("Session-Expires"), r.Message().Header.Get("Min-SE"); se != "1800" || mse != "1800" {
		t.Errorf("re-INVITE Session-Expires %q, Min-SE %q", se, mse)
	}

	_ = r.Accept(sdpOf("answer-2")...)

	alice.next("answer")

	if res := recv(t, done); res.code() != 200 {
		t.Fatalf("re-INVITE %d", res.code())
	}
}

// RFC 3261 §13.2.2.4: Handler.Forked keeps the session of a second 2xx, confirmed.
func TestForkedKept(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	fork := netOf(t).Peer()

	uri, _ := sip.ParseURI("sip:bob@" + fork.Addr().String())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+uri.String()+">")
	req.SetBodies(ua.SDP([]byte("offer-1")))

	h := alice.handler()
	forked := make(chan *ua.Session, 1)
	h.Forked = func(s *ua.Session, res *sip.Response) bool {
		forked <- s
		return true
	}

	if _, err := alice.a.Invite(req, h); err != nil {
		t.Fatal(err)
	}

	inv, f := fork.RecvRequest()

	respond := func(tag, sdp string) {
		res := sip.NewResponse(inv, 200, "")
		_ = res.Header.SetToTag(tag)
		res.Header.Add("Contact", "<sip:"+tag+"@"+fork.Addr().String()+">")
		res.SetBodies(ua.SDP([]byte(sdp)))
		fork.Send(sip.UDP, f.Remote, res)
	}

	respond("one", "answer-1")

	one := alice.next("established").s
	alice.next("answer")

	if ack, _ := fork.RecvRequest(); ack.Method != "ACK" {
		t.Fatalf("got %s", ack.Method)
	}

	respond("two", "answer-2")

	two := alice.next("established").s
	if e := alice.next("answer"); e.s != two || string(e.sdp) != "answer-2" {
		t.Errorf("answer %+v", e)
	}

	if s := <-forked; s != two || two == one {
		t.Error("Forked asked about another session")
	}

	if ack, _ := fork.RecvRequest(); ack.Method != "ACK" || !strings.Contains(ack.Header.Get("To"), "two") {
		t.Fatalf("got %s, want the ACK of two", ack.Method)
	}

	fork.RecvNone(0)

	if two.State() != ua.Confirmed || one.State() != ua.Confirmed {
		t.Errorf("states %s %s", one.State(), two.State())
	}
}

// A call over TCP: INVITE, re-INVITE, BYE.
func TestCallOverTCP(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	alice.tr, bob.tr = sip.TCP, sip.TCP

	as, bs := established(t, alice, bob)

	done, f := results()

	if err := bs.Reinvite(ua.Change{Bodies: sdpOf("offer-2")}, f); err != nil {
		t.Fatal(err)
	}

	r := alice.next("request").req
	if r.Message().Flow.Transport != sip.TCP {
		t.Errorf("re-INVITE over %s", r.Message().Flow.Transport)
	}

	_ = r.Accept(sdpOf("answer-2")...)

	bob.next("answer")

	if res := recv(t, done); res.code() != 200 {
		t.Fatalf("re-INVITE %d", res.code())
	}

	_ = as.Bye(ua.Change{}, nil)

	alice.next("ended")

	if e := bob.next("ended"); e.end.Cause != ua.EndBye || !e.end.Remote {
		t.Errorf("bob ended %+v", e.end)
	}
}

// forkedInvite sends an INVITE of alice to a raw socket standing for the callees behind a forking proxy.
func forkedInvite(t *testing.T, alice *peer, h ua.Handler) (*ua.Outgoing, *siptest.Peer, *sip.Request, func(code int, tag string, header ...sip.Field)) {
	t.Helper()

	fork := netOf(t).Peer()

	uri, _ := sip.ParseURI("sip:bob@" + fork.Addr().String())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+uri.String()+">")
	req.SetBodies(ua.SDP([]byte(realSDP(4000, 1))))

	o, err := alice.a.Invite(req, h)
	if err != nil {
		t.Fatal(err)
	}

	inv, f := fork.RecvRequest()

	respond := func(code int, tag string, header ...sip.Field) {
		res := sip.NewResponse(inv, code, "")
		_ = res.Header.SetToTag(tag)
		res.Header.Add("Contact", "<sip:"+tag+"@"+fork.Addr().String()+">")

		for _, f := range header {
			res.Header.Add(f.Name, f.Value)
		}

		if code >= 200 && code < 300 {
			res.SetBodies(ua.SDP([]byte(realSDP(5000, 1))))
		}

		fork.Send(sip.UDP, f.Remote, res)
	}

	return o, fork, inv, respond
}

// RFC 6228: a 199 ends the early dialog it names, PRACKed when reliable, even of a dialog not seen before; the INVITE
// goes on.
func TestEarlyDialogTerminated(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	_, fork, inv, respond := forkedInvite(t, alice, alice.handler())

	if !inv.Header.HasOptionTag("Supported", "199") {
		t.Errorf("INVITE Supported %q", inv.Header.Values("Supported"))
	}

	respond(180, "one")

	one := alice.next("progress").s

	respond(199, "one")

	if e := alice.next("progress"); e.res.StatusCode != 199 || e.s != one {
		t.Errorf("progress %+v", e)
	}

	if e := alice.next("ended"); e.s != one || e.end.Cause != ua.EndNotChosen || e.end.Response.StatusCode != 199 {
		t.Errorf("ended %+v", e.end)
	}

	respond(199, "two", sip.Field{Name: "Require", Value: "100rel"}, sip.Field{Name: "RSeq", Value: "1"})

	if prack, _ := fork.RecvRequest(); prack.Method != "PRACK" || !strings.Contains(prack.Header.Get("To"), "two") {
		t.Fatalf("got %s, want the PRACK of the reliable 199", prack.Method)
	}

	alice.nextAll("progress", "ended")

	respond(200, "three")

	if s := alice.next("established").s; s == one {
		t.Error("the ended dialog confirmed")
	}
}

// RFC 3261 §9.1: a 2xx crossing the CANCEL is acknowledged, and the session ended with a BYE carrying the Reason of the
// CANCEL.
func TestAnswerCrossingCancel(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	o, fork, _, respond := forkedInvite(t, alice, alice.handler())

	respond(180, "one")
	alice.next("progress")

	reason, _ := sip.NewReason("RELEASE_CAUSE", 1, "User ends call")

	if err := o.Cancel(ua.Reasons(reason)); err != nil {
		t.Fatal(err)
	}

	if cancel, _ := fork.RecvRequest(); cancel.Method != "CANCEL" {
		t.Fatalf("got %s", cancel.Method)
	}

	respond(200, "one")

	var methods []string

	for len(methods) < 2 {
		m := fork.Recv()
		if req, ok := m.Msg.(*sip.Request); ok {
			methods = append(methods, req.Method)

			if req.Method == "BYE" && req.Header.Get("Reason") != reason.String() {
				t.Errorf("BYE Reason %q", req.Header.Get("Reason"))
			}
		}
	}

	if strings.Join(methods, ",") != "ACK,BYE" {
		t.Errorf("got %v, want the ACK, then the BYE", methods)
	}

	for {
		e := <-alice.events
		if e.kind == "ended" {
			if e.end.Cause != ua.EndCancelled {
				t.Errorf("ended %+v", e.end)
			}

			break
		}
	}
}

// RFC 3261 §12.2.1.2: a 481 to the PRACK loses that early dialog only.
func TestPrackLosesEarlyDialog(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	_, fork, _, respond := forkedInvite(t, alice, alice.handler())

	respond(183, "one", sip.Field{Name: "Require", Value: "100rel"}, sip.Field{Name: "RSeq", Value: "1"})

	prack, f := fork.RecvRequest()
	fork.Send(sip.UDP, f.Remote, sip.NewResponse(prack, 481, ""))

	// The 183 is not reported: its dialog is lost before its PRACK completed.
	if e := alice.next("ended"); e.end.Cause != ua.EndLost {
		t.Errorf("ended %+v", e.end)
	}

	respond(200, "two")
	alice.next("established")
}

// A re-INVITE the peer leaves unanswered once it ended the session completes with ErrState.
func TestReinviteDoneWhenSessionEnds(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	r := newRaw(t)

	invite := r.invite(bob, realSDP(4000, 1))
	in := bob.next("invite").req
	_ = in.Accept(ua.SDP([]byte(realSDP(5000, 1))))
	ok := r.response()
	r.ack(invite, ok)

	bs := bob.next("established").s

	done, f := results()

	if err := bs.Reinvite(ua.Change{Bodies: sdpOf(realSDP(5002, 2))}, f); err != nil {
		t.Fatal(err)
	}

	for {
		req, f := r.sock.RecvRequest()
		if req.Method == "INVITE" {
			r.sock.Send(sip.UDP, f.Remote, sip.NewResponse(req, 100, ""))
			break
		}
	}

	r.sock.Send(sip.UDP, bob.addr, r.inDialog("BYE", invite, ok, 2))

	if res := recv(t, done); !errors.Is(res.err, ua.ErrState) {
		t.Errorf("re-INVITE %+v, want ErrState", res)
	}

	if e := bob.next("ended"); e.end.Cause != ua.EndBye || !e.end.Remote {
		t.Errorf("ended %+v", e.end)
	}
}
