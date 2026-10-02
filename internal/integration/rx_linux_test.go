//go:build linux && (amd64 || arm64)

package integration

import (
	"testing"

	"github.com/ellanetworks/core/diameter/rx"
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

	return r.SessionID
}

func (s *scene) wantSTR(session string, cause rx.TerminationCause) {
	s.t.Helper()

	if r := s.pcrf.Next(s.t); r.STR == nil || r.SessionID != session || r.STR.Cause != cause {
		s.t.Fatalf("got %s, want the STR of %s with %s", r, session, cause)
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

func TestRxAbortSessionOverIPsec(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{})

	s.register(u)

	session := s.aar(false)

	if _, err := s.pcrf.ASR(s.ctx(), session, rx.AbortBearerReleased); err != nil {
		t.Fatalf("ASR: %v", err)
	}

	s.wantSTR(session, rx.TerminationAdministrative)

	if _, err := s.pcrf.ASR(s.ctx(), session, rx.AbortBearerReleased); err == nil {
		t.Fatal("ASA success for the ended session")
	}
}
