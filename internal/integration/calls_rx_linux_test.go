//go:build linux && (amd64 || arm64)

package integration

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/pcrftest"
	"github.com/ellanetworks/ims/internal/testue"
)

type callSession struct {
	ue     netip.Addr
	aars   []rx.AARequest
	str    *rx.SessionTerminationRequest
	strSeq int
}

// mediaRx reads the PCRF's requests until every call session it saw has
// ended and want sessions were seen, skipping the IMS signalling sessions.
func (s *scene) mediaRx(want int) map[string]*callSession {
	s.t.Helper()

	sessions := map[string]*callSession{}
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

	for seq := 0; !done(); seq++ {
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
				c = &callSession{ue: r.AAR.FramedIPAddress}
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

			c.str, c.strSeq = r.STR, seq
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
				if first.RequestType == nil || *first.RequestType != rx.RequestInitial || len(first.SpecificActions) != 2 {
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
					mc := r.MediaComponents
					if len(mc) != 1 || mc[0].Type == nil || *mc[0].Type != rx.MediaAudio || len(mc[0].SubComponents) == 0 ||
						mc[0].MaxRequestedBandwidthUL == nil || mc[0].MaxRequestedBandwidthDL == nil {
						t.Errorf("%s: Media-Component-Description %+v, want the audio with its bandwidth", id, mc)
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
}
