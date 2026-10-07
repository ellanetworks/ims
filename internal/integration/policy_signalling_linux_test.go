//go:build linux && (amd64 || arm64)

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/testue"
)

// TS 29.214 §4.4.5, TS 29.514 §4.2.6.7
func TestPolicySignallingSession(t *testing.T) {
	forEachPolicy(t, func(t *testing.T, iface string) {
		for _, family := range []struct {
			name string
			v6   bool
		}{{"IPv4", false}, {"IPv6", true}} {
			t.Run(family.name, func(t *testing.T) {
				s := newPolicyScene(t, iface, nil)
				u := s.newUE(family.v6, testue.Config{})

				s.register(u)

				session := s.pol.signalling(ueAddr(0, family.v6))

				eventually(t, "the session stored with the registration", func() bool {
					reg, ok := s.pcscfRegistration()
					return ok && reg.Policy.ID == session.id && (session.uri == "" || reg.Policy.Ref == session.uri)
				})

				if err := u.Deregister(s.ctx()); err != nil {
					t.Fatalf("Deregister: %v", err)
				}

				s.pol.wantEnd(session, rx.TerminationLogout)
			})
		}
	})
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

func TestPolicySessionEndsWithTheNetworkDeregistration(t *testing.T) {
	forEachPolicy(t, func(t *testing.T, iface string) {
		s := newPolicyScene(t, iface, nil)
		u := s.newUE(false, testue.Config{})

		s.register(u)

		session := s.pol.signalling(ueAddr(0, false))

		if _, err := s.hss.RTR(s.ctx(), cx.DeregistrationReason{Code: cx.ReasonPermanentTermination}, impi); err != nil {
			t.Fatal(err)
		}

		s.pol.wantEnd(session, rx.TerminationAdministrative)
	})
}

// TS 29.214 §4.4.6.5, TS 29.514 §4.2.5.10
func TestPolicySignallingPathLost(t *testing.T) {
	forEachPolicy(t, func(t *testing.T, iface string) {
		s := newPolicyScene(t, iface, nil)
		u := s.newUE(false, testue.Config{})

		s.register(u)

		session := s.pol.signalling(ueAddr(0, false))
		s.pol.pathLost(session)

		eventually(t, "the signalling to be marked lost", func() bool {
			reg, ok := s.pcscfRegistration()
			return ok && reg.SignallingLost && reg.Policy.ID == session.id
		})

		s.pol.none(200 * time.Millisecond)
	})
}

// TS 29.214 §4.4.6.1, TS 29.514 §4.2.5.3
func TestPolicySignallingAborted(t *testing.T) {
	forEachPolicy(t, func(t *testing.T, iface string) {
		s := newPolicyScene(t, iface, nil)
		u := s.newUE(false, testue.Config{})

		s.register(u)

		session := s.pol.signalling(ueAddr(0, false))

		if err := s.pol.abort(session); err != nil {
			t.Fatalf("abort: %v", err)
		}

		s.pol.wantEnd(session, rx.TerminationAdministrative)

		eventually(t, "the session to be cleared and the registration kept", func() bool {
			reg, ok := s.pcscfRegistration()
			return ok && reg.SignallingLost && reg.Policy.ID == ""
		})

		if err := s.pol.abort(session); err == nil {
			t.Fatal("the ended session was aborted again")
		}

		if err := u.Reregister(s.ctx()); err != nil {
			t.Fatal(err)
		}

		again := s.pol.signalling(ueAddr(0, false))
		if again.id == session.id {
			t.Fatal("the new registration reused the aborted session")
		}

		eventually(t, "the new session and the signalling restored", func() bool {
			reg, ok := s.pcscfRegistration()
			return ok && !reg.SignallingLost && reg.Policy.ID == again.id
		})
	})
}

// After a restart, the session stored with the registration is ended and a new one opened.
func TestPolicySignallingReopenedAfterRestart(t *testing.T) {
	forEachPolicy(t, func(t *testing.T, iface string) {
		s := newPolicyScene(t, iface, nil)
		u := s.newUE(false, testue.Config{})

		s.register(u)

		session := s.pol.signalling(ueAddr(0, false))

		eventually(t, "the session stored with the registration", func() bool {
			reg, ok := s.pcscfRegistration()
			return ok && reg.Policy.ID == session.id
		})

		s.srv.Shutdown(context.Background())
		s.pol.none(100 * time.Millisecond)

		s.srv = &server.Server{Config: s.srv.Config, Logger: testLogger(t)}
		if err := s.srv.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}

		s.pol.wantEnd(session, rx.TerminationAdministrative)

		again := s.pol.signalling(ueAddr(0, false))
		if again.id == session.id {
			t.Fatal("the restarted P-CSCF reused the session")
		}

		eventually(t, "the new session stored with the registration", func() bool {
			reg, ok := s.pcscfRegistration()
			return ok && reg.Policy.ID == again.id
		})
	})
}
