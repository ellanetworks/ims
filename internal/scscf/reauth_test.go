package scscf

import (
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
)

func TestReauthenticate(t *testing.T) {
	h := newHarness(t)
	h.cfg.ReauthExpires = 2 * time.Minute
	h.restart()

	u := h.newUE()
	p := h.newPCSCF()

	ueSub, pcscfSub := h.subscribeBoth(u, p, registerOptions{expires: "3600"})

	if err := h.reg.Reauthenticate(t.Context(), testIMPI); err != nil {
		t.Fatal(err)
	}

	for _, s := range []*subscriber{pcscfSub, ueSub} {
		n := s.recvNotify()
		wantState(t, n, "active;expires=600", 1)

		for _, aor := range []string{testMSISDN, testTel, testAlias} {
			if r := n.registration(t, aor); r.State != regevent.Active {
				t.Fatalf("registration = %+v", r)
			}

			c := n.contact(t, aor, u.contact)
			wantContact(t, c, regevent.Active, regevent.Shortened)

			if c.Expires == nil || *c.Expires != 120 {
				t.Fatalf("contact expires %v, want 120", deref(c.Expires))
			}
		}
	}

	if b := h.registration(testIMPU).Bindings; len(b) != 1 || !b[0].ExpiresAt.Equal(testEpoch.Add(2*time.Minute)) {
		t.Fatalf("bindings = %+v, want them expiring in 2 minutes", b)
	}

	res := u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "3600"})
	wantStatus(t, res, 401)
	h.hss.nextMAR(t)

	nonce := sip.Unquote(challengeParams(t, res)["nonce"])

	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES), expires: "3600"}), 200)
	h.hss.wantSAR(t, cx.AssignmentReRegistration)

	for _, s := range []*subscriber{pcscfSub, ueSub} {
		n := s.recvNotify()
		wantContact(t, n.contact(t, testMSISDN, u.contact), regevent.Active, regevent.Refreshed)

		if c := n.contact(t, testMSISDN, u.contact); c.Expires == nil || *c.Expires != 3600 {
			t.Fatalf("contact expires %v after the re-authentication, want 3600", deref(c.Expires))
		}
	}

	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES), expires: "3600"}), 200)
	h.hss.noCx(t)
}

func TestReauthenticateUnregistered(t *testing.T) {
	h := newHarness(t)

	if err := h.reg.Reauthenticate(t.Context(), testIMPI); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("Reauthenticate = %v, want ErrNotRegistered", err)
	}
}

func TestReauthenticateDefaultExpires(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{expires: "3600"})
	h.hss.nextSAR(t)

	if err := h.reg.Reauthenticate(t.Context(), testIMPI); err != nil {
		t.Fatal(err)
	}

	if b := h.registration(testIMPU).Bindings; len(b) != 1 || !b[0].ExpiresAt.Equal(testEpoch.Add(DefaultReauthExpires)) {
		t.Fatalf("bindings = %+v, want them expiring in %s", b, DefaultReauthExpires)
	}

	h.clock.Advance(DefaultReauthExpires)
	h.hss.wantSAR(t, cx.AssignmentTimeoutDeregistration)
	h.wantUnregistered()
}
