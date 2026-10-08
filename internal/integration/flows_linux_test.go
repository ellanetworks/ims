//go:build linux && (amd64 || arm64)

package integration

import (
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/testue"
)

const reason480 = `SIP;cause=480;text="Temporarily Unavailable"`

// flow is the callee's registration flow regID from host 1 (RFC 5626 §4.2): the same USIM and IMEI for
// every flow, each with its own security associations (TS 33.203 §6.1).
func (s *scene) flow(regID int64, v6 bool, cfg testue.Config) *testue.UE {
	s.t.Helper()

	cfg.AcceptCalls, cfg.RegID = true, regID

	u := s.newDevice(1, subscriberAt(1), v6, cfg)
	s.register(u)

	if !u.State().Outbound {
		s.t.Fatalf("flow %d registered without outbound", regID)
	}

	return u
}

// noCall checks that u gets no call for a while.
func noCall(t *testing.T, u *testue.UE) {
	t.Helper()

	select {
	case c := <-u.Calls():
		t.Fatalf("unexpected call %s", callID(c))
	case <-time.After(time.Second):
	}
}

// TS 24.229 §5.4.1.2.2 step 6d, §5.4.1.4 step 6a ii, RFC 5626 §7: the flows of a device stay registered
// side by side; a call reaches the most recent, and the others once it deregisters.
func TestFlowsOfOneDevice(t *testing.T) {
	s := newScene(t)
	caller := s.caller(0, false, testue.Config{})
	one := s.flow(1, false, testue.Config{})
	two := s.flow(2, false, testue.Config{})

	// The registrations API shows each flow with the P-CSCF's registration of its own (TS 24.229 §5.2.2.1).
	contacts := s.registrations(subscriberAt(1).imsi).Items[0].Contacts
	if len(contacts) != 2 || contacts[0].RegID == contacts[1].RegID || contacts[0].Address == contacts[1].Address ||
		contacts[0].Address == "" || contacts[1].Address == "" {
		t.Fatalf("contacts %+v, want flows 1 and 2, each at its own address", contacts)
	}

	ctx := s.ctx()

	ac, bc := connect(t, ctx, caller, two, phone(1), testue.CallOptions{})
	noCall(t, one)

	if err := ac.Bye(ctx); err != nil {
		t.Fatal(err)
	}

	ended(t, bc, testue.RemoteBye)

	if err := two.Deregister(ctx); err != nil {
		t.Fatal(err)
	}

	if !one.State().Registered {
		t.Fatal("flow 1 deregistered with flow 2")
	}

	ac, bc = connect(t, ctx, caller, one, phone(1), testue.CallOptions{})

	if err := ac.Bye(ctx); err != nil {
		t.Fatal(err)
	}

	ended(t, bc, testue.RemoteBye)
}

// Two devices under one private identity, each registering a flow, both ring (TS 24.229 §5.4.3.3).
func TestDevicesSharingAPrivateIdentity(t *testing.T) {
	s := newScene(t)
	caller := s.caller(0, false, testue.Config{})
	phoneUE := s.flow(1, false, testue.Config{})
	watch := s.flow(1, false, testue.Config{IMEI: "35693803564399"})

	ctx := s.ctx()
	ac := invite(t, caller, phone(1), testue.CallOptions{})

	pc, wc := incoming(t, phoneUE, ac), incoming(t, watch, ac)

	if err := wc.Answer(ctx); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if res, err := ac.Wait(ctx); err != nil || res.StatusCode != 200 {
		t.Fatalf("Wait = %v, %v, want 200", res, err)
	}

	ended(t, pc, testue.Cancelled)

	if err := ac.Bye(ctx); err != nil {
		t.Fatal(err)
	}

	ended(t, wc, testue.RemoteBye)
}

// TS 24.229 §5.4.1.2.2 step 6d ii: a flow registered again over a new P-CSCF flow, here from another
// address, replaces the old one, whose call is released.
func TestFlowReplacedReleasesItsCall(t *testing.T) {
	s := newScene(t)
	caller := s.caller(0, false, testue.Config{})
	old := s.flow(1, false, testue.Config{})

	ctx := s.ctx()
	ac, _ := connect(t, ctx, caller, old, phone(1), testue.CallOptions{})

	s.flow(1, true, testue.Config{})

	// The old flow's UE drops its security associations once told its contact ended, which can be before
	// the BYE toward it arrives: only the caller's is certain.
	ended(t, ac, testue.RemoteBye)

	if got := reason(t, ac); got != reason480 {
		t.Errorf("caller's BYE Reason %q, want %q", got, reason480)
	}

	// The P-CSCF learns of the replacement from the reg-event NOTIFY, and drops the old flow (TS 24.229 §5.2.5.2).
	eventually(t, "the P-CSCF dropping the replaced flow", func() bool {
		regs := s.pcscfRegistrations()
		return len(regs) == 1 && regs[0].RegID == 1 && regs[0].UEAddress.Addr().Is6()
	})
}

func (s *scene) pcscfRegistrations() []db.PCSCFRegistration {
	s.t.Helper()

	d, err := db.Open(s.t.Context(), s.db)
	if err != nil {
		s.t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	var out []db.PCSCFRegistration

	regs, err := d.ListPCSCFRegistrations(s.t.Context())
	if err != nil {
		s.t.Fatal(err)
	}

	for _, r := range regs {
		if r.IMPI == subscriberAt(1).impi {
			out = append(out, r)
		}
	}

	return out
}
