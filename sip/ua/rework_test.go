package ua_test

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

// realSDP is an SDP description on loopback, of the origin version given.
func realSDP(port, version int) string {
	return fmt.Sprintf("v=0\r\no=- 7 %d IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio %d RTP/AVP 0\r\n", version, port)
}

// raw is a peer that sends and receives messages itself.
type raw struct {
	t    *testing.T
	sock *siptest.Socket
}

func newRaw(t *testing.T) *raw {
	return &raw{t: t, sock: siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))}
}

func (r *raw) invite(to *peer, body string, header ...sip.Field) *sip.Request {
	req := siptest.NewRequest("INVITE", to.uri(), sip.UDP, r.sock.Addr())

	for _, f := range header {
		req.Header.Add(f.Name, f.Value)
	}

	if body != "" {
		req.SetBodies(ua.SDP([]byte(body)))
	}

	r.sock.Send(sip.UDP, to.addr, req)

	return req
}

// response is the next response other than 100.
func (r *raw) response() *sip.Response {
	r.t.Helper()

	for {
		res, _ := r.sock.RecvResponse()
		if res.StatusCode != 100 {
			return res
		}
	}
}

// inDialog builds a request of the raw peer within the dialog a response created.
func (r *raw) inDialog(method string, invite *sip.Request, res *sip.Response, seq uint32) *sip.Request {
	contact, _ := res.Header.Contacts()
	req := siptest.NewRequest(method, contact[0].URI.String(), sip.UDP, r.sock.Addr())
	req.Header.Set("From", invite.Header.Get("From"))
	req.Header.Set("To", res.Header.Get("To"))
	req.Header.Set("Call-ID", invite.Header.CallID())
	req.Header.Set("CSeq", sip.CSeq{Seq: seq, Method: method}.String())

	return req
}

func (r *raw) ack(invite *sip.Request, res *sip.Response) {
	ack := r.inDialog("ACK", invite, res, 1)

	contact, _ := res.Header.Contacts()
	r.sock.Send(sip.UDP, netip.AddrPortFrom(loopback, contact[0].URI.Port), ack)
}

// RFC 3261 §12.1.1: every response that creates or confirms the dialog carries the Record-Route of the INVITE.
func TestRecordRouteInEachResponse(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	r := newRaw(t)

	r.invite(bob, realSDP(4000, 1), sip.Field{Name: "Record-Route", Value: "<sip:proxy.example;lr>"})

	in := bob.next("invite").req
	_ = in.Progress(ua.Reply{Code: 180})
	_ = in.Accept(ua.SDP([]byte(realSDP(5000, 1))))

	for _, code := range []int{180, 200} {
		res := r.response()
		if res.StatusCode != code || res.Header.Get("Record-Route") != "<sip:proxy.example;lr>" {
			t.Errorf("%d with Record-Route %q", res.StatusCode, res.Header.Get("Record-Route"))
		}
	}
}

// RFC 3261 §15.1.2, RFC 3262 §3: a BYE in the early dialog ends the session; the 2xx held for its PRACK gives way to a
// 487.
func TestByeWhile2xxHeld(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	r := newRaw(t)

	invite := r.invite(bob, realSDP(4000, 1), sip.Field{Name: "Supported", Value: "100rel"})

	in := bob.next("invite").req
	_ = in.Progress(ua.Reply{Code: 183, Reliable: true, Bodies: sdpOf(realSDP(5000, 1))})
	_ = in.Accept()

	early := r.response()
	r.sock.Send(sip.UDP, bob.addr, r.inDialog("BYE", invite, early, 2))

	var codes []string

	for len(codes) < 2 {
		res := r.response()
		if res.StatusCode == 183 {
			continue
		}

		cseq, _ := res.Header.CSeq()
		codes = append(codes, fmt.Sprint(cseq.Method, " ", res.StatusCode))
	}

	if !strings.Contains(strings.Join(codes, ","), "BYE 200") || !strings.Contains(strings.Join(codes, ","), "INVITE 487") {
		t.Errorf("responses %v, want 200 to the BYE and 487 to the INVITE", codes)
	}

	if e := bob.next("ended"); e.end.Cause != ua.EndBye || !e.end.Remote {
		t.Errorf("ended %+v", e.end)
	}
}

// Close ends the sessions of the agent: the peer gets a BYE.
func TestClose(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	established(t, alice, bob)

	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	if err := alice.a.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if e := alice.next("ended"); e.end.Cause != ua.EndClosed {
		t.Errorf("alice ended %+v", e.end)
	}

	if e := bob.next("ended"); e.end.Cause != ua.EndBye || !e.end.Remote {
		t.Errorf("bob ended %+v", e.end)
	}
}

// RFC 3261 §13.2.2.4, RFC 3264 §6: an offer of a 2xx refused is answered with its streams refused, then the BYE.
func TestRejectOfferInTheAck(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	alice.invite(bob, "")

	_ = bob.next("invite").req.Accept(ua.SDP([]byte(realSDP(5000, 1))))
	alice.next("established")

	alice.next("offer").offer.Reject()

	answer := bob.next("answer")

	s, err := sdp.Parse(answer.sdp)
	if err != nil || len(s.Media) != 1 || s.Media[0].Port() != 0 {
		t.Errorf("answer in the ACK %q, want the stream refused", answer.sdp)
	}

	bob.next("established")

	if e := bob.next("ended"); e.end.Cause != ua.EndBye || !e.end.Remote {
		t.Errorf("bob ended %+v", e.end)
	}
}

// RFC 3264 §4: an ACK without the answer to the offer of the 2xx ends the session.
func TestAckWithoutAnswer(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	r := newRaw(t)

	invite := r.invite(bob, "")
	_ = bob.next("invite").req.Accept(ua.SDP([]byte(realSDP(5000, 1))))

	ok := r.response()
	r.ack(invite, ok)

	bob.next("established")

	if e := bob.next("ended"); e.end.Cause != ua.EndLost {
		t.Errorf("ended %+v", e.end)
	}

	if req, _ := r.sock.RecvRequest(); req.Method != "BYE" {
		t.Errorf("got %s, want the BYE", req.Method)
	}
}

// RFC 3261 §15: the BYE of a UAS waits for the ACK of its 2xx.
func TestUasByeBeforeAck(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	r := newRaw(t)

	invite := r.invite(bob, realSDP(4000, 1))
	in := bob.next("invite").req
	_ = in.Accept(ua.SDP([]byte(realSDP(5000, 1))))

	ok := r.response()

	if err := in.Session().Bye(); err != nil {
		t.Fatal(err)
	}

	r.sock.RecvNone(100 * time.Millisecond)
	r.ack(invite, ok)

	if req, _ := r.sock.RecvRequest(); req.Method != "BYE" {
		t.Errorf("got %s, want the BYE", req.Method)
	}

	bob.next("established")

	if e := bob.next("ended"); e.end.Cause != ua.EndBye || e.end.Remote {
		t.Errorf("ended %+v", e.end)
	}
}

// RFC 3261 §8.2.2.2: the same request reaching the agent on another path gets a 482.
func TestMergedRequest(t *testing.T) {
	bob := newPeer(t, "bob", nil, ua.SessionTimer{})
	r := newRaw(t)

	invite := r.invite(bob, realSDP(4000, 1))
	bob.next("invite")

	again := invite.Clone()
	via, _ := again.Header.TopVia()
	via.Params.Set("branch", sip.NewBranch())
	_ = again.Header.SetTopVia(via)
	r.sock.Send(sip.UDP, bob.addr, again)

	if res := r.response(); res.StatusCode != 482 {
		t.Errorf("got %d, want 482", res.StatusCode)
	}
}

// The session keeps its Contact: the one of its 2xx goes in the requests that refresh its target.
func TestSessionContact(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	focus := fmt.Sprintf("<sip:focus@%s>;isfocus", bob.addr)

	alice.invite(bob, realSDP(4000, 1))

	in := bob.next("invite").req
	_ = in.Respond(ua.Reply{Code: 200, Header: sip.Header{{Name: "Contact", Value: focus}}, Bodies: sdpOf(realSDP(5000, 1))})

	as := alice.next("established").s
	alice.next("answer")
	bs := bob.next("established").s

	done := make(chan *sip.Response, 1)

	if err := bs.Update(ua.Change{}, func(res *sip.Response, err error) { done <- res }); err != nil {
		t.Fatal(err)
	}

	<-done

	d, _ := as.Dialog()
	if !strings.HasPrefix(d.Target, "sip:focus@") {
		t.Errorf("alice's remote target %s, want the Contact of bob's session", d.Target)
	}
}

// RFC 3264 §8, RFC 4028 §7.4: refreshes are answered by the agent: an unchanged offer with the last answer, a
// re-INVITE without an offer with the last description.
func TestRefreshAnsweredByTheAgent(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	offer, answer := realSDP(4000, 1), realSDP(5000, 1)

	alice.invite(bob, offer)
	_ = bob.next("invite").req.Accept(ua.SDP([]byte(answer)))

	as := alice.next("established").s
	alice.next("answer")
	bob.next("established")

	done := make(chan *sip.Response, 1)

	if err := as.Reinvite(ua.Change{Bodies: sdpOf(offer)}, func(res *sip.Response, err error) { done <- res }); err != nil {
		t.Fatal(err)
	}

	if res := <-done; res.StatusCode != 200 || string(sessionBody(res)) != answer {
		t.Fatalf("re-INVITE: %d with %q", res.StatusCode, sessionBody(res))
	}

	alice.next("answer")

	if err := as.Reinvite(ua.Change{}, func(res *sip.Response, err error) { done <- res }); err != nil {
		t.Fatal(err)
	}

	o := alice.next("offer")
	if string(o.sdp) != answer {
		t.Fatalf("offer %q, want bob's last description", o.sdp)
	}

	_ = o.offer.Answer(ua.SDP([]byte(offer)))

	<-done

	// Bob gets alice's answer in the ACK; the application saw no request.
	if e := bob.next("answer"); string(e.sdp) != offer {
		t.Errorf("bob answer %q", e.sdp)
	}

	bob.none()
}

func sessionBody(res *sip.Response) []byte {
	p, _, _ := res.Content().FindFunc(sip.BodyPart.IsSession)
	return p.Body
}

// RFC 3261 §22.2: a challenged INVITE is sent again with credentials and the next CSeq.
func TestAuthentication(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})

	_ = alice.a.Close(context.Background())
	alice = newPeerWith(t, "alice", func(c *ua.Config) {
		c.Authenticate = func(req *sip.Request, challenge *sip.Response) error {
			req.Header.Set("Authorization", `Digest username="alice", response="ok"`)
			return nil
		}
	})

	alice.invite(bob, realSDP(4000, 1))

	first := bob.next("invite").req
	_ = first.Reject(401, sip.Field{Name: "WWW-Authenticate", Value: `Digest realm="x", nonce="n"`})

	bob.next("ended")

	second := bob.next("invite").req
	if cseq, _ := second.Message().Header.CSeq(); cseq.Seq != 2 || !second.Message().Header.Has("Authorization") {
		t.Fatalf("second INVITE CSeq %d, Authorization %q", cseq.Seq, second.Message().Header.Get("Authorization"))
	}

	_ = second.Accept(ua.SDP([]byte(realSDP(5000, 1))))

	alice.next("established")
}

// Requests outside dialogs: sent with Agent.Request, received with OnRequest.
func TestRequestOutsideDialogs(t *testing.T) {
	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) {
		c.OnRequest = func(r *ua.Request) { _ = r.Respond(ua.Reply{Code: 200}) }
	})

	uri, _ := sip.ParseURI(bob.uri())
	req := sip.NewRequest("OPTIONS", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+bob.uri()+">")

	type result struct {
		res *sip.Response
		err error
	}

	done := make(chan result, 1)

	if err := alice.a.Request(req, func(res *sip.Response, err error) { done <- result{res, err} }); err != nil {
		t.Fatal(err)
	}

	select {
	case r := <-done:
		if r.err != nil || r.res.StatusCode != 200 {
			t.Errorf("OPTIONS %v, %v", r.res, r.err)
		}
	case <-time.After(wait):
		t.Fatal("no response")
	}
}

// RFC 3261 §8.2.2.3: Require is checked on requests within dialogs too, and every unsupported tag listed.
func TestRequireInDialog(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	as, _ := established(t, alice, bob)

	done := make(chan *sip.Response, 1)
	_ = as.Send("INFO", ua.Change{Header: sip.Header{{Name: "Require", Value: "foo"}}}, func(res *sip.Response, err error) {
		done <- res
	})

	if res := <-done; res.StatusCode != 420 || res.Header.Get("Unsupported") != "foo" {
		t.Errorf("INFO %d, Unsupported %q", res.StatusCode, res.Header.Get("Unsupported"))
	}

	bob.none()
}

// A Change keeps its repeated header fields.
func TestChangeRepeatedHeaderFields(t *testing.T) {
	alice, bob := newPeer(t, "alice", nil, ua.SessionTimer{}), newPeer(t, "bob", nil, ua.SessionTimer{})
	as, _ := established(t, alice, bob)

	c := ua.Change{
		Header: sip.Header{{Name: "Accept-Contact", Value: "*;+a"}, {Name: "Accept-Contact", Value: "*;+b"}},
		Bodies: sdpOf("offer-2"),
	}

	_ = as.Reinvite(c, nil)

	r := bob.next("request").req
	if n := r.Message().Header.Count("Accept-Contact"); n != 2 {
		t.Errorf("%d Accept-Contact", n)
	}

	_ = r.Accept(sdpOf("answer-2")...)
}
