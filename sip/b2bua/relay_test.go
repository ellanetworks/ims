package b2bua_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/b2bua"
	"github.com/ellanetworks/ims/sip/ua"
)

// bridged sets up a call from alice to bob through b, and returns the sessions of alice and bob.
func bridged(t *testing.T, alice, bob *party) (as, bs *ua.Session) {
	t.Helper()

	in := bob.next("invite").req
	_ = in.Accept(sdpOf("answer-1")...)

	as = alice.next("established").s
	alice.next("answer")

	return as, bob.next("established").s
}

func newCall(t *testing.T, cfg b2bua.RelayConfig) (alice, bob *party, b *b2b) {
	n := newNet(t)
	bob = newParty(t, n, "bob", nil)
	b = newB2B(t, n, func() *party { return bob }, cfg)
	alice = newParty(t, n, "alice", nil)

	return alice, bob, b
}

// A re-INVITE of either side reaches the other with its offer, and the answer comes back; a 491 of the other side
// goes back as it is (RFC 3261 §14.1).
func TestRelayReinvite(t *testing.T) {
	alice, bob, b := newCall(t, b2bua.RelayConfig{})
	alice.invite(b.uri(), "offer-1", alice.handler())
	as, bs := bridged(t, alice, bob)

	type result struct {
		res *sip.Response
		err error
	}

	done := make(chan result, 4)
	f := func(res *sip.Response, err error) { done <- result{res, err} }
	next := func() result {
		t.Helper()

		select {
		case r := <-done:
			return r
		case <-time.After(wait):
			t.Fatal("no result")
			return result{}
		}
	}

	if err := as.Reinvite(ua.Change{Bodies: sdpOf("offer-2")}, f); err != nil {
		t.Fatal(err)
	}

	r := bob.next("request").req
	if offer, _ := r.Offer(); string(offer) != "offer-2" {
		t.Fatalf("bob got %q", offer)
	}

	_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf("answer-2")})

	alice.next("answer")

	if res := next().res; res == nil || res.StatusCode != 200 || !hasSDP(res, "answer-2") {
		t.Fatalf("alice's re-INVITE: %d", res.StatusCode)
	}

	// From bob, refused by alice with a 491; bob, retrying no glare, gets it.
	pol := bs.Policy()
	pol.RetryGlare = false
	bs.SetPolicy(pol)

	if err := bs.Reinvite(ua.Change{Bodies: sdpOf("offer-3")}, f); err != nil {
		t.Fatal(err)
	}

	r = alice.next("request").req
	_ = r.Reject(491)

	if r := next(); r.res == nil || r.res.StatusCode != 491 {
		t.Fatalf("bob's re-INVITE: %+v %v", r.res, r.err)
	}
}

// RFC 4028 §7.4: a refresh is answered on its own leg; RFC 3261 §14.2: a re-INVITE without an offer that asks for
// more gets the offer of the other side, fetched with a re-INVITE without one; the answer in the ACK goes back in the
// ACK.
func TestRelayReinviteWithoutOffer(t *testing.T) {
	alice, bob, b := newCall(t, b2bua.RelayConfig{})
	alice.invite(b.uri(), "offer-1", alice.handler())
	as, _ := bridged(t, alice, bob)

	done := make(chan *sip.Response, 2)
	f := func(res *sip.Response, _ error) { done <- res }

	// A refresh is answered on alice's leg.
	if err := as.Reinvite(ua.Change{}, f); err != nil {
		t.Fatal(err)
	}

	if res := <-done; res.StatusCode != 200 || !hasSDP(res, "answer-1") {
		t.Fatalf("refresh %d %q", res.StatusCode, res.Body)
	}

	// Its 2xx offers the session as it was, which alice answers in the ACK.
	_ = alice.next("offer").offer.Answer(ua.Change{Bodies: sdpOf("offer-1")})

	bob.none()

	// One asking for more goes across, and fetches the offer of bob.
	info := sip.NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte("<mcpttinfo/>"))

	if err := as.Reinvite(ua.Change{Bodies: []sip.BodyPart{info}}, f); err != nil {
		t.Fatal(err)
	}

	r := bob.next("request").req
	if _, ok := r.Offer(); ok {
		t.Fatal("bob's re-INVITE with an offer")
	}

	_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf("offer-b")})

	o := alice.next("offer")
	if string(o.sdp) != "offer-b" {
		t.Fatalf("alice got offer %q", o.sdp)
	}

	_ = o.offer.Answer(ua.Change{Bodies: sdpOf("answer-a")})

	if e := bob.next("answer"); string(e.sdp) != "answer-a" {
		t.Fatalf("bob got answer %q", e.sdp)
	}

	if res := <-done; res.StatusCode != 200 {
		t.Fatalf("alice's re-INVITE: %d", res.StatusCode)
	}
}

// An UPDATE and an INFO go across with their bodies, byte-exact, and their responses come back; a refresh is
// answered on its own leg.
func TestRelayRequests(t *testing.T) {
	alice, bob, b := newCall(t, b2bua.RelayConfig{})
	alice.invite(b.uri(), "offer-1", alice.handler())
	as, _ := bridged(t, alice, bob)

	done := make(chan *sip.Response, 2)
	f := func(res *sip.Response, _ error) { done <- res }

	if err := as.Update(ua.Change{Bodies: sdpOf("offer-2")}, f); err != nil {
		t.Fatal(err)
	}

	r := bob.next("request").req
	if r.Message().Method != "UPDATE" || !hasSDP(r.Message(), "offer-2") {
		t.Fatalf("bob got %s", r.Message().Method)
	}

	_ = r.Respond(ua.Reply{Code: 200, Bodies: sdpOf("answer-2")})

	alice.next("answer")

	if res := <-done; res.StatusCode != 200 {
		t.Fatalf("UPDATE %d", res.StatusCode)
	}

	info := sip.NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte("<mcpttinfo>\r\n  <x/>\r\n</mcpttinfo>"))

	if err := as.Send("INFO", ua.Change{Bodies: []sip.BodyPart{info}}, f); err != nil {
		t.Fatal(err)
	}

	r = bob.next("request").req
	if r.Message().Method != "INFO" || !bytes.Equal(r.Message().Body, info.Body) {
		t.Fatalf("bob got %s %q", r.Message().Method, r.Message().Body)
	}

	_ = r.Respond(ua.Reply{Code: 202})

	if res := <-done; res.StatusCode != 202 {
		t.Fatalf("INFO %d", res.StatusCode)
	}
}

// The parts of an INVITE other than its session description cross byte-exact (TS 24.379 §6.3.2.1.5); Media makes the
// session descriptions of each leg.
func TestBridgeBodiesAndMedia(t *testing.T) {
	alice, bob, b := newCall(t, b2bua.RelayConfig{Media: anchor{}})

	info := sip.NewBodyPart("application/vnd.3gpp.mcptt-info+xml", []byte("<mcpttinfo> exact </mcpttinfo>"))

	uri, _ := sip.ParseURI(b.uri())
	req := sip.NewRequest("INVITE", uri)
	req.Header.Add("From", "<"+alice.uri()+">")
	req.Header.Add("To", "<"+b.uri()+">")
	req.SetBodies(ua.SDP([]byte("offer-1")), info)

	if _, err := alice.a.Invite(req, alice.handler()); err != nil {
		t.Fatal(err)
	}

	in := bob.next("invite").req

	parts, _ := in.Message().Content().Parts()
	if len(parts) != 2 || string(parts[0].Body) != "anchored offer-1" || !bytes.Equal(parts[1].Body, info.Body) {
		t.Fatalf("bob's INVITE parts %+v", parts)
	}

	_ = in.Accept(sdpOf("answer-1")...)

	if e := alice.next("established"); !hasSDP(e.msg, "anchored answer-1") {
		t.Errorf("alice's 200 %q", e.msg.Env().Body)
	}
}

// anchor is the Media of a B2BUA anchoring the media: it marks the descriptions it makes.
type anchor struct{}

func (anchor) Offer(x *b2bua.Exchange) ([]byte, error) {
	return append([]byte("anchored "), x.Offer...), nil
}

func (anchor) Answer(_ *b2bua.Exchange, _ *ua.Session, sdp []byte) ([]byte, error) {
	return append([]byte("anchored "), sdp...), nil
}

func (anchor) End(*b2bua.Exchange, *ua.Session) {}

// RFC 3261 §15: a 2xx of bob crossing the CANCEL of alice is acknowledged, and his session ended with a BYE.
func TestBridge2xxCrossingCancel(t *testing.T) {
	n := newNet(t)
	bob := n.Peer()

	b := newB2B(t, n, func() *party { return &party{name: "bob", addr: bob.Addr()} }, b2bua.RelayConfig{})
	alice := newParty(t, n, "alice", nil)

	o := alice.invite(b.uri(), "offer-1", alice.handler())

	invite, _ := bob.RecvRequest()
	tag := sip.NewTag()

	respond := func(code int, sdp string) {
		res := sip.NewResponse(invite, code, "")
		_ = res.Header.SetToTag(tag)
		res.Header.Set("Contact", "<sip:bob@"+bob.Addr().String()+">")

		if sdp != "" {
			res.SetBodies(ua.SDP([]byte(sdp)))
		}

		bob.Send(sip.UDP, invite.Flow.Remote, res)
	}

	respond(180, "")
	alice.next("progress")

	_ = o.Cancel(ua.Change{})

	cancel, _ := bob.RecvRequest()
	if cancel.Method != "CANCEL" {
		t.Fatalf("bob got %s", cancel.Method)
	}

	bob.Send(sip.UDP, invite.Flow.Remote, sip.NewResponse(cancel, 200, ""))
	respond(200, "answer-1")

	for _, method := range []string{"ACK", "BYE"} {
		req, _ := bob.RecvRequest()
		for req.Method == "CANCEL" {
			req, _ = bob.RecvRequest()
		}

		if req.Method != method {
			t.Fatalf("bob got %s, want %s", req.Method, method)
		}

		if method == "BYE" {
			bob.Send(sip.UDP, invite.Flow.Remote, sip.NewResponse(req, 200, ""))
		}
	}

	alice.next("failed")
	b.callEnded(t)
	settled(t, n, alice, b.party)
}
