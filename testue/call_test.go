package testue

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/sdp"
	"github.com/ellanetworks/ims/sip/siptest"
)

const captureDir = "../sip/internal/corpus/testdata/ella/live/4g"

func newCallUE(t *testing.T, imsi string, pcscf netip.AddrPort, cfg Config) *UE {
	t.Helper()

	cfg.IMSI, cfg.IMEI = imsi, imei
	cfg.PCSCF, cfg.Local = pcscf, loopback
	cfg.Plain, cfg.NoRegEvent = true, true

	u, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = u.Close() })

	return u
}

func pair(t *testing.T) (*UE, *UE) {
	t.Helper()

	cfg := Config{AcceptCalls: true}

	placeholder := netip.AddrPortFrom(loopback, 9)
	a := newCallUE(t, "001010000000001", placeholder, cfg)
	b := newCallUE(t, "001010000000002", placeholder, cfg)

	a.cfg.PCSCF, b.cfg.PCSCF = b.Unprotected(), a.Unprotected()

	return a, b
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	return ctx
}

func incoming(t *testing.T, u *UE) *Call {
	t.Helper()

	select {
	case c := <-u.Calls():
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("no incoming call")
	}

	return nil
}

func ended(t *testing.T, c *Call, want EndReason) {
	t.Helper()

	select {
	case <-c.Done():
	case <-time.After(10 * time.Second):
		t.Fatalf("call still %s, want it ended by %s", c.State(), want)
	}

	if got := c.End(); got != want {
		t.Fatalf("call ended by %s, want %s", got, want)
	}
}

func methods(c *Call) []string {
	var out []string

	for {
		select {
		case e := <-c.Events():
			switch {
			case e.Request != nil:
				out = append(out, e.Request.Method)
			case e.Response != nil:
				cseq, _ := e.Response.Header.CSeq()
				out = append(out, strconv.Itoa(e.Response.StatusCode)+" "+cseq.Method)
			}
		default:
			return out
		}
	}
}

func sorted(events []string) string {
	slices.Sort(events)
	return strings.Join(events, ",")
}

func audio(t *testing.T, s *sdp.Session) *sdp.Media {
	t.Helper()

	if s == nil || len(s.Media) == 0 {
		t.Fatalf("no media in\n%s", s)
	}

	return s.Media[0]
}

func connect(t *testing.T, ctx context.Context, a, b *UE, opts CallOptions) (*Call, *Call) {
	t.Helper()

	ac, err := a.Invite("sip:+15550002@"+domain+";user=phone", opts)
	if err != nil {
		t.Fatal(err)
	}

	bc := incoming(t, b)

	if err := bc.Ring(ctx); err != nil {
		t.Fatalf("Ring: %v", err)
	}

	done := make(chan error, 1)

	go func() { done <- bc.Answer(ctx) }()

	res, err := ac.Wait(ctx)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	if err := <-done; err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if ac.State() != CallConfirmed || bc.State() != CallConfirmed {
		t.Fatalf("states %s and %s", ac.State(), bc.State())
	}

	return ac, bc
}

func TestBasicCall(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	ac, bc := connect(t, ctx, a, b, CallOptions{})

	if aid, bid := ac.ID(), bc.ID(); aid.CallID != bid.CallID || aid.LocalTag != bid.RemoteTag || aid.RemoteTag != bid.LocalTag {
		t.Fatalf("dialogs %+v and %+v", ac.ID(), bc.ID())
	}

	invite := bc.Invite()
	for name, want := range map[string]string{
		"P-Preferred-Service": mmtelService,
		"Session-Expires":     "1800",
		"Supported":           "100rel, timer, 199",
	} {
		if got := invite.Header.Get(name); got != want {
			t.Errorf("INVITE %s = %q, want %q", name, got, want)
		}
	}

	if !strings.Contains(invite.Header.Get("Accept-Contact"), "icsi.mmtel") {
		t.Errorf("INVITE Accept-Contact = %q", invite.Header.Get("Accept-Contact"))
	}

	waitSorted(t, ac, "100 INVITE,180 INVITE,183 INVITE,200 INVITE,200 PRACK")

	if got, want := strings.Join(methods(bc), ","), "PRACK,ACK"; got != want {
		t.Errorf("callee saw %s, want %s", got, want)
	}

	answer := audio(t, ac.RemoteSDP())
	if codecs, _ := answer.Codecs(); len(codecs) != 2 || codecs[0].Encoding != "AMR-WB" || codecs[1].Encoding != telephoneEvent {
		t.Errorf("answer codecs %+v", codecs)
	}

	if interval, refresher := ac.SessionTimer(); interval != DefaultSessionExpires || !refresher {
		t.Errorf("caller session timer %s, refresher %v", interval, refresher)
	}

	if interval, refresher := bc.SessionTimer(); interval != DefaultSessionExpires || refresher {
		t.Errorf("callee session timer %s, refresher %v", interval, refresher)
	}

	if err := ac.Bye(ctx); err != nil {
		t.Fatal(err)
	}

	ended(t, ac, LocalBye)
	ended(t, bc, RemoteBye)
}

func TestCalleeHangsUp(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	ac, bc := connect(t, ctx, a, b, CallOptions{NoSessionTimer: true})

	if err := bc.Bye(ctx); err != nil {
		t.Fatal(err)
	}

	ended(t, bc, LocalBye)
	ended(t, ac, RemoteBye)

	if err := ac.Bye(ctx); !errors.Is(err, ErrCallEnded) {
		t.Fatalf("Bye after the call = %v", err)
	}
}

func TestPreconditionCall(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	ac, bc := connect(t, ctx, a, b, CallOptions{Preconditions: true})

	if got, want := strings.Join(methods(bc), ","), "PRACK,UPDATE,ACK"; got != want {
		t.Errorf("callee saw %s, want %s", got, want)
	}

	waitSorted(t, ac, "100 INVITE,180 INVITE,183 INVITE,200 INVITE,200 PRACK,200 UPDATE")

	if !ac.PreconditionsMet() || !bc.PreconditionsMet() {
		t.Fatal("preconditions not met")
	}

	offer := audio(t, sdpOf(t, bc.Invite().Body))
	assertQoS(t, "offer", offer, "curr:qos local none", "curr:qos remote none", "des:qos mandatory local sendrecv", "des:qos optional remote sendrecv")

	assertQoS(t, "UPDATE answer", audio(t, bc.LocalSDP()), "curr:qos local sendrecv", "curr:qos remote sendrecv")
}

func sdpOf(t *testing.T, body []byte) *sdp.Session {
	t.Helper()

	s, err := sdp.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func assertQoS(t *testing.T, what string, m *sdp.Media, want ...string) {
	t.Helper()

	attrs := map[string]bool{}

	for _, kind := range []string{sdp.Current, sdp.Desired, sdp.Confirmed} {
		for _, v := range m.Attrs(kind) {
			attrs[kind+":"+v] = true
		}
	}

	for _, w := range want {
		if !attrs[w] {
			t.Errorf("%s: no a=%s in %v", what, w, attrs)
		}
	}
}

func TestReject(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	ac, err := a.Invite("tel:+15550002", CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	bc := incoming(t, b)
	if err := bc.Reject(486); err != nil {
		t.Fatal(err)
	}

	res, err := ac.Wait(ctx)
	if rerr, ok := errors.AsType[*ResponseError](err); !ok || rerr.Response.StatusCode != 486 || res.StatusCode != 486 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	ended(t, ac, Rejected)
	ended(t, bc, Rejected)
}

func TestBusyWithoutAcceptCalls(t *testing.T) {
	ctx := testContext(t)
	a, _ := pair(t)

	b := newCallUE(t, "001010000000003", a.Unprotected(), Config{})
	a.cfg.PCSCF = b.Unprotected()

	ac, err := a.Invite("tel:+15550003", CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ac.Wait(ctx); err == nil || !strings.Contains(err.Error(), "486") {
		t.Fatalf("Wait = %v, want 486", err)
	}
}

func TestCancel(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	ac, err := a.Invite("tel:+15550002", CallOptions{Preconditions: true})
	if err != nil {
		t.Fatal(err)
	}

	bc := incoming(t, b)
	if err := bc.Ring(ctx); err != nil {
		t.Fatal(err)
	}

	if err := ac.Cancel(ctx); err != nil {
		t.Fatal(err)
	}

	ended(t, ac, Cancelled)
	ended(t, bc, Cancelled)

	if res, err := ac.Wait(ctx); res == nil || res.StatusCode != 487 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	if err := bc.Answer(ctx); !errors.Is(err, ErrCallEnded) {
		t.Fatalf("Answer after CANCEL = %v", err)
	}
}

func TestCancelBeforeRinging(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	ac, err := a.Invite("tel:+15550002", CallOptions{})
	if err != nil {
		t.Fatal(err)
	}

	bc := incoming(t, b)

	if err := ac.Cancel(ctx); err != nil {
		t.Fatal(err)
	}

	ended(t, bc, Cancelled)
	ended(t, ac, Cancelled)
}

func TestHoldAndResume(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	ac, bc := connect(t, ctx, a, b, CallOptions{})
	methods(bc)

	if err := ac.Hold(ctx); err != nil {
		t.Fatal(err)
	}

	if d := ac.LocalSDP().MediaDirection(0); d != sdp.SendOnly {
		t.Errorf("hold offer %s", d)
	}

	if d := ac.RemoteSDP().MediaDirection(0); d != sdp.RecvOnly {
		t.Errorf("hold answer %s", d)
	}

	if err := ac.Resume(ctx); err != nil {
		t.Fatal(err)
	}

	if d := ac.RemoteSDP().MediaDirection(0); d != sdp.SendRecv {
		t.Errorf("resume answer %s", d)
	}

	waitMethods(t, bc, "INVITE,ACK,INVITE,ACK")

	if ac.State() != CallConfirmed || bc.State() != CallConfirmed {
		t.Fatalf("states %s and %s", ac.State(), bc.State())
	}

	if err := bc.Hold(ctx); err != nil {
		t.Fatal(err)
	}

	if d := bc.RemoteSDP().MediaDirection(0); d != sdp.RecvOnly {
		t.Errorf("callee's hold answered %s", d)
	}
}

func waitMethods(t *testing.T, c *Call, want string) {
	t.Helper()

	waitEvents(t, c, want, func(got []string) string { return strings.Join(got, ",") })
}

func waitSorted(t *testing.T, c *Call, want string) {
	t.Helper()

	waitEvents(t, c, want, func(got []string) string { return sorted(slices.Clone(got)) })
}

func waitEvents(t *testing.T, c *Call, want string, format func([]string) string) {
	t.Helper()

	var got []string

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got = append(got, methods(c)...)
		if format(got) == want {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("saw %s, want %s", format(got), want)
}

func TestSessionRefresh(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	_, bc := connect(t, ctx, a, b, CallOptions{SessionExpires: 2 * time.Second})
	methods(bc)

	waitMethods(t, bc, "UPDATE")

	if bc.State() != CallConfirmed {
		t.Fatalf("callee %s", bc.State())
	}
}

func TestSessionExpiry(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	ac, bc := connect(t, ctx, a, b, CallOptions{SessionExpires: 2 * time.Second})
	ac.SetAutoRefresh(false)

	ended(t, bc, Expired)
	ended(t, ac, RemoteBye)
}

func TestReinviteGlare(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	ac, bc := connect(t, ctx, a, b, CallOptions{})

	ac.mu.Lock()
	ac.offering = true
	ac.mu.Unlock()

	err := bc.Hold(ctx)
	if rerr, ok := errors.AsType[*ResponseError](err); !ok || rerr.Response.StatusCode != 491 {
		t.Fatalf("Hold during an offer = %v, want 491", err)
	}

	if d := bc.LocalSDP().MediaDirection(0); d != sdp.SendRecv {
		t.Errorf("callee's description %s after the refused hold", d)
	}

	ac.mu.Lock()
	ac.offering = false
	ac.mu.Unlock()

	if err := bc.Hold(ctx); err != nil {
		t.Fatal(err)
	}
}

type peer struct {
	t *testing.T
	s *siptest.Socket
	u *UE
}

func newPeer(t *testing.T, cfg Config) *peer {
	t.Helper()

	s := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	cfg.AcceptCalls = true

	return &peer{t: t, s: s, u: newCallUE(t, "001010000000002", s.Addr(), cfg)}
}

func (p *peer) capture(name string) sip.Message {
	p.t.Helper()

	b, err := os.ReadFile(filepath.Join(captureDir, name))
	if err != nil {
		p.t.Fatal(err)
	}

	m, err := sip.Parse(b)
	if err != nil {
		p.t.Fatalf("%s: %v", name, err)
	}

	if req, ok := m.(*sip.Request); ok {
		req.Header.Del("Via")

		via := sip.NewVia(sip.UDP, p.s.Addr())
		req.Header.Prepend("Via", via.String())
	}

	return m
}

func (p *peer) send(m sip.Message) {
	p.s.Send(sip.UDP, p.u.Unprotected(), m)
}

func (p *peer) response(code int, method string) *sip.Response {
	p.t.Helper()

	for {
		res, _ := p.s.RecvResponse()

		cseq, _ := res.Header.CSeq()
		if res.StatusCode == code && cseq.Method == method {
			return res
		}

		if res.StatusCode == 100 {
			continue
		}

		p.t.Fatalf("got %s for %s, want %d", res.StartLine(), cseq.Method, code)
	}
}

func (p *peer) request(method string) *sip.Request {
	p.t.Helper()

	for {
		req, _ := p.s.RecvRequest()
		if req.Method == method {
			return req
		}

		p.t.Fatalf("got %s, want %s", req.StartLine(), method)
	}
}

func setToTag(t *testing.T, h *sip.Header, tag string) {
	t.Helper()

	to, err := h.To()
	if err != nil {
		t.Fatal(err)
	}

	to.Params.Set("tag", tag)
	h.Set("To", to.String())
}

// What the P-CSCF delivered to the callee when an iPhone 11 called a Crosscall Core-Z5.
const iphoneCaller = "iphone-11/call_callee_bye/"

func TestCalleeAgainstIPhoneCaller(t *testing.T) {
	ctx := testContext(t)
	p := newPeer(t, Config{})

	invite := p.capture(iphoneCaller + "003-INVITE.sip").(*sip.Request)
	p.send(invite)

	c := incoming(t, p.u)

	ring := make(chan error, 1)

	go func() { ring <- c.Ring(ctx) }()

	p.response(100, "INVITE")

	res := p.response(183, "INVITE")
	if got := res.Header.Get("Require"); got != "precondition, 100rel" {
		t.Errorf("183 Require = %q", got)
	}

	if got := len(res.Header.Values("Record-Route")); got != 6 {
		t.Errorf("183 has %d Record-Route, want the INVITE's 6", got)
	}

	answer := audio(t, sdpOf(t, res.Body))
	if d, _ := answer.Desc(); strings.Join(d.Formats, " ") != "99 105" {
		t.Errorf("answer formats %v, want AMR-WB and its telephone-event", d.Formats)
	}

	assertQoS(t, "183", answer, "curr:qos local none", "curr:qos remote none",
		"des:qos mandatory local sendrecv", "des:qos mandatory remote sendrecv", "conf:qos remote sendrecv")

	tag := dialogTag(t, res)

	rseq, err := res.Header.RSeq()
	if err != nil {
		t.Fatal(err)
	}

	prack := p.capture(iphoneCaller + "008-PRACK.sip").(*sip.Request)
	setToTag(t, &prack.Header, tag)
	prack.Header.Set("RAck", sip.RAck{RSeq: rseq, CSeq: 1, Method: "INVITE"}.String())
	p.send(prack)
	p.response(200, "PRACK")

	update := p.capture(iphoneCaller + "012-UPDATE.sip").(*sip.Request)
	setToTag(t, &update.Header, tag)
	p.send(update)

	res, ringing := nil, false

	for res == nil || !ringing {
		r, _ := p.s.RecvResponse()

		switch cseq, _ := r.Header.CSeq(); {
		case r.StatusCode == 200 && cseq.Method == "UPDATE":
			res = r
		case r.StatusCode == 180:
			ringing = true
		default:
			t.Fatalf("got %s for %s, want the 200 to the UPDATE and a 180", r.StartLine(), cseq.Method)
		}
	}

	assertQoS(t, "200 UPDATE", audio(t, sdpOf(t, res.Body)), "curr:qos local sendrecv", "curr:qos remote sendrecv")

	// The iPhone supports timer but asks for no session interval.
	if got := res.Header.Get("Session-Expires"); got != "" {
		t.Errorf("200 UPDATE Session-Expires = %q, want none", got)
	}

	if err := <-ring; err != nil {
		t.Fatalf("Ring: %v", err)
	}

	answered := make(chan error, 1)

	go func() { answered <- c.Answer(ctx) }()

	res = p.response(200, "INVITE")
	if len(res.Body) != 0 {
		t.Errorf("200 to the INVITE with a body:\n%s", res.Body)
	}

	if got := res.Header.Get("Session-Expires"); got != "1800;refresher=uac" {
		t.Errorf("200 Session-Expires = %q", got)
	}

	ack := p.capture(iphoneCaller + "024-ACK.sip").(*sip.Request)
	setToTag(t, &ack.Header, tag)
	p.send(ack)

	if err := <-answered; err != nil {
		t.Fatalf("Answer: %v", err)
	}

	go func() { _ = c.Bye(ctx) }()

	bye := p.request("BYE")
	if got := bye.Header.Get("Route"); !strings.HasPrefix(got, "<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;") {
		t.Errorf("BYE Route = %q", got)
	}

	if bye.URI.Host != "10.46.0.17" {
		t.Errorf("BYE to %s, want the caller's Contact", bye.URI)
	}

	p.s.Send(sip.UDP, p.u.Unprotected(), sip.NewResponse(bye, 200, ""))
	ended(t, c, LocalBye)
}

func dialogTag(t *testing.T, res *sip.Response) string {
	t.Helper()

	to, err := res.Header.To()
	if err != nil || to.Tag() == "" {
		t.Fatalf("no To tag in %s", res.StartLine())
	}

	return to.Tag()
}

// What the P-CSCF delivered to the caller when a Crosscall Core-Z5 called a Motorola XT2417,
// which sends its 180 reliably.
const motorolaCallee = "crosscall-core-z5/call_to_motorola-xt2417_callee_bye/"

func TestCallerAgainstMotorolaCallee(t *testing.T) {
	ctx := testContext(t)
	p := newPeer(t, Config{})

	c, err := p.u.Invite("tel:15550000001;phone-context=ims.mnc001.mcc001.3gppnetwork.org", CallOptions{Preconditions: true})
	if err != nil {
		t.Fatal(err)
	}

	invite := p.request("INVITE")
	p.send(sip.NewResponse(invite, 100, ""))

	reply := func(name string, to *sip.Request) *sip.Response {
		res := p.capture(motorolaCallee + name).(*sip.Response)

		for _, h := range []string{"Via", "From", "Call-ID", "CSeq"} {
			res.Header.Del(h)

			for _, v := range to.Header.Values(h) {
				res.Header.Add(h, v)
			}
		}

		p.send(res)

		return res
	}

	r183 := reply("006-183-INVITE.sip", invite)

	prack := p.request("PRACK")
	if got := prack.Header.Get("RAck"); got != "1 1 INVITE" {
		t.Errorf("RAck = %q", got)
	}

	if got := prack.Header.Get("To"); got != r183.Header.Get("To") {
		t.Errorf("PRACK To = %q", got)
	}

	if got := prack.Header.Get("Route"); !strings.HasPrefix(got, "<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;") {
		t.Errorf("PRACK Route = %q, want the reversed Record-Route", got)
	}

	p.send(r183)
	reply("010-200-PRACK.sip", prack)

	update := p.request("UPDATE")
	assertQoS(t, "UPDATE", audio(t, sdpOf(t, update.Body)), "curr:qos local sendrecv", "curr:qos remote none",
		"des:qos mandatory local sendrecv", "des:qos mandatory remote sendrecv")

	reply("016-200-UPDATE.sip", update)
	reply("015-180-INVITE.sip", invite)

	prack = p.request("PRACK")
	if got := prack.Header.Get("RAck"); got != "2 1 INVITE" {
		t.Errorf("RAck for the reliable 180 = %q", got)
	}

	reply("020-200-PRACK.sip", prack)
	reply("022-200-INVITE.sip", invite)

	res, err := c.Wait(ctx)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v", res, err)
	}

	ack := p.request("ACK")
	if cseq, _ := ack.Header.CSeq(); cseq.Seq != 1 {
		t.Errorf("ACK CSeq %d", cseq.Seq)
	}

	eventually(t, "preconditions met", c.PreconditionsMet)

	if interval, refresher := c.SessionTimer(); interval != DefaultSessionExpires || !refresher {
		t.Errorf("session timer %s, refresher %v", interval, refresher)
	}

	reply("022-200-INVITE.sip", invite)
	p.request("ACK")
}

func TestNoPrack(t *testing.T) {
	ctx := testContext(t)
	p := newPeer(t, Config{T1: 10 * time.Millisecond})

	p.send(p.capture(iphoneCaller + "003-INVITE.sip"))

	c := incoming(t, p.u)

	ring := make(chan error, 1)

	go func() { ring <- c.Ring(ctx) }()

	p.response(100, "INVITE")
	first := p.response(183, "INVITE")

	if again := p.response(183, "INVITE"); again.Header.Get("RSeq") != first.Header.Get("RSeq") {
		t.Errorf("RSeq %q then %q", first.Header.Get("RSeq"), again.Header.Get("RSeq"))
	}

	for {
		res, _ := p.s.RecvResponse()
		if res.StatusCode == 183 {
			continue
		}

		if res.StatusCode != 504 {
			t.Fatalf("got %s, want 504", res.StartLine())
		}

		break
	}

	if err := <-ring; !errors.Is(err, ErrNoPrack) {
		t.Fatalf("Ring = %v", err)
	}

	ended(t, c, TimedOut)
}

func TestNoAck(t *testing.T) {
	ctx := testContext(t)
	p := newPeer(t, Config{T1: 10 * time.Millisecond})

	invite := p.capture(iphoneCaller + "003-INVITE.sip").(*sip.Request)
	invite.Header.Set("Supported", "timer")
	invite.Header.Del("Session-Expires")
	p.send(invite)

	c := incoming(t, p.u)

	answered := make(chan error, 1)

	go func() { answered <- c.Answer(ctx) }()

	p.response(100, "INVITE")

	res := p.response(200, "INVITE")
	if len(res.Body) == 0 {
		t.Error("200 without the answer")
	}

	if got := res.Header.Get("Session-Expires"); got != "1800;refresher=uac" {
		t.Errorf("200 Session-Expires = %q", got)
	}

	var bye *sip.Request

	for bye == nil {
		switch m := p.s.Recv().Msg.(type) {
		case *sip.Request:
			bye = m
		case *sip.Response:
			if m.StatusCode != 200 {
				t.Fatalf("got %s, want 200 retransmissions then a BYE", m.StartLine())
			}
		}
	}

	if bye.Method != "BYE" {
		t.Fatalf("got %s, want a BYE", bye.StartLine())
	}

	p.s.Send(sip.UDP, p.u.Unprotected(), sip.NewResponse(bye, 200, ""))

	if err := <-answered; !errors.Is(err, ErrNoAck) {
		t.Fatalf("Answer = %v", err)
	}

	ended(t, c, TimedOut)
}

func TestVideoOnlyOffer(t *testing.T) {
	p := newPeer(t, Config{})

	invite := p.capture(iphoneCaller + "003-INVITE.sip").(*sip.Request)
	body := strings.Replace(string(invite.Body), "m=audio", "m=video", 1)
	invite.SetBody(sdp.ContentType, []byte(body))
	p.send(invite)

	p.response(100, "INVITE")
	res := p.response(488, "INVITE")

	if w := res.Header.Get("Warning"); !strings.HasPrefix(w, "304 ") {
		t.Errorf("Warning = %q", w)
	}
}

func TestInDialogRequestForUnknownCall(t *testing.T) {
	p := newPeer(t, Config{})

	bye := siptest.NewRequest("BYE", "sip:ue@127.0.0.1", sip.UDP, p.s.Addr())
	setToTag(t, &bye.Header, "unknown")
	p.send(bye)

	p.response(481, "BYE")
}

func TestResponsesCarryAccessNetworkInfo(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})

	req := siptest.NewRequest("BYE", "sip:ue@127.0.0.1", sip.UDP, n.pcscf.Addr())

	if got := u.response(req, 200).Header.Get("P-Access-Network-Info"); got == "" {
		t.Fatal("response without P-Access-Network-Info")
	}
}

func TestAnswerMirrorsAMRParameters(t *testing.T) {
	m := newMedia(loopback, 40000, false)

	offer := sdpOf(t, []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.2\r\ns=-\r\nc=IN IP4 127.0.0.2\r\nt=0 0\r\n"+
		"m=audio 5000 RTP/AVP 97 98 100\r\n"+
		"a=rtpmap:97 AMR-WB/16000/1\r\na=fmtp:97 octet-align=1;mode-set=0,1,2\r\n"+
		"a=rtpmap:98 AMR-WB/16000/1\r\na=fmtp:98 mode-set=0,1,2\r\n"+
		"a=rtpmap:100 telephone-event/16000\r\n"))

	answer, err := m.answer(offer)
	if err != nil {
		t.Fatal(err)
	}

	am := audio(t, answer)
	if d, _ := am.Desc(); strings.Join(d.Formats, " ") != "98 100" {
		t.Fatalf("answer formats %v, want the bandwidth-efficient AMR-WB", d.Formats)
	}

	if fmtp, _ := am.Fmtp("98"); !strings.Contains(fmtp, "mode-set=0,1,2") || strings.Contains(fmtp, "octet-align") {
		t.Errorf("answer fmtp %q", fmtp)
	}

	offer = sdpOf(t, []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.2\r\ns=-\r\nc=IN IP4 127.0.0.2\r\nt=0 0\r\n"+
		"m=audio 5000 RTP/AVP 97\r\na=rtpmap:97 AMR/8000/1\r\na=fmtp:97 octet-align=1\r\n"))

	if answer, err = newMedia(loopback, 40000, false).answer(offer); err != nil {
		t.Fatal(err)
	}

	if fmtp, _ := audio(t, answer).Fmtp("97"); !strings.Contains(fmtp, "octet-align=1") {
		t.Errorf("answer fmtp %q, want octet-align=1", fmtp)
	}
}

func TestLaterOffersKeepTheStreams(t *testing.T) {
	m := newMedia(loopback, 40000, false)

	offer := sdpOf(t, []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.2\r\ns=-\r\nc=IN IP4 127.0.0.2\r\nt=0 0\r\n"+
		"m=video 6000 RTP/AVP 99\r\na=rtpmap:99 H264/90000\r\n"+
		"m=audio 5000 RTP/AVP 116\r\na=rtpmap:116 AMR-WB/16000/1\r\n"))

	if _, err := m.answer(offer); err != nil {
		t.Fatal(err)
	}

	next, err := m.offer(sdp.SendOnly)
	if err != nil {
		t.Fatal(err)
	}

	if len(next.Media) != 2 || next.Media[0].Type() != sdp.Video || next.Media[0].Port() != 0 || next.Media[1].Type() != sdp.Audio {
		t.Fatalf("later offer:\n%s", next)
	}

	if codecs, _ := next.Media[1].Codecs(); len(codecs) != 1 || codecs[0].Payload != 116 {
		t.Errorf("later offer codecs %+v, want the negotiated one", codecs)
	}
}

func TestAnswerWithoutPreconditions(t *testing.T) {
	m := newMedia(loopback, 40000, true)

	if _, err := m.offer(sdp.SendRecv); err != nil {
		t.Fatal(err)
	}

	answer := sdpOf(t, []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.2\r\ns=-\r\nc=IN IP4 127.0.0.2\r\nt=0 0\r\n"+
		"m=audio 5000 RTP/AVP 116\r\na=rtpmap:116 AMR-WB/16000/1\r\n"))

	if err := m.answered(answer); err != nil {
		t.Fatal(err)
	}

	if m.precondition || !m.met() {
		t.Fatal("preconditions kept after an answer without them")
	}
}

func TestHoldWhileHeld(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	ac, bc := connect(t, ctx, a, b, CallOptions{})

	if err := ac.Hold(ctx); err != nil {
		t.Fatal(err)
	}

	if err := bc.Hold(ctx); err != nil {
		t.Fatal(err)
	}

	if d := bc.LocalSDP().MediaDirection(0); d != sdp.Inactive {
		t.Errorf("hold while held offered %s", d)
	}
}

func TestSessionTimerWithoutTimerSupport(t *testing.T) {
	ctx := testContext(t)
	p := newPeer(t, Config{})

	invite := p.capture(iphoneCaller + "003-INVITE.sip").(*sip.Request)
	invite.Header.Del("Supported")
	invite.Header.Add("Supported", "100rel")
	// RFC 4028 §8.1: a proxy may ask for a session timer that the UAC does not support.
	invite.Header.Set("Session-Expires", "1800")
	p.send(invite)

	c := incoming(t, p.u)

	c.mu.Lock()
	c.rel100 = false
	c.mu.Unlock()

	answered := make(chan error, 1)

	go func() { answered <- c.Answer(ctx) }()

	p.response(100, "INVITE")
	res := p.response(200, "INVITE")

	if got := res.Header.Get("Session-Expires"); got != "1800;refresher=uas" {
		t.Errorf("Session-Expires = %q, want the UAS refreshing", got)
	}

	if has(res.Header, "Require", "timer") {
		t.Error("Require: timer to a UAC without timers")
	}

	if interval, refresher := c.SessionTimer(); interval != DefaultSessionExpires || !refresher {
		t.Errorf("session timer %s, refresher %v", interval, refresher)
	}
}

func TestIntervalTooSmall(t *testing.T) {
	ctx := testContext(t)
	p := newPeer(t, Config{})

	c, err := p.u.Invite("tel:+15550002", CallOptions{SessionExpires: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	invite := p.request("INVITE")

	res := sip.NewResponse(invite, 422, "")
	res.Header.Add("Min-SE", "90")
	p.send(res)

	var retry *sip.Request

	for retry == nil {
		if req, _ := p.s.RecvRequest(); req.Method == "INVITE" {
			retry = req
		} else if req.Method != "ACK" {
			t.Fatalf("got %s, want the INVITE again", req.StartLine())
		}
	}

	if got := retry.Header.Get("Session-Expires"); got != "90" || retry.Header.Get("Min-SE") != "90" {
		t.Errorf("retry Session-Expires %q, Min-SE %q", got, retry.Header.Get("Min-SE"))
	}

	if cseq, _ := retry.Header.CSeq(); cseq.Seq != 2 {
		t.Errorf("retry CSeq %d", cseq.Seq)
	}

	p.send(sip.NewResponse(retry, 486, ""))

	if res, _ := c.Wait(ctx); res == nil || res.StatusCode != 486 {
		t.Fatalf("Wait = %v", res)
	}
}

func TestUnsupportedRequire(t *testing.T) {
	p := newPeer(t, Config{})

	invite := p.capture(iphoneCaller + "003-INVITE.sip").(*sip.Request)
	invite.Header.Add("Require", "foo")
	p.send(invite)

	p.response(100, "INVITE")

	res := p.response(420, "INVITE")
	if got := res.Header.Get("Unsupported"); got != "foo" {
		t.Errorf("Unsupported = %q", got)
	}
}

// RFC 3312
func TestCalleeReportsItsResources(t *testing.T) {
	ctx := testContext(t)
	p := newPeer(t, Config{})

	invite := p.capture(iphoneCaller + "003-INVITE.sip").(*sip.Request)
	invite.SetBody(sdp.ContentType, []byte(strings.Replace(string(invite.Body), "a=curr:qos local none", "a=curr:qos local sendrecv", 1)))
	p.send(invite)

	c := incoming(t, p.u)

	ring := make(chan error, 1)

	go func() { ring <- c.Ring(ctx) }()

	p.response(100, "INVITE")

	res := p.response(183, "INVITE")
	tag := dialogTag(t, res)
	rseq, _ := res.Header.RSeq()

	prack := p.capture(iphoneCaller + "008-PRACK.sip").(*sip.Request)
	setToTag(t, &prack.Header, tag)
	prack.Header.Set("RAck", sip.RAck{RSeq: rseq, CSeq: 1, Method: "INVITE"}.String())
	p.send(prack)

	var update *sip.Request

	for gotOK := false; !gotOK || update == nil; {
		switch m := p.s.Recv().Msg.(type) {
		case *sip.Request:
			if m.Method != "UPDATE" || update != nil {
				t.Fatalf("got %s, want the UPDATE", m.StartLine())
			}

			update = m
		case *sip.Response:
			cseq, _ := m.Header.CSeq()
			if m.StatusCode != 200 || cseq.Method != "PRACK" || gotOK {
				t.Fatalf("got %s for %s, want 200 for PRACK", m.StartLine(), cseq.Method)
			}

			gotOK = true
		}
	}

	assertQoS(t, "UPDATE", audio(t, sdpOf(t, update.Body)), "curr:qos local sendrecv", "curr:qos remote sendrecv")

	ok := sip.NewResponse(update, 200, "")
	ok.SetBody(sdp.ContentType, []byte(strings.NewReplacer("a=curr:qos remote none", "a=curr:qos remote sendrecv",
		"a=inactive", "a=sendrecv").Replace(string(invite.Body))))
	p.send(ok)

	p.response(180, "INVITE")

	if err := <-ring; err != nil {
		t.Fatalf("Ring: %v", err)
	}
}

func TestUpdateGlare(t *testing.T) {
	ctx := testContext(t)
	a, b := pair(t)

	ac, bc := connect(t, ctx, a, b, CallOptions{})

	ac.mu.Lock()
	ac.offering = true
	ac.mu.Unlock()

	err := bc.update(ctx, true)
	if rerr, ok := errors.AsType[*ResponseError](err); !ok || rerr.Response.StatusCode != 491 {
		t.Fatalf("UPDATE with an offer = %v, want 491", err)
	}

	if err := bc.Refresh(ctx); err != nil {
		t.Fatalf("Refresh = %v", err)
	}

	ac.mu.Lock()
	ac.offering = false
	ac.mu.Unlock()
}

func TestUpdateBeforeTheAnswer(t *testing.T) {
	ctx := testContext(t)
	p := newPeer(t, Config{})

	invite := p.capture(iphoneCaller + "003-INVITE.sip").(*sip.Request)
	p.send(invite)

	c := incoming(t, p.u)

	if err := c.provisional(ctx, 180, false); err != nil {
		t.Fatal(err)
	}

	p.response(100, "INVITE")
	tag := dialogTag(t, p.response(180, "INVITE"))

	update := p.capture(iphoneCaller + "012-UPDATE.sip").(*sip.Request)
	setToTag(t, &update.Header, tag)
	p.send(update)

	res := p.response(500, "UPDATE")
	if n, err := strconv.Atoi(res.Header.Get("Retry-After")); err != nil || n < 0 || n > 10 {
		t.Errorf("Retry-After = %q", res.Header.Get("Retry-After"))
	}
}

func TestCancelDuringRetransmissions(t *testing.T) {
	for range 20 {
		p := newPeer(t, Config{T1: 2 * time.Millisecond})

		invite := p.capture(iphoneCaller + "003-INVITE.sip").(*sip.Request)
		p.send(invite)

		c := incoming(t, p.u)

		go func() { _ = c.Ring(context.Background()) }()

		p.response(100, "INVITE")
		p.response(183, "INVITE")

		cancel, err := sip.NewCancel(invite)
		if err != nil {
			t.Fatal(err)
		}

		p.send(cancel)

		ended(t, c, Cancelled)
	}
}

func TestMessage(t *testing.T) {
	a, b := pair(t)

	res, err := a.Message(testContext(t), "tel:+15550002", SMSContentType, []byte{0x00, 0x01})
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("Message = %v, %v, want 200", res, err)
	}

	timeout := time.After(10 * time.Second)

	for {
		var e Event

		select {
		case e = <-b.Events():
		case <-timeout:
			t.Fatal("no MESSAGE reached the callee")
		}

		if e.Request == nil || e.Request.Method != "MESSAGE" {
			continue
		}

		if ct := e.Request.Header.ContentType(); ct != SMSContentType || string(e.Request.Body) != "\x00\x01" ||
			e.Request.Header.Get("Accept-Contact") != "*;+g.3gpp.smsip" {
			t.Fatalf("MESSAGE:\n%s", e.Request)
		}

		return
	}
}

// RFC 3261 §8.1.2, RFC 3263 §4.1
func TestRequestTransportFollowsTheRoute(t *testing.T) {
	u := newCallUE(t, "001010000000001", netip.AddrPortFrom(loopback, 9), Config{})

	for _, tc := range []struct {
		route string
		want  sip.Transport
	}{
		{"", sip.UDP},
		{"<sip:127.0.0.1:5063;lr>", sip.UDP},
		{"<sip:127.0.0.1:5063;transport=tcp;lr>, <sip:127.0.0.1:5060;lr>", sip.TCP},
	} {
		req := sip.NewRequest("BYE", sip.URI{Scheme: "sip", Host: "127.0.0.1"})
		if tc.route != "" {
			req.Header.Add("Route", tc.route)
		}

		if got := u.requestTransport(req); got != tc.want {
			t.Errorf("Route %q: transport %s, want %s", tc.route, got, tc.want)
		}
	}
}
