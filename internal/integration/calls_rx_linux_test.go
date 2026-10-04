//go:build linux && (amd64 || arm64)

package integration

import (
	"cmp"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/pcrftest"
	"github.com/ellanetworks/ims/internal/testue"
)

type mediaSession struct {
	ue   netip.Addr
	aars []rx.AARequest
	str  *rx.SessionTerminationRequest
}

// mediaRx reads the PCRF's requests until every call session it saw has
// ended and want sessions were seen, skipping the IMS signalling sessions.
func (s *scene) mediaRx(want int) map[string]*mediaSession {
	s.t.Helper()

	sessions := map[string]*mediaSession{}
	signalling := map[string]bool{}
	deadline := time.After(15 * time.Second)

	done := func() bool {
		if len(sessions) < want {
			return false
		}

		for _, c := range sessions {
			if c.str == nil {
				return false
			}
		}

		return true
	}

	for !done() {
		var r pcrftest.Request

		select {
		case r = <-s.pcrf.Requests():
		case <-deadline:
			s.t.Fatalf("saw call sessions %+v, want %d ended", sessions, want)
		}

		switch {
		case r.AAR != nil && pcrftest.Signalling(*r.AAR):
			signalling[r.SessionID] = true
		case r.AAR != nil:
			c := sessions[r.SessionID]
			if c == nil {
				c = &mediaSession{ue: r.AAR.FramedIPAddress}
				if !c.ue.IsValid() {
					c.ue = r.AAR.FramedIPv6Address
				}

				sessions[r.SessionID] = c
			}

			c.aars = append(c.aars, *r.AAR)
		case r.STR != nil && !signalling[r.SessionID]:
			c := sessions[r.SessionID]
			if c == nil {
				s.t.Fatalf("STR for the unknown session %s", r.SessionID)
			}

			c.str = r.STR
		}
	}

	return sessions
}

func ueAddr(i int, v6 bool) netip.Addr {
	if v6 {
		return ueAddrsAt(i)[1].Addr()
	}

	return ueAddrsAt(i)[0].Addr()
}

// TS 29.214 Annex A.1, TS 29.213 Annex B.2.1, B.4.1
func TestCallRxSessions(t *testing.T) {
	for _, family := range []struct {
		name string
		v6   bool
	}{{"IPv4", false}, {"IPv6", true}} {
		t.Run(family.name, func(t *testing.T) {
			s := newScene(t)
			a := s.caller(0, family.v6, testue.Config{})
			b := s.caller(1, family.v6, testue.Config{})

			ctx := s.ctx()
			ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{Preconditions: true})
			await(t, ac, "", "180 INVITE,183 INVITE,200 INVITE,200 PRACK,200 UPDATE")

			if err := ac.Bye(ctx); err != nil {
				t.Fatalf("Bye: %v", err)
			}

			ended(t, bc, testue.RemoteBye)

			sessions := s.mediaRx(2)
			if len(sessions) != 2 {
				t.Fatalf("call sessions %+v, want one per UE", sessions)
			}

			var ues []netip.Addr

			for id, c := range sessions {
				ues = append(ues, c.ue)

				first := c.aars[0]
				if first.RequestType == nil || *first.RequestType != rx.RequestInitial || len(first.SpecificActions) != 4 {
					t.Errorf("%s: first AAR %+v, want INITIAL with the bearer events", id, first)
				}

				if len(c.aars) < 2 {
					t.Errorf("%s: %d AARs, want the 183 and the UPDATE answers", id, len(c.aars))
				}

				for _, r := range c.aars[1:] {
					if r.RequestType == nil || *r.RequestType != rx.RequestUpdate || len(r.SpecificActions) != 0 {
						t.Errorf("%s: later AAR %+v, want UPDATE without Specific-Action", id, r)
					}
				}

				for _, r := range c.aars {
					if ue := cmp.Or(r.FramedIPAddress, r.FramedIPv6Address); ue != c.ue {
						t.Errorf("%s: AAR for %s, want every AAR bound to %s", id, ue, c.ue)
					}

					mc := r.MediaComponents
					if len(mc) != 1 || mc[0].Type == nil || *mc[0].Type != rx.MediaAudio || len(mc[0].SubComponents) != 2 ||
						mc[0].MaxRequestedBandwidthUL == nil || mc[0].MaxRequestedBandwidthDL == nil {
						t.Fatalf("%s: Media-Component-Description %+v, want the audio with RTP, RTCP and bandwidth", id, mc)
					}

					source := c.ue.String()
					if family.v6 {
						p, _ := c.ue.Prefix(64)
						source = p.String()
					}

					for _, d := range mc[0].SubComponents[0].FlowDescriptions {
						if f, err := rx.ParseFlowDescription(d); err != nil ||
							f.Direction == rx.FlowDirectionIn && f.Source.String() != source && f.Source.Addr().String() != source ||
							f.Direction == rx.FlowDirectionOut && f.Destination.Addr() != c.ue {
							t.Errorf("%s: flow %q, want the UE %s on its side", id, d, c.ue)
						}
					}
				}

				if c.str.Cause != rx.TerminationLogout {
					t.Errorf("%s: STR cause %s, want %s", id, c.str.Cause, rx.TerminationLogout)
				}
			}

			slices.SortFunc(ues, netip.Addr.Compare)

			want := []netip.Addr{ueAddr(0, family.v6), ueAddr(1, family.v6)}

			if !slices.Equal(ues, want) {
				t.Errorf("sessions for %v, want %v", ues, want)
			}
		})
	}
}

// TS 24.229 §5.2.7.2, TS 29.214 §4.4.1
func TestCallMediaRefused(t *testing.T) {
	s := newScene(t)
	a := s.caller(0, false, testue.Config{})
	b := s.caller(1, false, testue.Config{})

	s.pcrf.RefuseMedia(tgpp.Result{Code: tgpp.ResultRequestedServiceNotAuthorized, Experimental: true, VendorID: tgpp.VendorID})

	ctx := s.ctx()
	ac := invite(t, a, phone(1), testue.CallOptions{Preconditions: true})
	bc := incoming(t, b, ac)

	go func() { _ = bc.Ring(ctx) }()

	failed(t, ctx, ac, 500)
	ended(t, bc, testue.Cancelled)

	s.noMediaSTR(200 * time.Millisecond)
}

// noMediaSTR fails on any STR for a call session within d.
func (s *scene) noMediaSTR(d time.Duration) {
	s.t.Helper()

	signalling := map[string]bool{}
	deadline := time.After(d)

	for {
		select {
		case r := <-s.pcrf.Requests():
			switch {
			case r.AAR != nil && pcrftest.Signalling(*r.AAR):
				signalling[r.SessionID] = true
			case r.STR != nil && !signalling[r.SessionID]:
				s.t.Fatalf("unexpected STR %s", r)
			}
		case <-deadline:
			return
		}
	}
}

// wantMediaSTR waits for the STR of session, skipping other requests.
func (s *scene) wantMediaSTR(session string, cause rx.TerminationCause) {
	s.t.Helper()

	deadline := time.After(15 * time.Second)

	for {
		select {
		case r := <-s.pcrf.Requests():
			if r.STR != nil && r.SessionID == session {
				if r.STR.Cause != cause {
					s.t.Fatalf("STR %s, want %s", r, cause)
				}

				return
			}
		case <-deadline:
			s.t.Fatalf("no STR for %s", session)
		}
	}
}

// firstMediaAAR waits for the first media AAR for the UE at addr.
func (s *scene) firstMediaAAR(addr netip.Addr) string {
	s.t.Helper()

	deadline := time.After(15 * time.Second)

	for {
		select {
		case r := <-s.pcrf.Requests():
			if r.AAR == nil || pcrftest.Signalling(*r.AAR) {
				continue
			}

			if r.AAR.FramedIPAddress == addr || r.AAR.FramedIPv6Address == addr {
				return r.SessionID
			}
		case <-deadline:
			s.t.Fatalf("no media AAR for %s", addr)
		}
	}
}

// TS 24.229 §5.2.8.1.2, TS 29.214 §4.4.6.1
func TestCallAbortedByThePCRF(t *testing.T) {
	s := newScene(t)
	a := s.caller(0, false, testue.Config{})
	b := s.caller(1, false, testue.Config{})

	ctx := s.ctx()
	ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})
	session := s.firstMediaAAR(ueAddr(0, false))

	if _, err := s.pcrf.ASR(ctx, session, rx.AbortBearerReleased); err != nil {
		t.Fatalf("ASR: %v", err)
	}

	ended(t, bc, testue.RemoteBye)

	s.wantMediaSTR(session, rx.TerminationAdministrative)

	if ac.State() != testue.CallConfirmed {
		t.Errorf("caller's call %s, want it left to the UE that lost its bearer", ac.State())
	}
}

// TS 24.229 §5.2.8.1.2, TS 29.214 §4.4.6.2
func TestCallMediaBearerLost(t *testing.T) {
	s := newSceneWith(t, func(c *config.Config) { c.PCSCF.MediaLossTimeout = 100 * time.Millisecond })
	a := s.caller(0, false, testue.Config{})
	b := s.caller(1, false, testue.Config{})

	ctx := s.ctx()
	ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})
	session := s.firstMediaAAR(ueAddr(1, false))

	if _, err := s.pcrf.ReAuth(ctx, session, rx.ReAuthRequest{
		SpecificActions: []rx.SpecificAction{rx.ActionIndicationOfLossOfBearer},
		Flows:           []rx.Flows{{MediaComponentNumber: 1}},
	}); err != nil {
		t.Fatalf("RAR: %v", err)
	}

	ended(t, ac, testue.RemoteBye)

	if bc.State() != testue.CallConfirmed {
		t.Errorf("callee's call %s, want it left to the UE that lost its bearer", bc.State())
	}
}
