package server

import (
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
)

func (e *e2e) aar() (string, rx.AARequest) {
	e.t.Helper()

	m := next(e.t, e.rx)
	if m.CommandCode != rx.CommandAA {
		e.t.Fatalf("Rx request %d, want an AAR", m.CommandCode)
	}

	r, err := rx.ParseAARequest(m)
	if err != nil {
		e.t.Fatalf("ParseAARequest: %v", err)
	}

	return tgpp.ParseEnvelope(m).SessionID, r
}

func (e *e2e) wantSTR(session string, cause rx.TerminationCause) {
	e.t.Helper()

	m := next(e.t, e.rx)
	if m.CommandCode != rx.CommandSessionTermination {
		e.t.Fatalf("Rx request %d, want an STR", m.CommandCode)
	}

	r, err := rx.ParseSessionTerminationRequest(m)
	if err != nil {
		e.t.Fatalf("ParseSessionTerminationRequest: %v", err)
	}

	if id := tgpp.ParseEnvelope(m).SessionID; id != session || r.Cause != cause {
		e.t.Fatalf("STR %s %s, want %s %s", id, r.Cause, session, cause)
	}
}

func (e *e2e) noRx(d time.Duration) {
	e.t.Helper()

	select {
	case m := <-e.rx:
		e.t.Fatalf("unexpected Rx request %d", m.CommandCode)
	case <-time.After(d):
	}
}

func (e *e2e) rxEnvelope(session string) tgpp.Envelope {
	env := e.hss.envelope()
	env.SessionID = session

	return env
}

func (e *e2e) setUpRx() string {
	e.t.Helper()

	e.setUp("600")

	id, r := e.aar()

	signalling := rx.FlowUsageAFSignalling
	if r.FramedIPAddress != ueAddr || len(r.MediaComponents) != 1 || r.MediaComponents[0].Number != 0 ||
		len(r.MediaComponents[0].SubComponents) != 1 || r.MediaComponents[0].SubComponents[0].FlowNumber != 0 ||
		*r.MediaComponents[0].SubComponents[0].FlowUsage != signalling ||
		!slices.Equal(r.SpecificActions, []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer}) {
		e.t.Fatalf("AAR = %+v, want the AF signalling subscription for %s", r, ueAddr)
	}

	eventually(e.t, "the Rx session on the P-CSCF's registration", func() bool {
		regs := e.pcscfRegistrations()
		return len(regs) == 1 && regs[0].RxSessionID == id
	})

	return id
}

func TestRxSessionFollowsTheRegistration(t *testing.T) {
	e := newE2E(t)
	id := e.setUpRx()

	e.protectedRegister("600", digestAuth("", false))
	e.noRx(200 * time.Millisecond)

	e.protectedRegister("0", digestAuth("", false))
	e.wantSTR(id, rx.TerminationLogout)
}

func TestRxSessionEndsWithTheNetworkDeregistration(t *testing.T) {
	e := newE2E(t)
	id := e.setUpRx()

	rtr, err := cx.NewRegistrationTerminationRequest(e.hss.envelope(), cx.RegistrationTerminationRequest{
		PrivateIdentity: e2eIMPI,
		Reason:          cx.DeregistrationReason{Code: cx.ReasonPermanentTermination},
	})

	if r := e.hss.send(t, rtr, err); r.Code != diameter.ResultSuccess {
		t.Fatalf("RTA result = %s", r)
	}

	e.notified()
	e.wantSTR(id, rx.TerminationAdministrative)
}

func TestRxReAuth(t *testing.T) {
	e := newE2E(t)
	id := e.setUpRx()

	rar, err := rx.NewReAuthRequest(e.rxEnvelope(id), rx.ReAuthRequest{
		SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfReleaseOfBearer},
	})

	if r := e.hss.send(t, rar, err); r.Code != diameter.ResultSuccess || r.Experimental {
		t.Fatalf("RAA result = %s, want DIAMETER_SUCCESS", r)
	}

	eventually(t, "the signalling to be marked lost", func() bool {
		regs := e.pcscfRegistrations()
		return len(regs) == 1 && regs[0].SignallingLost && regs[0].RxSessionID == id
	})

	e.noRx(200 * time.Millisecond)

	rar, err = rx.NewReAuthRequest(e.rxEnvelope(id), rx.ReAuthRequest{
		SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer},
	})
	if err != nil {
		t.Fatal(err)
	}

	rar.AVPs = slices.DeleteFunc(rar.AVPs, func(a diameter.AVP) bool {
		return a.Code == diameter.AVPDestinationHost && a.VendorID == 0
	})

	if r := e.hss.send(t, rar, nil); r.Code != diameter.ResultMissingAVP {
		t.Fatalf("RAA result without Destination-Host = %s, want DIAMETER_MISSING_AVP", r)
	}
}

func TestRxAbortSession(t *testing.T) {
	e := newE2E(t)
	id := e.setUpRx()

	asr, err := rx.NewAbortSessionRequest(e.rxEnvelope(id), rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
	if r := e.hss.send(t, asr, err); r.Code != diameter.ResultSuccess || r.Experimental {
		t.Fatalf("ASA result = %s, want DIAMETER_SUCCESS", r)
	}

	e.wantSTR(id, rx.TerminationAdministrative)

	eventually(t, "the session to be cleared and the registration kept", func() bool {
		regs := e.pcscfRegistrations()
		return len(regs) == 1 && regs[0].SignallingLost && regs[0].RxSessionID == ""
	})

	asr, err = rx.NewAbortSessionRequest(e.rxEnvelope(id), rx.AbortSessionRequest{Cause: rx.AbortBearerReleased})
	if r := e.hss.send(t, asr, err); r.Code != diameter.ResultUnknownSessionID {
		t.Fatalf("ASA result for the ended session = %s, want DIAMETER_UNKNOWN_SESSION_ID", r)
	}

	e.protectedRegister("600", digestAuth("", false))

	again, _ := e.aar()
	if again == id {
		t.Fatal("the new registration reused the aborted session")
	}

	eventually(t, "the new session and the signalling restored", func() bool {
		regs := e.pcscfRegistrations()
		return len(regs) == 1 && !regs[0].SignallingLost && regs[0].RxSessionID == again
	})
}
