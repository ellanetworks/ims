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

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/testue"
	"github.com/ellanetworks/ims/sip"
)

func (s *scene) caller(i int, v6 bool, tr sip.Transport) *testue.UE {
	s.t.Helper()

	u := s.newUEAt(i, v6, testue.Config{Transport: tr, AcceptCalls: true})
	s.register(u)

	return u
}

func phone(i int) string {
	return subscriberAt(i).msisdn + ";user=phone"
}

func incoming(t *testing.T, u *testue.UE) *testue.Call {
	t.Helper()

	select {
	case c := <-u.Calls():
		return c
	case <-time.After(15 * time.Second):
		t.Fatal("no incoming call")
	}

	return nil
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
	bc := incoming(t, b)

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
}

func seen(c *testue.Call) events {
	var out events

	for {
		select {
		case e := <-c.Events():
			switch {
			case e.Request != nil:
				out.requests = append(out.requests, e.Request.Method)
			case e.Response != nil && e.Response.StatusCode != 100:
				cseq, _ := e.Response.Header.CSeq()
				out.responses = append(out.responses, strconv.Itoa(e.Response.StatusCode)+" "+cseq.Method)
			}
		default:
			return out
		}
	}
}

func set(v []string) string {
	v = slices.Clone(v)
	slices.Sort(v)

	return strings.Join(slices.Compact(v), ",")
}

func hasValue(h sip.Header, name, want string) bool {
	return slices.ContainsFunc(h.Values(name), func(v string) bool { return strings.Contains(v, want) })
}

func absent(t *testing.T, what string, h sip.Header, names ...string) {
	t.Helper()

	for _, name := range names {
		if h.Has(name) {
			t.Errorf("%s carries %s: %q", what, name, h.Values(name))
		}
	}
}

func checkIdentities(t *testing.T, bc *testue.Call, from, to int) {
	t.Helper()

	in := bc.Invite()

	if !hasValue(in.Header, "P-Asserted-Identity", subscriberAt(from).tel) || !hasValue(in.Header, "P-Asserted-Identity", subscriberAt(from).msisdn) {
		t.Errorf("callee's INVITE P-Asserted-Identity = %q, want the caller's SIP and tel URIs", in.Header.Values("P-Asserted-Identity"))
	}

	if !hasValue(in.Header, "P-Called-Party-ID", strings.TrimPrefix(subscriberAt(to).tel, "tel:")) {
		t.Errorf("callee's INVITE P-Called-Party-ID = %q, want the dialled number", in.Header.Values("P-Called-Party-ID"))
	}

	absent(t, "callee's INVITE", in.Header, "P-Charging-Vector", "P-Charging-Function-Addresses", "P-Asserted-Service",
		"P-Preferred-Identity", "P-Served-User", "P-User-Database")

	if rr, _ := in.Header.RecordRoutes(); len(rr) < 4 {
		t.Errorf("callee's INVITE Record-Route = %q, want the P-CSCFs and S-CSCFs", in.Header.Values("Record-Route"))
	}
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
	s := newScene(t)
	a, b := s.caller(0, v6, tr), s.caller(1, v6, tr)

	record := func(flow string) {
		if fixtures {
			s.record(flow)
		}
	}

	t.Run("caller hangs up", func(t *testing.T) {
		record("call_caller_bye")
		defer s.record("")

		ctx := s.ctx()
		ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})

		checkIdentities(t, bc, 0, 1)

		if got, want := set(seen(ac).responses), "180 INVITE,183 INVITE,200 INVITE,200 PRACK"; got != want {
			t.Errorf("caller saw %s, want %s", got, want)
		}

		if got, want := set(seen(bc).requests), "ACK,PRACK"; got != want {
			t.Errorf("callee saw %s, want %s", got, want)
		}

		if err := ac.Refresh(ctx); err != nil {
			t.Fatalf("Refresh: %v", err)
		}

		if err := ac.Bye(ctx); err != nil {
			t.Fatalf("Bye: %v", err)
		}

		ended(t, ac, testue.LocalBye)
		ended(t, bc, testue.RemoteBye)
	})

	t.Run("callee hangs up", func(t *testing.T) {
		record("call_callee_bye")
		defer s.record("")

		ctx := s.ctx()
		ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})

		if err := bc.Refresh(ctx); err != nil {
			t.Fatalf("Refresh: %v", err)
		}

		if err := bc.Bye(ctx); err != nil {
			t.Fatalf("Bye: %v", err)
		}

		ended(t, bc, testue.LocalBye)
		ended(t, ac, testue.RemoteBye)
	})

	t.Run("preconditions", func(t *testing.T) {
		record("call_preconditions")
		defer s.record("")

		ctx := s.ctx()
		ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{Preconditions: true})

		if got, want := set(seen(ac).responses), "180 INVITE,183 INVITE,200 INVITE,200 PRACK,200 UPDATE"; got != want {
			t.Errorf("caller saw %s, want %s", got, want)
		}

		if got, want := set(seen(bc).requests), "ACK,PRACK,UPDATE"; got != want {
			t.Errorf("callee saw %s, want %s", got, want)
		}

		if !ac.PreconditionsMet() || !bc.PreconditionsMet() {
			t.Fatal("preconditions not met")
		}

		if err := ac.Bye(ctx); err != nil {
			t.Fatalf("Bye: %v", err)
		}

		ended(t, bc, testue.RemoteBye)
	})
}

func TestCallFailures(t *testing.T) {
	s := newScene(t)
	a, b := s.caller(0, false, sip.UDP), s.caller(1, false, sip.UDP)

	t.Run("busy", func(t *testing.T) {
		s.record("call_busy")
		defer s.record("")

		ac := invite(t, a, phone(1), testue.CallOptions{})
		bc := incoming(t, b)

		if err := bc.Reject(486); err != nil {
			t.Fatal(err)
		}

		failed(t, s.ctx(), ac, 486)
		ended(t, bc, testue.Rejected)
	})

	t.Run("cancelled", func(t *testing.T) {
		s.record("call_cancel")
		defer s.record("")

		ctx := s.ctx()
		ac := invite(t, a, phone(1), testue.CallOptions{Preconditions: true})
		bc := incoming(t, b)

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
		ac := invite(t, a, phone(1), testue.CallOptions{})
		bc := incoming(t, b)

		if err := ac.Cancel(s.ctx()); err != nil {
			t.Fatal(err)
		}

		ended(t, bc, testue.Cancelled)
		ended(t, ac, testue.Cancelled)
	})

	t.Run("unknown number", func(t *testing.T) {
		failed(t, s.ctx(), invite(t, a, "tel:+15550099", testue.CallOptions{}), 404)
	})

	t.Run("never registered", func(t *testing.T) {
		failed(t, s.ctx(), invite(t, a, subscriberAt(2).tel, testue.CallOptions{}), 480)
	})

	t.Run("unregistered with services", func(t *testing.T) {
		s.hss.Update(subscriberAt(2).impi, func(sub *hsstest.Subscriber) { sub.UnregisteredServices = true })
		defer s.hss.Update(subscriberAt(2).impi, func(sub *hsstest.Subscriber) { sub.UnregisteredServices = false })

		failed(t, s.ctx(), invite(t, a, subscriberAt(2).tel, testue.CallOptions{}), 480)
	})

	t.Run("deregistered", func(t *testing.T) {
		c := s.caller(3, false, sip.UDP)

		if err := c.Deregister(s.ctx()); err != nil {
			t.Fatal(err)
		}

		failed(t, s.ctx(), invite(t, a, subscriberAt(3).tel, testue.CallOptions{}), 480)
	})

	t.Run("MESSAGE", func(t *testing.T) {
		res, err := a.Message(s.ctx(), subscriberAt(1).tel, testue.SMSContentType, []byte{0x00, 0x01})
		if err != nil || res.StatusCode != 403 {
			t.Fatalf("Message = %v, %v, want 403", res, err)
		}
	})
}

func TestCallNotAnswered(t *testing.T) {
	s := newSceneWith(t, func(c *config.Config) { c.SIP.NoAnswerTimeout = 2 * time.Second })
	a, b := s.caller(0, false, sip.UDP), s.caller(1, false, sip.UDP)

	ctx := s.ctx()
	ac := invite(t, a, phone(1), testue.CallOptions{})
	bc := incoming(t, b)

	if err := bc.Ring(ctx); err != nil {
		t.Fatal(err)
	}

	failed(t, ctx, ac, 408)
	ended(t, bc, testue.Cancelled)
}

func TestCallDialling(t *testing.T) {
	s := newSceneWith(t, func(c *config.Config) {
		c.IMS.Numbering = config.Numbering{CountryCode: "1", NationalPrefix: "0"}
	})
	a, b := s.caller(0, false, sip.UDP), s.caller(1, false, sip.UDP)

	for _, tc := range []struct {
		name, target string
	}{
		{"tel URI", subscriberAt(1).tel},
		{"SIP URI", phone(1)},
		{"home-local number", "tel:05550002;phone-context=" + domain},
		{"national number as a SIP URI", "sip:05550002;phone-context=" + domain + "@" + domain + ";user=phone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := s.ctx()
			ac, bc := connect(t, ctx, a, b, tc.target, testue.CallOptions{})

			if !hasValue(bc.Invite().Header, "P-Called-Party-ID", "+15550002") {
				t.Errorf("P-Called-Party-ID = %q, want the callee's number", bc.Invite().Header.Values("P-Called-Party-ID"))
			}

			if err := ac.Bye(ctx); err != nil {
				t.Fatal(err)
			}

			ended(t, bc, testue.RemoteBye)
		})
	}

	t.Run("oneself", func(t *testing.T) {
		ctx := s.ctx()
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
