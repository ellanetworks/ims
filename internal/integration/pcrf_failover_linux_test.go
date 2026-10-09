//go:build linux && (amd64 || arm64)

package integration

import (
	"context"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/diametertest"
	"github.com/ellanetworks/ims/internal/pcrftest"
	"github.com/ellanetworks/ims/testue"
)

// addPCRF adds a second PCRF of the realm of the scene's PCRF, after it in priority.
func (s *scene) addPCRF() *pcrftest.PCRF {
	s.t.Helper()

	var ues []netip.Addr

	for i := range hosts {
		for _, p := range ueAddrsAt(i) {
			ues = append(ues, p.Addr())
		}
	}

	p := pcrftest.New(s.t, pcrftest.Config{
		Host: "pcrf2." + s.pcrf.Realm(), Realm: s.pcrf.Realm(), IMSHost: imsHost, IMSRealm: domain, UEs: ues,
	})

	priority := 10
	id := s.addPeer(api.DiameterPeerParams{
		Host: p.Host(), Address: p.Addr().Addr().String(), Port: int(p.Addr().Port()), Applications: []string{"rx"},
		Priority: &priority,
	})
	s.diameterOpen("hss", "pcrf", id)

	return p
}

func aars(p *pcrftest.PCRF) []rx.RequestType {
	var out []rx.RequestType

	for {
		select {
		case r := <-p.Requests():
			if r.AAR != nil && r.AAR.RequestType != nil && len(r.AAR.MediaComponents) > 0 && r.AAR.MediaComponents[0].Number > 0 {
				out = append(out, *r.AAR.RequestType)
			}
		default:
			return out
		}
	}
}

// RFC 6733 §8.18 REFUSE_SERVICE, the default: a call whose PCRF is lost ends, though another PCRF is up.
func TestCallEndsWithItsPCRF(t *testing.T) {
	s := newScene(t)
	other := s.addPCRF()

	a := s.caller(0, false, testue.Config{})
	b := s.caller(1, false, testue.Config{})

	ctx := s.ctx()
	ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})

	s.pcrf.Stop(t)

	_ = ac.Hold(ctx)

	ended(t, ac, testue.RemoteBye)
	ended(t, bc, testue.RemoteBye)

	if got := aars(other); len(got) != 0 {
		t.Fatalf("the other PCRF got %v for the call, want nothing: the session was refused another PCRF", got)
	}
}

// RFC 6733 §8.18 TRY_AGAIN: a call whose PCRF is lost goes on with another PCRF of the realm, which opens a new
// session for it (TS 29.214 §4.4.2).
func TestCallMovesToAnotherPCRF(t *testing.T) {
	s := newScene(t)
	other := s.addPCRF()

	s.pcrf.AnswerWith(func(rx.AARequest) rx.AAAnswer { return rx.AAAnswer{SessionServerFailover: diameter.TryAgain} })

	a := s.caller(0, false, testue.Config{})
	b := s.caller(1, false, testue.Config{})

	ctx := s.ctx()
	ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})

	s.pcrf.Stop(t)

	if err := ac.Hold(ctx); err != nil {
		t.Fatalf("Hold with the PCRF of the call lost: %v", err)
	}

	if ac.State() != testue.CallConfirmed || bc.State() != testue.CallConfirmed {
		t.Fatalf("calls %s and %s, want them kept", ac.State(), bc.State())
	}

	got := aars(other)
	if len(got) < 2 || got[0] != rx.RequestUpdate || got[len(got)-1] != rx.RequestInitial {
		t.Fatalf("the other PCRF got %v, want the update it does not know, then a new session", got)
	}

	if err := ac.Bye(ctx); err != nil {
		t.Fatalf("Bye: %v", err)
	}

	ended(t, bc, testue.RemoteBye)
}

// TS 29.213 §7.3.4: a redirect DRA sends the first AAR of each session to the PCRF of the UE; the session's
// next AARs and its STR go to that PCRF, not through the DRA (§7.3.4.1).
func TestCallThroughARedirectDRA(t *testing.T) {
	s := newScene(t)

	var redirected commandLog

	id := diameter.Identity{OriginHost: "dra." + s.pcrf.Realm(), OriginRealm: s.pcrf.Realm(), HostIPAddresses: []netip.Addr{s.pcrf.Addr().Addr()}, ProductName: "dra"}

	_, addr := diametertest.Listen(t, diametertest.Config{
		Identity: id,
		Peer:     diameter.Peer{ID: "ims", Host: imsHost, Applications: []diameter.Application{{ID: rx.ApplicationID, VendorID: tgpp.VendorID}}},
		Handler: diameter.HandlerFunc(func(_ context.Context, _ *diameter.Conn, req *diameter.Message) *diameter.Message {
			redirected.add(req.CommandCode)

			ans, err := diameter.NewRedirectAnswer(req, id, diameter.Redirect{
				Hosts:        []diameter.URI{{Host: s.pcrf.Host(), Port: s.pcrf.Addr().Port(), Transport: diameter.TransportTCP}},
				Usage:        diameter.AllSession,
				MaxCacheTime: time.Minute,
			})
			if err != nil {
				panic(err)
			}

			return ans
		}),
	})

	low := 65535
	s.put("/api/v1/diameter/peers/pcrf", api.DiameterPeerParams{
		Host: s.pcrf.Host(), Address: s.pcrf.Addr().Addr().String(), Port: int(s.pcrf.Addr().Port()),
		Applications: []string{"rx"}, Priority: &low,
	})

	dra := s.addPeer(api.DiameterPeerParams{Host: id.OriginHost, Address: addr.Addr().String(), Port: int(addr.Port()), Applications: []string{"rx"}})
	s.diameterOpen("hss", "pcrf", dra)

	a := s.caller(0, false, testue.Config{})
	b := s.caller(1, false, testue.Config{})

	ctx := s.ctx()
	ac, bc := connect(t, ctx, a, b, phone(1), testue.CallOptions{})

	if err := ac.Bye(ctx); err != nil {
		t.Fatalf("Bye: %v", err)
	}

	ended(t, bc, testue.RemoteBye)

	if got := redirected.take(); slices.ContainsFunc(got, func(c uint32) bool { return c != rx.CommandAA }) {
		t.Fatalf("the DRA got %v, want initial AARs only", got)
	}

	strs := 0

	eventually(t, "the STRs of the call at the PCRF", func() bool {
		for {
			select {
			case r := <-s.pcrf.Requests():
				if r.STR != nil {
					strs++
				}

				continue
			default:
			}

			return strs >= 2
		}
	})
}
