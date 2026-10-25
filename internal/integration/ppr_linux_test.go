//go:build linux && (amd64 || arm64)

package integration

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/internal/server"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/testue"
)

const bob = "sip:bob@" + domain

func (s *scene) associatedURIs() []string {
	s.t.Helper()

	d, err := db.Open(s.t.Context(), s.db)
	if err != nil {
		s.t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	regs, err := d.ListPCSCFRegistrationsByIMPI(s.t.Context(), impi)
	if err != nil || len(regs) != 1 {
		s.t.Fatalf("P-CSCF registrations = %+v, %v", regs, err)
	}

	return regs[0].AssociatedURIs
}

func (s *scene) pushProfile() {
	s.t.Helper()

	ppa, err := s.hss.PPR(s.ctx(), impi)
	if err != nil || !ppa.Result.Success() {
		s.t.Fatalf("PPA = %+v, %v", ppa, err)
	}
}

// TS 29.228 §6.5.2.1, TS 24.229 §5.4.1.8: the HSS adds an identity to the registered set, then removes it; the UE and
// the P-CSCF learn of each from the reg event.
func TestPushProfileIdentities(t *testing.T) {
	s := newScene(t)
	u := s.newUE(false, testue.Config{T1: fastT1})

	s.subscribed(u)

	s.hss.Update(impi, func(sub *hsstest.Subscriber) {
		sub.IMPUs = append(sub.IMPUs, cx.ProfileIdentity{Identity: bob})
	})
	s.pushProfile()

	n := nextNotify(t, u)
	if rs, c := n.contact(t, bob, ueAddrs[0].Addr()); rs != reginfo.Active || c.Event != reginfo.Created {
		t.Fatalf("registration %s, contact %s; want bob active, created", rs, c.Event)
	}

	eventually(t, "the P-CSCF to associate bob", func() bool { return slices.Contains(s.associatedURIs(), bob) })

	s.hss.Update(impi, func(sub *hsstest.Subscriber) { sub.IMPUs = sub.IMPUs[:len(sub.IMPUs)-1] })
	s.pushProfile()

	n = nextNotify(t, u)
	if rs, c := n.contact(t, bob, ueAddrs[0].Addr()); rs != reginfo.Terminated || c.Event != reginfo.Rejected {
		t.Fatalf("registration %s, contact %s; want bob terminated, rejected", rs, c.Event)
	}

	n.want(t, "active", reginfo.Active, reginfo.Active, reginfo.Created)

	eventually(t, "the P-CSCF to forget bob", func() bool { return !slices.Contains(s.associatedURIs(), bob) })

	if !u.State().Registered {
		t.Fatal("UE deregistered by the profile push")
	}
}

// TS 24.229 §5.4.1.8 step 4 b: a new AS of the REGISTER gets its third-party REGISTER from the re-registration the
// shortened registration brings.
func TestPushProfileRegisterAS(t *testing.T) {
	s := newSceneWith(t, func(srv *server.Server) {
		srv.ASTimeout = asTimeout
		srv.ReauthExpires = 2 * time.Second
		srv.Config.SIP.TrustedNetworks = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	})
	srv := newAS(t, 5091)
	u := s.newUE(false, testue.Config{T1: fastT1})

	s.subscribed(u)

	s.setIFCs(0, ifc(0, srv.URI(), cx.DefaultHandlingSessionContinued, methodIs("REGISTER")))
	s.pushProfile()

	nextNotify(t, u).want(t, "active", reginfo.Active, reginfo.Active, reginfo.Shortened)

	r := srv.next(t)
	if to, _ := r.Header.To(); r.Method != "REGISTER" || to.URI.String() != msisdn {
		t.Fatalf("AS got:\n%s\nwant the third-party REGISTER of the re-registration", r)
	}

	if !u.State().Registered {
		t.Fatal("UE not registered after the re-registration")
	}
}
