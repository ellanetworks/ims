package testue

import (
	"strconv"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
	"github.com/ellanetworks/ims/sip/sdp"
)

// callee is one UAS behind a forking proxy, answering the INVITE in its own dialog.
type callee struct {
	t      *testing.T
	p      *peer
	invite *sip.Request
	tag    string
	m      *media
	rseq   uint32
}

func (p *peer) callee(invite *sip.Request, tag string, port uint16) *callee {
	return &callee{t: p.t, p: p, invite: invite, tag: tag, m: newMedia(loopback, port, false), rseq: 1}
}

func (c *callee) response(code int, reliable, withSDP bool) {
	c.t.Helper()

	res := sip.NewResponse(c.invite, code, "")
	setToTag(c.t, &res.Header, c.tag)
	res.Header.Add("Contact", "<sip:"+c.tag+"@"+sip.FormatHost(loopback)+">")

	if reliable {
		res.Header.Add("Require", "100rel")
		res.Header.Add("RSeq", itoa(c.rseq))
		c.rseq++
	}

	if withSDP {
		offer, err := sdp.Parse(c.invite.Body)
		if err != nil {
			c.t.Fatal(err)
		}

		answer, err := c.m.answer(offer)
		if err != nil {
			c.t.Fatal(err)
		}

		res.SetBody(sdp.ContentType, answer.Bytes())
	}

	c.p.send(res)
}

func itoa(n uint32) string {
	return strconv.FormatUint(uint64(n), 10)
}

func sessionID(t *testing.T, s *sdp.Session) string {
	t.Helper()

	if s == nil {
		return ""
	}

	o, err := s.Origin()
	if err != nil {
		t.Fatal(err)
	}

	return o.SessionID
}

func toTag(t *testing.T, req *sip.Request) string {
	t.Helper()

	to, err := req.Header.To()
	if err != nil {
		t.Fatal(err)
	}

	return to.Tag()
}

func forkedInvite(t *testing.T) (*peer, *Call, *sip.Request) {
	t.Helper()

	p := newPeer(t, Config{})

	c, err := p.u.Invite("tel:+15550002", CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	invite := p.request("INVITE")
	p.send(sip.NewResponse(invite, 100, ""))

	return p, c, invite
}

func earlyTags(c *Call) []string {
	var out []string

	for _, id := range c.EarlyDialogs() {
		out = append(out, id.RemoteTag)
	}

	return out
}

// RFC 3261 §13.2.2.4
func TestForkedCallAnsweredBySecondCallee(t *testing.T) {
	ctx := testContext(t)
	p, c, invite := forkedInvite(t)

	a := p.callee(invite, "callee-a", 41000)
	b := p.callee(invite, "callee-b", 41002)

	a.response(180, false, false)
	b.response(180, false, false)

	eventually(t, "two early dialogs", func() bool { return len(c.EarlyDialogs()) == 2 })

	b.response(200, false, true)

	res, err := c.Wait(ctx)
	if err != nil || dialog.ResponseID(res).RemoteTag != b.tag {
		t.Fatalf("Wait = %v, %v; want the 200 from %s", res, err, b.tag)
	}

	if ack := p.request("ACK"); toTag(t, ack) != b.tag {
		t.Errorf("ACK to %s, want %s", toTag(t, ack), b.tag)
	}

	if got := c.ID().RemoteTag; got != b.tag {
		t.Errorf("call dialog with %s, want %s", got, b.tag)
	}

	if got := earlyTags(c); len(got) != 0 {
		t.Errorf("early dialogs %v after the answer", got)
	}

	if got, want := sessionID(t, c.RemoteSDP()), sessionID(t, b.m.local); got != want {
		t.Errorf("remote description %s, want %s from %s", got, want, b.tag)
	}

	// A 2xx from the other callee confirms a second dialog that the caller acknowledges and ends.
	a.response(200, false, true)

	if ack := p.request("ACK"); toTag(t, ack) != a.tag {
		t.Errorf("ACK to %s, want %s", toTag(t, ack), a.tag)
	}

	bye := p.request("BYE")
	if toTag(t, bye) != a.tag {
		t.Fatalf("BYE to %s, want %s", toTag(t, bye), a.tag)
	}

	p.send(sip.NewResponse(bye, 200, ""))

	if c.State() != CallConfirmed || c.ID().RemoteTag != b.tag {
		t.Fatalf("call %s with %s after the second 2xx", c.State(), c.ID().RemoteTag)
	}

	// The retransmitted 2xx of the kept dialog is acknowledged again, still to that dialog.
	b.response(200, false, true)

	if ack := p.request("ACK"); toTag(t, ack) != b.tag {
		t.Errorf("ACK to the retransmission to %s, want %s", toTag(t, ack), b.tag)
	}
}

// RFC 3262 §4: each early dialog has its own RSeq space and gets its own PRACK.
func TestForkedReliableProvisionals(t *testing.T) {
	ctx := testContext(t)
	p, c, invite := forkedInvite(t)

	a := p.callee(invite, "callee-a", 41000)
	b := p.callee(invite, "callee-b", 41002)

	a.rseq, b.rseq = 7, 900

	for _, callee := range []*callee{a, b} {
		callee.response(183, true, true)

		prack := p.request("PRACK")
		if got := toTag(t, prack); got != callee.tag {
			t.Fatalf("PRACK to %s, want %s", got, callee.tag)
		}

		if got, want := prack.Header.Get("RAck"), itoa(callee.rseq-1)+" 1 INVITE"; got != want {
			t.Errorf("RAck %q, want %q", got, want)
		}

		p.send(sip.NewResponse(prack, 200, ""))
	}

	b.response(180, true, false)

	if prack := p.request("PRACK"); prack.Header.Get("RAck") != "901 1 INVITE" {
		t.Errorf("RAck %q for the second reliable response of %s", prack.Header.Get("RAck"), b.tag)
	} else {
		p.send(sip.NewResponse(prack, 200, ""))
	}

	a.response(200, false, false)

	if _, err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	p.request("ACK")

	if got, want := sessionID(t, c.RemoteSDP()), sessionID(t, a.m.local); got != want {
		t.Errorf("remote description %s, want the early answer %s from %s", got, want, a.tag)
	}
}

// RFC 6228 §4
func TestEarlyDialogTerminated(t *testing.T) {
	ctx := testContext(t)
	p, c, invite := forkedInvite(t)

	a := p.callee(invite, "callee-a", 41000)
	b := p.callee(invite, "callee-b", 41002)

	a.response(180, false, false)
	b.response(180, false, false)

	eventually(t, "two early dialogs", func() bool { return len(c.EarlyDialogs()) == 2 })

	a.response(199, false, false)

	eventually(t, "one early dialog", func() bool {
		tags := earlyTags(c)
		return len(tags) == 1 && tags[0] == b.tag
	})

	if c.State() != CallEarly {
		t.Fatalf("call %s after a 199", c.State())
	}

	// An unreliable 199 for a dialog never established is discarded.
	p.callee(invite, "callee-c", 41004).response(199, false, false)
	b.response(180, false, false)

	if got := earlyTags(c); len(got) != 1 || got[0] != b.tag || c.hasLeg("callee-c") {
		t.Fatalf("early dialogs %v, a dialog for callee-c %v; want only %s", got, c.hasLeg("callee-c"), b.tag)
	}

	b.response(200, false, true)

	if _, err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	p.request("ACK")

	if got := c.ID().RemoteTag; got != b.tag {
		t.Errorf("call dialog with %s, want %s", got, b.tag)
	}
}

// RFC 3261 §15: a callee does not end its early dialog with a BYE, and one that does leaves the
// other dialogs of the forked INVITE.
func TestByeOnEarlyDialog(t *testing.T) {
	ctx := testContext(t)
	p, c, invite := forkedInvite(t)

	a := p.callee(invite, "callee-a", 41000)
	b := p.callee(invite, "callee-b", 41002)

	a.response(180, false, false)
	b.response(180, false, false)

	eventually(t, "two early dialogs", func() bool { return len(c.EarlyDialogs()) == 2 })

	bye := sip.NewRequest("BYE", sip.URI{Scheme: "sip", User: "x", Host: sip.FormatHost(loopback)})
	bye.Header.Add("Via", sip.NewVia(sip.UDP, p.s.Addr()).String())
	bye.Header.Add("Max-Forwards", "70")
	bye.Header.Add("From", "<tel:+15550002>;tag="+a.tag)
	bye.Header.Add("To", invite.Header.Get("From"))
	bye.Header.Add("Call-ID", invite.Header.CallID())
	bye.Header.Add("CSeq", "1 BYE")
	p.send(bye)

	p.response(200, "BYE")

	eventually(t, "one early dialog", func() bool { return len(c.EarlyDialogs()) == 1 })

	// The dialog of a, the call's until then, gave way to b's.
	if got := earlyTags(c); got[0] != b.tag || c.ID().RemoteTag != b.tag {
		t.Fatalf("early dialogs %v, call dialog with %s; want %s", got, c.ID().RemoteTag, b.tag)
	}

	b.response(200, false, true)

	if _, err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

// RFC 3326
func TestCancelReason(t *testing.T) {
	p := newPeer(t, Config{})

	invite := p.capture(iphoneCaller + "003-INVITE.sip").(*sip.Request)
	p.send(invite)

	c := incoming(t, p.u)

	cancel, err := sip.NewCancel(invite)
	if err != nil {
		t.Fatal(err)
	}

	cancel.Header.Add("Reason", `SIP;cause=200;text="Call completed elsewhere"`)
	p.send(cancel)

	ended(t, c, Cancelled)

	reasons := c.Reasons()
	if len(reasons) != 1 {
		t.Fatalf("reasons %v", reasons)
	}

	if cause, ok := reasons[0].Cause(); !ok || cause != 200 || reasons[0].Text() != "Call completed elsewhere" {
		t.Errorf("reason %s", reasons[0])
	}
}

func TestContactParams(t *testing.T) {
	u, _ := newNetwork(t).newUE(Config{ContactParams: sip.Params{{Name: "q", Value: "0.5"}, {Name: "video"}}})

	contact, err := sip.ParseAddress(u.contact(5060))
	if err != nil {
		t.Fatal(err)
	}

	if q, _ := contact.Params.Get("q"); q != "0.5" || !contact.Params.Has("video") || !contact.Params.Has("audio") {
		t.Errorf("Contact %s", contact)
	}
}

func (c *Call) hasLeg(tag string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.legLocked(tag) != nil
}

// RFC 6228 §4: a reliable 199 for a dialog never established is acknowledged, and ends that dialog at once.
func TestReliable199ForUnknownDialog(t *testing.T) {
	p, c, invite := forkedInvite(t)

	a := p.callee(invite, "callee-a", 41000)
	a.response(199, true, false)

	prack := p.request("PRACK")
	if toTag(t, prack) != a.tag || prack.Header.Get("RAck") != "1 1 INVITE" {
		t.Fatalf("PRACK to %s with RAck %q", toTag(t, prack), prack.Header.Get("RAck"))
	}

	p.send(sip.NewResponse(prack, 200, ""))

	if got := earlyTags(c); len(got) != 0 || c.State() == CallTerminated {
		t.Fatalf("early dialogs %v, call %s; want none, and the call still waiting", got, c.State())
	}
}

// RFC 6228 §4: losing an early dialog leaves the INVITE pending; another callee can still answer.
func TestLostEarlyDialog(t *testing.T) {
	ctx := testContext(t)
	p, c, invite := forkedInvite(t)

	a := p.callee(invite, "callee-a", 41000)
	b := p.callee(invite, "callee-b", 41002)

	a.response(183, true, true)
	p.send(sip.NewResponse(p.request("PRACK"), 481, ""))

	eventually(t, "the early dialog released", func() bool { return len(c.EarlyDialogs()) == 0 })

	if c.State() != CallEarly {
		t.Fatalf("call %s after losing its only early dialog, want early", c.State())
	}

	b.response(200, false, true)

	if res, err := c.Wait(ctx); err != nil || dialog.ResponseID(res).RemoteTag != b.tag {
		t.Fatalf("Wait = %v, %v; want the 200 from %s", res, err, b.tag)
	}

	if ack := p.request("ACK"); toTag(t, ack) != b.tag {
		t.Errorf("ACK to %s", toTag(t, ack))
	}
}

// RFC 3261 §9.1, §13.2.2.4: a 2xx that crosses the caller's CANCEL confirms a dialog the caller then ends.
func TestAnswerCrossingCancel(t *testing.T) {
	ctx := testContext(t)
	p, c, invite := forkedInvite(t)

	a := p.callee(invite, "callee-a", 41000)
	b := p.callee(invite, "callee-b", 41002)

	a.response(180, false, false)
	b.response(180, false, false)

	eventually(t, "two early dialogs", func() bool { return len(c.EarlyDialogs()) == 2 })

	cancelled := make(chan error, 1)

	go func() { cancelled <- c.Cancel(ctx) }()

	cancel := p.request("CANCEL")
	p.send(sip.NewResponse(cancel, 200, ""))

	b.response(200, false, true)

	if ack := p.request("ACK"); toTag(t, ack) != b.tag {
		t.Fatalf("ACK to %s, want %s", toTag(t, ack), b.tag)
	}

	bye := p.request("BYE")
	if toTag(t, bye) != b.tag {
		t.Fatalf("BYE to %s, want %s", toTag(t, bye), b.tag)
	}

	p.send(sip.NewResponse(bye, 200, ""))

	if err := <-cancelled; err != nil {
		t.Fatal(err)
	}

	if c.End() != Cancelled {
		t.Errorf("call ended by %s, want cancelled", c.End())
	}
}
