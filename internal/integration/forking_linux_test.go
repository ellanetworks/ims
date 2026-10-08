//go:build linux && (amd64 || arm64)

package integration

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/testue"
)

const groupNoAnswer = 2 * time.Second

// devices is a caller, and the callee's phone and second device on the same number, all
// registered.
type devices struct {
	s             *scene
	caller        *testue.UE
	phone, second *testue.UE
}

// twoDevices gives the callee's number a second device with its own USIM, such as a watch: a
// private identity of its own whose implicit registration set holds the callee's number. Both
// register; a call to the number reaches the contacts of both (TS 24.229 §5.4.3.3 step 8).
//
// Two devices sharing one private identity need the multiple registration mechanism (RFC 5626);
// without it, a new contact replaces the previous one (TS 24.229 §5.4.1.2.2 step 4A).
func twoDevices(t *testing.T, s *scene, secondCfg testue.Config) devices {
	t.Helper()

	callee, second := subscriberAt(1), subscriberAt(4)

	s.hss.Add(hsstest.Subscriber{
		IMPI: second.impi, IMSI: second.imsi, K: testK, OPc: testOPc, SQN: 32,
		IMPUs: []cx.ProfileIdentity{{Identity: "sip:" + second.impi, Barred: true}, {Identity: callee.msisdn}, {Identity: callee.tel}},
	})

	d := devices{s: s, caller: s.caller(0, false, testue.Config{})}

	secondCfg.AcceptCalls = true

	d.phone = s.newDevice(1, callee, false, testue.Config{AcceptCalls: true})
	s.register(d.phone)

	d.second = s.newDevice(4, second, false, secondCfg)
	s.register(d.second)

	return d
}

// ringing has both devices take the call and ring.
func (d devices) ringing(t *testing.T, ctx context.Context, ac *testue.Call) (*testue.Call, *testue.Call) {
	t.Helper()

	pc, sc := incoming(t, d.phone, ac), incoming(t, d.second, ac)

	for _, c := range []*testue.Call{pc, sc} {
		if err := c.Ring(ctx); err != nil {
			t.Fatalf("Ring: %v", err)
		}
	}

	eventually(t, "two early dialogs at the caller", func() bool { return len(ac.EarlyDialogs()) == 2 })

	return pc, sc
}

func reason(t *testing.T, c *testue.Call) string {
	t.Helper()

	rs := c.Reasons()
	if len(rs) == 0 {
		return ""
	}

	return rs[0].String()
}

const completedElsewhere = `SIP;cause=200;text="Call completed elsewhere"`

// TS 23.228 §4.2.7.2, TS 24.229 §5.4.3.3, RFC 3261 §16.7
func TestRingAllDevices(t *testing.T) {
	sc := newScene(t)
	d := twoDevices(t, sc, testue.Config{})

	t.Run("first answer wins", func(t *testing.T) {
		s := sc.in(t)

		s.record("call_forked")
		defer s.record("")

		ctx := s.ctx()
		ac := invite(t, d.caller, phone(1), testue.CallOptions{})
		pc, sc := d.ringing(t, ctx, ac)

		if err := sc.Answer(ctx); err != nil {
			t.Fatalf("Answer: %v", err)
		}

		if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
			t.Fatalf("Wait = %v, %v, want 200", res, err)
		}

		ended(t, pc, testue.Cancelled)

		if got := reason(t, pc); got != completedElsewhere {
			t.Errorf("the phone's CANCEL Reason %q, want %q", got, completedElsewhere)
		}

		if ac.ID().RemoteTag != sc.ID().LocalTag {
			t.Errorf("caller in the dialog with %q, want the second device %q", ac.ID().RemoteTag, sc.ID().LocalTag)
		}

		if err := ac.Bye(ctx); err != nil {
			t.Fatal(err)
		}

		ended(t, sc, testue.RemoteBye)
	})

	t.Run("busy and answered", func(t *testing.T) {
		s := sc.in(t)
		ctx := s.ctx()
		ac := invite(t, d.caller, phone(1), testue.CallOptions{})
		pc, sc := d.ringing(t, ctx, ac)

		if err := pc.Reject(486); err != nil {
			t.Fatal(err)
		}

		// RFC 6228 §6: the caller learns, with a 199 for the phone's early dialog, that it ended, while the other
		// device rings on. The S-CSCF's Reason does not reach the UE: the P-CSCF removes it from responses leaving
		// the trust domain (TS 24.229 §4.4.7).
		r := next199(t, ac)
		if to, _ := r.Header.To(); to.Tag() != pc.ID().LocalTag || r.Header.Has("Reason") {
			t.Errorf("199 to %q with Reason %q, want the phone's %q without", to.Tag(), r.Header.Get("Reason"), pc.ID().LocalTag)
		}

		eventually(t, "one early dialog at the caller", func() bool { return len(ac.EarlyDialogs()) == 1 })

		if got := ac.EarlyDialogs()[0].RemoteTag; got != sc.ID().LocalTag || ac.State() != testue.CallEarly {
			t.Fatalf("caller %s with the early dialog of %q, want early with the second device's %q", ac.State(), got, sc.ID().LocalTag)
		}

		if err := sc.Answer(ctx); err != nil {
			t.Fatalf("Answer: %v", err)
		}

		if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
			t.Fatalf("Wait = %v, %v, want 200", res, err)
		}

		if err := ac.Bye(ctx); err != nil {
			t.Fatal(err)
		}

		ended(t, sc, testue.RemoteBye)
	})

	t.Run("declined", func(t *testing.T) {
		s := sc.in(t)
		ctx := s.ctx()
		ac := invite(t, d.caller, phone(1), testue.CallOptions{})
		pc, sc := d.ringing(t, ctx, ac)

		if err := pc.Reject(603); err != nil {
			t.Fatal(err)
		}

		failed(t, ctx, ac, 603)
		ended(t, sc, testue.Cancelled)

		if got, want := reason(t, sc), `SIP;cause=603;text="Decline"`; got != want {
			t.Errorf("CANCEL Reason %q, want %q", got, want)
		}
	})

	t.Run("all busy", func(t *testing.T) {
		s := sc.in(t)
		ctx := s.ctx()
		ac := invite(t, d.caller, phone(1), testue.CallOptions{})
		pc, sc := d.ringing(t, ctx, ac)

		for _, c := range []*testue.Call{pc, sc} {
			if err := c.Reject(486); err != nil {
				t.Fatal(err)
			}
		}

		failed(t, ctx, ac, 486)
	})

	t.Run("cancelled by the caller", func(t *testing.T) {
		s := sc.in(t)
		ctx := s.ctx()
		ac := invite(t, d.caller, phone(1), testue.CallOptions{})
		pc, sc := d.ringing(t, ctx, ac)

		if err := ac.Cancel(ctx); err != nil {
			t.Fatal(err)
		}

		ended(t, ac, testue.Cancelled)
		ended(t, pc, testue.Cancelled)
		ended(t, sc, testue.Cancelled)
	})

	t.Run("no-fork", func(t *testing.T) {
		s := sc.in(t)
		ctx := s.ctx()
		ac := invite(t, d.caller, phone(1), testue.CallOptions{Headers: []sip.Field{{Name: "Request-Disposition", Value: "no-fork"}}})

		// The second device registered last.
		sc := incoming(t, d.second, ac)

		select {
		case c := <-d.phone.Calls():
			t.Fatalf("the phone got %s", callID(c))
		case <-time.After(500 * time.Millisecond):
		}

		if err := sc.Reject(486); err != nil {
			t.Fatal(err)
		}

		failed(t, ctx, ac, 486)
	})
}

// Both devices answer at once: the caller keeps one call, and ends the other dialog (RFC 3261
// §13.2.2.4). Every QoS session of the call ends with it.
func TestRingAllDevicesAnsweredTwice(t *testing.T) {
	forEachPolicy(t, func(t *testing.T, iface string) {
		s := newPolicyScene(t, iface, nil)
		d := twoDevices(t, s, testue.Config{})

		ctx := s.ctx()
		ac := invite(t, d.caller, phone(1), testue.CallOptions{Preconditions: true})
		pc, sc := d.ringing(t, ctx, ac)

		answers := make(chan error, 2)

		for _, c := range []*testue.Call{pc, sc} {
			go func() { answers <- c.Answer(ctx) }()
		}

		if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
			t.Fatalf("Wait = %v, %v, want 200", res, err)
		}

		// The device that lost may find its call cancelled before or while it answers.
		for range 2 {
			if err := <-answers; err != nil {
				t.Logf("Answer: %v", err)
			}
		}

		kept, lost := pc, sc
		if ac.ID().RemoteTag == sc.ID().LocalTag {
			kept, lost = sc, pc
		}

		select {
		case <-lost.Done():
		case <-time.After(15 * time.Second):
			t.Fatal("the device that lost the call still has it")
		}

		if lost.End() != testue.Cancelled && lost.End() != testue.RemoteBye {
			t.Errorf("the device that lost the call ended by %s", lost.End())
		}

		if kept.State() != testue.CallConfirmed || ac.State() != testue.CallConfirmed {
			t.Fatalf("states %s and %s, want the call kept", ac.State(), kept.State())
		}

		if err := ac.Bye(ctx); err != nil {
			t.Fatal(err)
		}

		ended(t, kept, testue.RemoteBye)

		// The caller's, and one for each device that rang.
		if sessions := s.pol.calls(3); len(sessions) != 3 {
			t.Fatalf("call sessions %+v, want three", sessions)
		}
	})
}

// Devices with a lower q-value, or matching the caller's preferences less, ring once the others
// rang for a while (TS 23.228 §4.2.7.2, RFC 3841 §7.2.4).
func TestRingDevicesInTurn(t *testing.T) {
	configure := func(srv *server.Server) { srv.GroupNoAnswer = groupNoAnswer }

	t.Run("q-value", func(t *testing.T) {
		s := newSceneWith(t, configure)
		d := twoDevices(t, s, testue.Config{ContactParams: sip.Params{{Name: "q", Value: "0.5"}}})

		turn(t, s, d, testue.CallOptions{}, d.phone, d.second)
	})

	// RFC 3841 §7.2.4: a preference without "require" orders the devices of a q-value, without splitting them:
	// a video call rings the voice-only device together with the video one.
	t.Run("caller preferences", func(t *testing.T) {
		s := newSceneWith(t, configure)
		d := twoDevices(t, s, testue.Config{ContactParams: sip.Params{{Name: "video"}}})

		video := testue.CallOptions{Headers: []sip.Field{{Name: "Accept-Contact", Value: "*;+g.3gpp.icsi-ref=" + icsiMMTelParam + ";video"}}}

		ctx := s.ctx()
		ac := invite(t, d.caller, phone(1), video)
		pc, vc := d.ringing(t, ctx, ac)

		if err := vc.Answer(ctx); err != nil {
			t.Fatalf("Answer: %v", err)
		}

		if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
			t.Fatalf("Wait = %v, %v, want 200", res, err)
		}

		ended(t, pc, testue.Cancelled)
	})
}

const icsiMMTelParam = `"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`

// turn calls the devices' number: first rings alone, then gives way to then, which answers.
func turn(t *testing.T, s *scene, d devices, opts testue.CallOptions, first, then *testue.UE) {
	t.Helper()

	ctx := s.ctx()
	start := time.Now()
	ac := invite(t, d.caller, phone(1), opts)

	fc := incoming(t, first, ac)

	if err := fc.Ring(ctx); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the first device's early dialog at the caller", func() bool { return len(ac.EarlyDialogs()) == 1 })

	tc := incoming(t, then, ac)

	if elapsed := time.Since(start); elapsed < groupNoAnswer {
		t.Errorf("the second group rang after %s, want %s", elapsed, groupNoAnswer)
	}

	ended(t, fc, testue.Cancelled)
	eventually(t, "the first device's early dialog ended at the caller", func() bool { return len(ac.EarlyDialogs()) == 0 })

	if err := tc.Answer(ctx); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v, want 200", res, err)
	}

	if err := ac.Bye(ctx); err != nil {
		t.Fatal(err)
	}

	ended(t, tc, testue.RemoteBye)
}

// The API lists each identity of a shared number with the other identities it reaches, and each contact with the
// q-value it rings by.
func TestListSharedNumber(t *testing.T) {
	s := newScene(t)
	twoDevices(t, s, testue.Config{ContactParams: sip.Params{{Name: "q", Value: "0.5"}}})

	callee, second := subscriberAt(1), subscriberAt(4)

	got := s.registrations(strings.TrimPrefix(callee.tel, "tel:"))
	if len(got.Items) != 2 {
		t.Fatalf("registrations = %+v, want the callee's and the second device's", got)
	}

	for _, reg := range got.Items {
		other, q := second.impi, 1.0
		if reg.IMPI == second.impi {
			other, q = callee.impi, 0.5
		}

		for _, id := range reg.Identities {
			want := []string{other}
			if id.Barred {
				want = []string{}
			}

			if !slices.Equal(id.RegisteredWith, want) {
				t.Errorf("%s: %s registered with %v, want %v", reg.IMPI, id.URI, id.RegisteredWith, want)
			}
		}

		if len(reg.Contacts) != 1 || reg.Contacts[0].Q != q {
			t.Errorf("%s: contacts %+v, want one with q %v", reg.IMPI, reg.Contacts, q)
		}
	}
}

// next199 waits for the next 199 (Early Dialog Terminated) the caller receives.
func next199(t *testing.T, c *testue.Call) *sip.Response {
	t.Helper()

	deadline := time.After(15 * time.Second)

	for {
		select {
		case e := <-c.Events():
			if e.Response != nil && e.Response.StatusCode == 199 {
				return e.Response
			}
		case <-deadline:
			t.Fatal("no 199 at the caller")
			return nil
		}
	}
}
