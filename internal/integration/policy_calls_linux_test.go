//go:build linux && (amd64 || arm64)

package integration

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/testue"
)

func ueAddr(i int, v6 bool) netip.Addr {
	if v6 {
		return ueAddrsAt(i)[1].Addr()
	}

	return ueAddrsAt(i)[0].Addr()
}

// TS 29.214 Annex A.1, TS 29.213 Annex B.2.1, B.4.1, TS 29.514 §4.2.2.2, §4.2.3.2, TS 29.513 §7.2.3
func TestCallPolicySessions(t *testing.T) {
	forEachPolicy(t, func(t *testing.T, iface string) {
		for _, family := range []struct {
			name string
			v6   bool
		}{{"IPv4", false}, {"IPv6", true}} {
			t.Run(family.name, func(t *testing.T) {
				s := newPolicyScene(t, iface, nil)
				a := s.caller(0, family.v6, testue.Config{})
				b := s.caller(1, family.v6, testue.Config{})

				ctx := s.ctx()
				ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{Preconditions: true})
				await(t, ac, "", "180 INVITE,183 INVITE,200 INVITE,200 PRACK,200 UPDATE")

				if err := ac.Bye(ctx); err != nil {
					t.Fatalf("Bye: %v", err)
				}

				ended(t, bc, testue.RemoteBye)

				sessions := s.pol.calls(2)
				if len(sessions) != 2 {
					t.Fatalf("call sessions %+v, want one per UE", sessions)
				}

				// Over N5, a PATCH that would change nothing is not sent: the UPDATE answer may add no request.
				requests := 2
				if iface == policyN5 {
					requests = 1
				}

				var ues []netip.Addr

				for id, c := range sessions {
					ues = append(ues, c.ue)

					if len(c.requests) < requests {
						t.Errorf("%s: %d requests, want at least %d", id, len(c.requests), requests)
					}

					for _, r := range c.requests {
						if r.ue != c.ue {
							t.Errorf("%s: request for %s, want every request bound to %s", id, r.ue, c.ue)
						}

						if !r.audio || r.subs != 2 || !r.bandwidth {
							t.Fatalf("%s: media %+v, want the audio with RTP, RTCP and bandwidth", id, r)
						}

						source := c.ue.String()
						if family.v6 {
							p, _ := c.ue.Prefix(64)
							source = p.String()
						}

						for _, d := range r.flows {
							if f, err := rx.ParseFlowDescription(d); err != nil ||
								f.Direction == rx.FlowDirectionIn && f.Source.String() != source && f.Source.Addr().String() != source ||
								f.Direction == rx.FlowDirectionOut && f.Destination.Addr() != c.ue {
								t.Errorf("%s: flow %q, want the UE %s on its side", id, d, c.ue)
							}
						}
					}
				}

				slices.SortFunc(ues, netip.Addr.Compare)

				want := []netip.Addr{ueAddr(0, family.v6), ueAddr(1, family.v6)}

				if !slices.Equal(ues, want) {
					t.Errorf("sessions for %v, want %v", ues, want)
				}
			})
		}
	})
}

// TS 24.229 §5.2.7.2, TS 29.214 §4.4.1, TS 29.514 §4.2.2.2
func TestCallMediaRefused(t *testing.T) {
	forEachPolicy(t, func(t *testing.T, iface string) {
		s := newPolicyScene(t, iface, nil)
		a := s.caller(0, false, testue.Config{})
		b := s.caller(1, false, testue.Config{})

		s.pol.refuseMedia()

		ctx := s.ctx()
		ac := invite(t, a, phone(1), testue.CallOptions{Preconditions: true})
		bc := incoming(t, b, ac)

		go func() { _ = bc.Ring(ctx) }()

		failed(t, ctx, ac, 500)
		ended(t, bc, testue.Cancelled)

		s.pol.noCallEnd(200 * time.Millisecond)
	})
}

// TS 24.229 §5.2.8.1.2, TS 29.214 §4.4.6.1, TS 29.514 §4.2.5.3
func TestCallAbortedByThePolicyFunction(t *testing.T) {
	forEachPolicy(t, func(t *testing.T, iface string) {
		s := newPolicyScene(t, iface, nil)
		a := s.caller(0, false, testue.Config{})
		b := s.caller(1, false, testue.Config{})

		ctx := s.ctx()
		ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})
		session := s.pol.firstCall(ueAddr(0, false))

		if err := s.pol.abort(session); err != nil {
			t.Fatalf("abort: %v", err)
		}

		ended(t, bc, testue.RemoteBye)

		s.pol.wantCallEnd(session, rx.TerminationAdministrative)

		if ac.State() != testue.CallConfirmed {
			t.Errorf("caller's call %s, want it left to the UE that lost its bearer", ac.State())
		}
	})
}

// TS 24.229 §5.2.8.1.2, TS 29.214 §4.4.6.2, TS 29.514 §4.2.5.8
func TestCallMediaLost(t *testing.T) {
	forEachPolicy(t, func(t *testing.T, iface string) {
		s := newPolicyScene(t, iface, func(c *config.Config) { c.PCSCF.MediaLossTimeout = 100 * time.Millisecond })
		a := s.caller(0, false, testue.Config{})
		b := s.caller(1, false, testue.Config{})

		ctx := s.ctx()
		ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})
		session := s.pol.firstCall(ueAddr(1, false))

		s.pol.mediaLost(session, 1)

		ended(t, ac, testue.RemoteBye)

		if bc.State() != testue.CallConfirmed {
			t.Errorf("callee's call %s, want it left to the UE that lost its bearer", bc.State())
		}
	})
}
