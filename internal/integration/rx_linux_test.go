//go:build linux && (amd64 || arm64)

package integration

import (
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/testue"
)

func (s *scene) aar(v6 bool) string {
	s.t.Helper()

	r := s.pcrf.Next(s.t)
	if r.AAR == nil {
		s.t.Fatalf("got %s, want the AAR", r)
	}

	ue := ueAddrs[0].Addr()
	got := r.AAR.FramedIPAddress

	if v6 {
		ue = ueAddrs[1].Addr()
		got = r.AAR.FramedIPv6Address
	}

	if got != ue {
		s.t.Fatalf("AAR %s, want the UE's address %s", r, ue)
	}

	mc := r.AAR.MediaComponents
	if len(mc) != 1 || mc[0].Number != 0 || len(mc[0].SubComponents) != 1 || mc[0].SubComponents[0].FlowNumber != 0 ||
		mc[0].SubComponents[0].FlowUsage == nil || *mc[0].SubComponents[0].FlowUsage != rx.FlowUsageAFSignalling ||
		!slices.Equal(r.AAR.SpecificActions, []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer, rx.ActionIndicationOfReleaseOfBearer}) {
		s.t.Fatalf("AAR %s, want the AF signalling subscription", r)
	}

	return r.SessionID
}

func (s *scene) wantSTR(session string, cause rx.TerminationCause) {
	s.t.Helper()

	r := s.pcrf.Next(s.t)
	if r.STR == nil || r.SessionID != session || r.STR.Cause != cause {
		s.t.Fatalf("got %s, want the STR of %s with %s", r, session, cause)
	}

	if len(r.STR.Class) != 1 || string(r.STR.Class[0]) != session {
		s.t.Fatalf("STR Class = %q, want the AAA's %q", r.STR.Class, session)
	}
}

func TestRxSessionOverIPsec(t *testing.T) {
	for _, family := range []struct {
		name string
		v6   bool
	}{{"IPv4", false}, {"IPv6", true}} {
		t.Run(family.name, func(t *testing.T) {
			s := newScene(t)
			u := s.newUE(family.v6, testue.Config{})

			s.register(u)

			session := s.aar(family.v6)

			if err := u.Deregister(s.ctx()); err != nil {
				t.Fatalf("Deregister: %v", err)
			}

			s.wantSTR(session, rx.TerminationLogout)
		})
	}
}

func (s *scene) pcscfRegistration() (db.PCSCFRegistration, bool) {
	s.t.Helper()

	d, err := db.Open(s.t.Context(), s.db)
	if err != nil {
		s.t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	regs, err := d.ListPCSCFRegistrations(s.t.Context())
	if err != nil {
		s.t.Fatal(err)
	}

	if len(regs) != 1 {
		return db.PCSCFRegistration{}, false
	}

	return regs[0], true
}

func (s *scene) noRx(d time.Duration) {
	s.t.Helper()

	select {
	case r := <-s.pcrf.Requests():
		s.t.Fatalf("unexpected %s", r)
	case <-time.After(d):
	}
}

func TestRxSessionEndsWithTheNetworkDeregistration(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{})

	s.register(u)

	session := s.aar(false)

	if _, err := s.hss.RTR(s.ctx(), cx.DeregistrationReason{Code: cx.ReasonPermanentTermination}, impi); err != nil {
		t.Fatal(err)
	}

	s.wantSTR(session, rx.TerminationAdministrative)
}

func TestRxReAuthOverIPsec(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{})

	s.register(u)

	session := s.aar(false)

	ans, err := s.pcrf.RAR(s.ctx(), session, rx.ActionIndicationOfReleaseOfBearer)
	if err != nil || ans.Result.Code != diameter.ResultSuccess || ans.Result.Experimental {
		t.Fatalf("RAA = %+v, %v, want DIAMETER_SUCCESS", ans, err)
	}

	eventually(t, "the signalling to be marked lost", func() bool {
		reg, ok := s.pcscfRegistration()
		return ok && reg.SignallingLost && reg.RxSessionID == session
	})

	s.noRx(200 * time.Millisecond)
}

func TestRxAbortSessionOverIPsec(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{})

	s.register(u)

	session := s.aar(false)

	if _, err := s.pcrf.ASR(s.ctx(), session, rx.AbortBearerReleased); err != nil {
		t.Fatalf("ASR: %v", err)
	}

	s.wantSTR(session, rx.TerminationAdministrative)

	eventually(t, "the session to be cleared and the registration kept", func() bool {
		reg, ok := s.pcscfRegistration()
		return ok && reg.SignallingLost && reg.RxSessionID == ""
	})

	if _, err := s.pcrf.ASR(s.ctx(), session, rx.AbortBearerReleased); err == nil {
		t.Fatal("ASA success for the ended session")
	}

	if err := u.Reregister(s.ctx()); err != nil {
		t.Fatal(err)
	}

	again := s.aar(false)
	if again == session {
		t.Fatal("the new registration reused the aborted session")
	}

	eventually(t, "the new session and the signalling restored", func() bool {
		reg, ok := s.pcscfRegistration()
		return ok && !reg.SignallingLost && reg.RxSessionID == again
	})
}
