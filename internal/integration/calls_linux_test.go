//go:build linux && (amd64 || arm64)

package integration

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/internal/testue"
	"github.com/ellanetworks/ims/sip"
)

func (s *scene) caller(i int, v6 bool, cfg testue.Config) *testue.UE {
	s.t.Helper()

	cfg.AcceptCalls = true

	u := s.newUEAt(i, v6, cfg)
	s.register(u)

	return u
}

func phone(i int) string {
	return subscriberAt(i).msisdn + ";user=phone"
}

func callID(c *testue.Call) string {
	return c.Invite().Header.CallID()
}

func incoming(t *testing.T, u *testue.UE, of *testue.Call) *testue.Call {
	t.Helper()

	deadline := time.After(15 * time.Second)

	for {
		select {
		case c := <-u.Calls():
			if callID(c) == callID(of) {
				return c
			}

			t.Logf("rejecting the stale call %s", callID(c))
			_ = c.Reject(486)
		case <-deadline:
			t.Fatalf("no incoming call %s", callID(of))
		}
	}
}

func ended(t *testing.T, c *testue.Call, want testue.EndReason) {
	t.Helper()

	select {
	case <-c.Done():
	case <-time.After(15 * time.Second):
		t.Fatalf("call still %s, want it ended by %s", c.State(), want)
	}

	if got := c.End(); got != want {
		t.Fatalf("call ended by %s, want %s", got, want)
	}
}

func invite(t *testing.T, a *testue.UE, target string, opts testue.CallOptions) *testue.Call {
	t.Helper()

	c, err := a.Invite(target, opts)
	if err != nil {
		t.Fatalf("Invite %s: %v", target, err)
	}

	return c
}

func connect(t *testing.T, ctx context.Context, a, b *testue.UE, target string, opts testue.CallOptions) (*testue.Call, *testue.Call) {
	t.Helper()

	ac := invite(t, a, target, opts)
	bc := incoming(t, b, ac)

	if err := bc.Ring(ctx); err != nil {
		t.Fatalf("Ring: %v", err)
	}

	done := make(chan error, 1)

	go func() { done <- bc.Answer(ctx) }()

	if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v, want 200", res, err)
	}

	if err := <-done; err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if ac.State() != testue.CallConfirmed || bc.State() != testue.CallConfirmed {
		t.Fatalf("states %s and %s, want both confirmed", ac.State(), bc.State())
	}

	return ac, bc
}

func failed(t *testing.T, ctx context.Context, c *testue.Call, code int) {
	t.Helper()

	res, err := c.Wait(ctx)
	if rerr, ok := errors.AsType[*testue.ResponseError](err); !ok || rerr.Response.StatusCode != code || res.StatusCode != code {
		t.Fatalf("Wait = %v, %v, want %d", res, err, code)
	}

	ended(t, c, testue.Rejected)
}

type events struct {
	requests, responses []string
	messages            []sip.Message

	raw map[string]bool
}

func (e *events) add(ev testue.Event) {
	var m sip.Message

	switch {
	case ev.Request != nil:
		m = ev.Request
	case ev.Response != nil && ev.Response.StatusCode != 100:
		m = ev.Response
	default:
		return
	}

	if e.raw[m.String()] {
		return
	}

	e.raw[m.String()] = true
	e.messages = append(e.messages, m)

	if req, ok := m.(*sip.Request); ok {
		e.requests = append(e.requests, req.Method)
		return
	}

	cseq, _ := m.Env().Header.CSeq()
	e.responses = append(e.responses, strconv.Itoa(m.(*sip.Response).StatusCode)+" "+cseq.Method)
}

func sorted(v []string) string {
	v = slices.Clone(v)
	slices.Sort(v)

	return strings.Join(v, ",")
}

func contains(have []string, want string) bool {
	if want == "" {
		return true
	}

	have = slices.Clone(have)

	for _, w := range strings.Split(want, ",") {
		i := slices.Index(have, w)
		if i < 0 {
			return false
		}

		have = slices.Delete(have, i, i+1)
	}

	return true
}

func await(t *testing.T, c *testue.Call, requests, responses string) events {
	t.Helper()

	e := events{raw: map[string]bool{}}
	deadline := time.After(10 * time.Second)

	for !contains(e.requests, requests) || !contains(e.responses, responses) {
		select {
		case ev := <-c.Events():
			e.add(ev)
		case <-deadline:
			t.Fatalf("saw requests %s and responses %s, want %s and %s", sorted(e.requests), sorted(e.responses), requests, responses)
		}
	}

	for settled := false; !settled; {
		select {
		case ev := <-c.Events():
			e.add(ev)
		case <-time.After(50 * time.Millisecond):
			settled = true
		}
	}

	if got := sorted(e.requests); requests != "" && got != requests {
		t.Errorf("requests %s, want %s", got, requests)
	}

	if got := sorted(e.responses); responses != "" && got != responses {
		t.Errorf("responses %s, want %s", got, responses)
	}

	return e
}

func identities(h sip.Header, name string) []sip.URI {
	as, err := h.Addresses(name)
	if err != nil {
		return nil
	}

	out := make([]sip.URI, len(as))
	for i, a := range as {
		out[i] = a.URI
	}

	return out
}

func hasIdentity(h sip.Header, name, want string) bool {
	u, err := sip.ParseURI(want)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(identities(h, name), u.Equivalent)
}

func absent(t *testing.T, what string, h sip.Header, names ...string) {
	t.Helper()

	for _, name := range names {
		if h.Has(name) {
			t.Errorf("%s carries %s: %q", what, name, h.Values(name))
		}
	}
}

// TS 24.229 §5.2.6.4.1, §5.2.6.4.3, §5.4.3.2, §5.4.3.3
func checkCallee(t *testing.T, bc *testue.Call, from, to int) {
	t.Helper()

	in := bc.Invite()

	for _, id := range []string{subscriberAt(from).msisdn, subscriberAt(from).tel} {
		if !hasIdentity(in.Header, "P-Asserted-Identity", id) {
			t.Errorf("callee's INVITE P-Asserted-Identity = %q, want %s", in.Header.Values("P-Asserted-Identity"), id)
		}
	}

	if !hasIdentity(in.Header, "P-Called-Party-ID", subscriberAt(to).tel) {
		t.Errorf("callee's INVITE P-Called-Party-ID = %q, want %s", in.Header.Values("P-Called-Party-ID"), subscriberAt(to).tel)
	}

	absent(t, "callee's INVITE", in.Header, "P-Charging-Vector", "P-Charging-Function-Addresses", "P-Asserted-Service",
		"P-Preferred-Identity", "P-Served-User", "P-User-Database")

	rr := recordRoutes(in)
	if len(rr) != 6 {
		t.Fatalf("callee's INVITE Record-Route = %q, want both P-CSCFs twice and both S-CSCFs", rr)
	}

	for i, end := range []sip.URI{rr[0], rr[5]} {
		if end.Port != pcscfIPsecServerPort || !end.Params.Has("ue") {
			t.Errorf("Record-Route %d = %s, want the P-CSCF's protected server port %d facing the UE", i*5, end, pcscfIPsecServerPort)
		}
	}
}

// TS 24.229 §5.2.6.3.4, §5.2.6.4.4, §5.4.3.3, RFC 5009
func checkCaller(t *testing.T, e events, to int) {
	t.Helper()

	var checked int

	for _, m := range e.messages {
		res, ok := m.(*sip.Response)
		if cseq, _ := m.Env().Header.CSeq(); !ok || cseq.Method != "INVITE" || res.StatusCode < 180 || res.StatusCode >= 300 {
			continue
		}

		checked++

		for _, id := range []string{phone(to), subscriberAt(to).tel} {
			if !hasIdentity(res.Header, "P-Asserted-Identity", id) {
				t.Errorf("caller's %s P-Asserted-Identity = %q, want %s", res.StartLine(), res.Header.Values("P-Asserted-Identity"), id)
			}
		}

		absent(t, "caller's "+res.StartLine(), res.Header, "P-Access-Network-Info", "P-Charging-Vector", "P-Charging-Function-Addresses")

		if v := res.Header.Get("P-Early-Media"); v != "" && v != "inactive" {
			t.Errorf("caller's %s P-Early-Media = %q, want the callee's authorisation removed", res.StartLine(), v)
		}
	}

	if checked == 0 {
		t.Error("no 18x or 2xx to check at the caller")
	}
}

func recordRoutes(m sip.Message) []sip.URI {
	rr, _ := m.Env().Header.RecordRoutes()

	out := make([]sip.URI, len(rr))
	for i, a := range rr {
		out[i] = a.URI
	}

	return out
}

// TS 24.229 §5.2.6.4.5, §5.2.6.4.6
func refreshRoutes(t *testing.T, e events, dialog []sip.URI) {
	t.Helper()

	var checked int

	for _, m := range e.messages {
		cseq, err := m.Env().Header.CSeq()
		if err != nil || cseq.Method != "UPDATE" {
			continue
		}

		if res, ok := m.(*sip.Response); ok && !res.IsSuccess() {
			continue
		}

		checked++

		if rr := recordRoutes(m); !slices.EqualFunc(rr, dialog, func(a, b sip.URI) bool { return a.Equivalent(b) }) {
			t.Errorf("%s Record-Route = %q, want the dialog's %q", m.StartLine(), rr, dialog)
		}
	}

	if checked == 0 {
		t.Error("no UPDATE seen")
	}
}

// RFC 3261 §8.1.2, RFC 3263 §4.1
func routeTransport(route sip.URI, fallback sip.Transport) sip.Transport {
	if v, ok := route.Params.Get("transport"); ok && v != "" {
		return sip.Transport(strings.ToUpper(v))
	}

	return fallback
}

func (s *scene) sent(ue int, method string, status int) []wire {
	return s.wire.find(func(e wire) bool { return e.ue == ue && e.sent && e.method == method && e.status == status })
}

func TestCall(t *testing.T) {
	for _, tr := range []sip.Transport{sip.UDP, sip.TCP} {
		for _, v6 := range []bool{false, true} {
			name := string(tr) + "/IPv4"
			if v6 {
				name = string(tr) + "/IPv6"
			}

			fixtures := tr == sip.UDP && !v6

			t.Run(name, func(t *testing.T) { testCall(t, tr, v6, fixtures) })
		}
	}
}

func testCall(t *testing.T, tr sip.Transport, v6, fixtures bool) {
	sc := newScene(t)
	a := sc.caller(0, v6, testue.Config{Transport: tr})
	b := sc.caller(1, v6, testue.Config{Transport: tr, EarlyMedia: "sendrecv"})

	record := func(s *scene, flow string) {
		if fixtures {
			s.record(flow)
		}
	}

	t.Run("caller hangs up", func(t *testing.T) {
		s := sc.in(t)

		record(s, "call_caller_bye")
		defer s.record("")

		ctx := s.ctx()
		ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})

		checkCallee(t, bc, 0, 1)
		checkCaller(t, await(t, ac, "", "180 INVITE,183 INVITE,200 INVITE,200 PRACK"), 1)
		await(t, bc, "ACK,PRACK", "")

		if err := ac.Refresh(ctx); err != nil {
			t.Fatalf("Refresh: %v", err)
		}

		refreshRoutes(t, await(t, bc, "UPDATE", ""), recordRoutes(bc.Invite()))

		if err := ac.Bye(ctx); err != nil {
			t.Fatalf("Bye: %v", err)
		}

		ended(t, ac, testue.LocalBye)
		ended(t, bc, testue.RemoteBye)

		want := routeTransport(recordRoutes(bc.Invite())[5], tr)
		if byes := s.sent(0, "BYE", 0); len(byes) == 0 || byes[len(byes)-1].transport != want {
			t.Errorf("caller's BYE sent %+v, want it over %s, the transport of its first route", byes, want)
		}
	})

	t.Run("callee hangs up", func(t *testing.T) {
		s := sc.in(t)

		record(s, "call_callee_bye")
		defer s.record("")

		ctx := s.ctx()
		ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})

		await(t, ac, "", "180 INVITE,183 INVITE,200 INVITE,200 PRACK")

		if err := bc.Refresh(ctx); err != nil {
			t.Fatalf("Refresh: %v", err)
		}

		dialog := recordRoutes(bc.Invite())
		slices.Reverse(dialog)
		refreshRoutes(t, await(t, ac, "UPDATE", ""), dialog)

		if err := bc.Bye(ctx); err != nil {
			t.Fatalf("Bye: %v", err)
		}

		ended(t, bc, testue.LocalBye)
		ended(t, ac, testue.RemoteBye)
	})

	t.Run("preconditions", func(t *testing.T) {
		s := sc.in(t)

		record(s, "call_preconditions")
		defer s.record("")

		ctx := s.ctx()
		ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{Preconditions: true})

		caller := await(t, ac, "", "180 INVITE,183 INVITE,200 INVITE,200 PRACK,200 UPDATE")
		callee := await(t, bc, "ACK,PRACK,UPDATE", "")

		checkCaller(t, caller, 1)

		dialog := recordRoutes(bc.Invite())
		refreshRoutes(t, callee, dialog)
		refreshRoutes(t, caller, dialog)

		if !ac.PreconditionsMet() || !bc.PreconditionsMet() {
			t.Fatal("preconditions not met")
		}

		if err := ac.Bye(ctx); err != nil {
			t.Fatalf("Bye: %v", err)
		}

		ended(t, bc, testue.RemoteBye)
	})
}

// TS 33.203 §7.1, RFC 3261 §18.1.1
func TestCallBetweenPhonesBendingTheTransportRules(t *testing.T) {
	s := newScene(t)
	a := s.caller(0, false, testue.Config{Transport: sip.UDP, LargeUDP: true})
	b := s.caller(1, false, testue.Config{Transport: sip.UDP, TCPResponsesFromClientPort: true})

	ctx := s.ctx()
	ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{Preconditions: true})

	if invites := s.sent(0, "INVITE", 0); len(invites) != 1 || invites[0].transport != sip.UDP || invites[0].size <= 1300 {
		t.Errorf("caller's INVITE sent %+v, want one datagram over 1300 bytes", invites)
	}

	portUC := b.SAs()[0].Set.Local.PortC

	progress := s.sent(1, "INVITE", 183)
	if len(progress) == 0 {
		t.Fatal("the callee sent no 183")
	}

	for _, p := range progress {
		if p.transport != sip.TCP || p.src.Port() != portUC || p.dst.Port() != pcscfIPsecServerPort {
			t.Errorf("callee's 183 sent %+v, want TCP from its port_uc %d to port_ps %d", p, portUC, pcscfIPsecServerPort)
		}
	}

	if err := bc.Bye(ctx); err != nil {
		t.Fatal(err)
	}

	ended(t, ac, testue.RemoteBye)

	sa := a.SAs()[0].Set

	if byes := s.wire.find(func(e wire) bool { return e.ue == 0 && !e.sent && e.method == "BYE" }); len(byes) != 1 ||
		byes[0].transport != sip.UDP || byes[0].dst.Port() != sa.Local.PortS {
		t.Errorf("caller received the BYE %+v, want it over UDP on its port_us %d", byes, sa.Local.PortS)
	}

	if oks := s.sent(0, "BYE", 200); len(oks) == 0 || oks[0].transport != sip.UDP ||
		oks[0].src.Port() != sa.Local.PortC || oks[0].dst.Port() != pcscfIPsecServerPort {
		t.Errorf("caller's 200 to the BYE sent %+v, want UDP from its port_uc %d to port_ps %d", oks, sa.Local.PortC, pcscfIPsecServerPort)
	}
}

func TestCallFailures(t *testing.T) {
	sc := newScene(t)
	a := sc.caller(0, false, testue.Config{Transport: sip.UDP})
	b := sc.caller(1, false, testue.Config{Transport: sip.UDP})

	t.Run("busy", func(t *testing.T) {
		s := sc.in(t)

		s.record("call_busy")
		defer s.record("")

		ac := invite(t, a, phone(1), testue.CallOptions{})
		bc := incoming(t, b, ac)

		if err := bc.Reject(486); err != nil {
			t.Fatal(err)
		}

		failed(t, s.ctx(), ac, 486)
		ended(t, bc, testue.Rejected)
	})

	t.Run("cancelled", func(t *testing.T) {
		s := sc.in(t)

		s.record("call_cancel")
		defer s.record("")

		ctx := s.ctx()
		ac := invite(t, a, phone(1), testue.CallOptions{Preconditions: true})
		bc := incoming(t, b, ac)

		if err := bc.Ring(ctx); err != nil {
			t.Fatal(err)
		}

		if err := ac.Cancel(ctx); err != nil {
			t.Fatal(err)
		}

		ended(t, ac, testue.Cancelled)
		ended(t, bc, testue.Cancelled)

		if res, err := ac.Wait(ctx); res == nil || res.StatusCode != 487 {
			t.Fatalf("Wait = %v, %v, want 487", res, err)
		}
	})

	t.Run("cancelled before ringing", func(t *testing.T) {
		s := sc.in(t)
		ctx := s.ctx()
		ac := invite(t, a, phone(1), testue.CallOptions{})
		bc := incoming(t, b, ac)

		if err := ac.Cancel(ctx); err != nil {
			t.Fatal(err)
		}

		ended(t, bc, testue.Cancelled)
		ended(t, ac, testue.Cancelled)

		if res, err := ac.Wait(ctx); res == nil || res.StatusCode != 487 {
			t.Fatalf("Wait = %v, %v, want 487", res, err)
		}
	})

	t.Run("unknown number", func(t *testing.T) {
		failed(t, sc.in(t).ctx(), invite(t, a, "tel:+15550099", testue.CallOptions{}), 404)
	})

	t.Run("never registered", func(t *testing.T) {
		failed(t, sc.in(t).ctx(), invite(t, a, subscriberAt(2).tel, testue.CallOptions{}), 480)
	})

	t.Run("unregistered with services", func(t *testing.T) {
		s := sc.in(t)

		s.hss.Update(subscriberAt(2).impi, func(sub *hsstest.Subscriber) { sub.UnregisteredServices = true })
		defer s.hss.Update(subscriberAt(2).impi, func(sub *hsstest.Subscriber) { sub.UnregisteredServices = false })

		failed(t, s.ctx(), invite(t, a, subscriberAt(2).tel, testue.CallOptions{}), 480)
	})

	t.Run("deregistered", func(t *testing.T) {
		s := sc.in(t)
		c := s.caller(3, false, testue.Config{Transport: sip.UDP})

		if err := c.Deregister(s.ctx()); err != nil {
			t.Fatal(err)
		}

		failed(t, s.ctx(), invite(t, a, subscriberAt(3).tel, testue.CallOptions{}), 480)
	})

	t.Run("MESSAGE", func(t *testing.T) {
		s := sc.in(t)

		res, err := a.Message(s.ctx(), subscriberAt(1).tel, testue.SMSContentType, []byte{0x00, 0x01})
		if err != nil || res.StatusCode != 403 {
			t.Fatalf("Message = %v, %v, want 403", res, err)
		}

		time.Sleep(200 * time.Millisecond)

		if got := s.wire.find(func(e wire) bool { return e.ue == 1 && !e.sent && e.method == "MESSAGE" }); len(got) != 0 {
			t.Fatalf("the callee received %+v, want the MESSAGE stopped at the originating S-CSCF", got)
		}
	})
}

func TestCallNotAnswered(t *testing.T) {
	const timeout = 2 * time.Second

	sc := newSceneWith(t, func(srv *server.Server) { srv.NoAnswerTimeout = timeout })
	a := sc.caller(0, false, testue.Config{Transport: sip.UDP})
	b := sc.caller(1, false, testue.Config{Transport: sip.UDP})

	t.Run("ringing", func(t *testing.T) {
		ctx := sc.in(t).ctx()
		start := time.Now()
		ac := invite(t, a, phone(1), testue.CallOptions{})
		bc := incoming(t, b, ac)

		if err := bc.Ring(ctx); err != nil {
			t.Fatal(err)
		}

		failed(t, ctx, ac, 408)
		ended(t, bc, testue.Cancelled)

		if elapsed := time.Since(start); elapsed < timeout || elapsed > timeout+5*time.Second {
			t.Errorf("408 after %s, want it after the %s no-answer timeout", elapsed, timeout)
		}
	})

	t.Run("answered", func(t *testing.T) {
		ctx := sc.in(t).ctx()
		ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})

		time.Sleep(timeout + time.Second)

		if ac.State() != testue.CallConfirmed || bc.State() != testue.CallConfirmed {
			t.Fatalf("states %s and %s after the no-answer timeout, want the answered call kept", ac.State(), bc.State())
		}

		if err := ac.Bye(ctx); err != nil {
			t.Fatal(err)
		}

		ended(t, bc, testue.RemoteBye)
	})
}

func TestCallDialling(t *testing.T) {
	sc := newScene(t)
	sc.putOperator(`{"mcc": "001", "mnc": "01", "numbering": {"country_code": "1", "national_prefix": "0", "international_prefix": ""}}`)

	a := sc.caller(0, false, testue.Config{Transport: sip.UDP})
	b := sc.caller(1, false, testue.Config{Transport: sip.UDP})

	for _, tc := range []struct {
		name, target string
	}{
		{"tel URI", subscriberAt(1).tel},
		{"SIP URI", phone(1)},
		{"home-local number", "tel:05550002;phone-context=" + domain},
		{"national number as a SIP URI", "sip:05550002;phone-context=" + domain + "@" + domain + ";user=phone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := sc.in(t).ctx()
			ac, bc := connect(t, ctx, a, b, tc.target, testue.CallOptions{})

			if !hasIdentity(bc.Invite().Header, "P-Called-Party-ID", subscriberAt(1).tel) {
				t.Errorf("P-Called-Party-ID = %q, want %s", bc.Invite().Header.Values("P-Called-Party-ID"), subscriberAt(1).tel)
			}

			if err := ac.Bye(ctx); err != nil {
				t.Fatal(err)
			}

			ended(t, bc, testue.RemoteBye)
		})
	}

	t.Run("oneself", func(t *testing.T) {
		ctx := sc.in(t).ctx()
		ac, bc := connect(t, ctx, a, a, phone(0), testue.CallOptions{Preconditions: true})

		if ac.ID().CallID != bc.ID().CallID || ac.ID().LocalTag == bc.ID().LocalTag {
			t.Fatalf("dialogs %+v and %+v, want both ends of one call", ac.ID(), bc.ID())
		}

		if err := bc.Bye(ctx); err != nil {
			t.Fatal(err)
		}

		ended(t, ac, testue.RemoteBye)
		ended(t, bc, testue.LocalBye)
	})
}
