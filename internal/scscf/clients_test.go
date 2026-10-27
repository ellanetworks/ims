package scscf

import (
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/sip"
)

// twoClients has the phone's IMS client register its set and an MCPTT client of the same device register another,
// with the same private identity, instance ID and reg-id, from contacts of their own (TS 24.229 §3.1).
func twoClients(t *testing.T) (*harness, *ue, *ue, registerOptions, registerOptions) {
	t.Helper()

	h, one := flowsHarness(t)
	two := h.newUE()
	two.impu, two.path = secondIMPU, "<sip:token2@pcscf."+homeDomain+";lr;ob>"

	return h, one, two, registerOptions{contact: one.flowContact(one.contact, "1")}, registerOptions{contact: two.flowContact(two.contact, "1")}
}

func liveBindings(h *harness, impu string) int {
	n := 0

	for _, b := range h.registration(impu).Bindings {
		if b.ExpiresAt.After(h.clock.Now()) {
			n++
		}
	}

	return n
}

// TS 24.229 §5.4.1.2.2 step 4: a response to the challenge of another public user identity of the private identity
// does not authenticate the REGISTER.
func TestResponseToTheOtherClientsChallengeFails(t *testing.T) {
	h, one, two, o1, o2 := twoClients(t)

	nonce := one.challenged(o1)

	o2.auth = two.protected(nonce, testVector.XRES)
	wantStatus(t, two.send(o2), 403)
	h.hss.wantSAR(t, cx.AssignmentAuthenticationFailure)

	if regs := h.registrations(testIMPI); len(regs) != 0 {
		t.Fatalf("registrations %+v after a failed authentication, want none", regs)
	}
}

func TestDeregisteringOneClientKeepsTheOther(t *testing.T) {
	h, one, two, o1, o2 := twoClients(t)

	one.register(o1)
	two.register(o2)

	o1.auth, o1.expires = one.protected(testNonce(), testVector.XRES), "0"
	wantStatus(t, one.send(o1), 200)

	if n := liveBindings(h, secondIMPU); n != 1 {
		t.Fatalf("%d live bindings of the MCPTT client's set, want its own", n)
	}

	for _, reg := range h.registrations(testIMPI) {
		if holds(reg.Identities, identityKeyOf(testIMPU)) && len(liveAt(reg.Bindings, h.clock.Now())) != 0 {
			t.Fatalf("phone's set still bound: %+v", reg.Bindings)
		}
	}
}

// A re-authentication the network asks for applies to each client, until it authenticates again.
func TestReauthenticationOfEachClient(t *testing.T) {
	h, one, two, o1, o2 := twoClients(t)

	one.register(o1)
	two.register(o2)

	if err := h.reg.Reauthenticate(t.Context(), testIMPI); err != nil {
		t.Fatal(err)
	}

	one.register(o1)

	o2.auth = two.protected(testNonce(), testVector.XRES)
	wantStatus(t, two.send(o2), 401)
	h.hss.nextMAR(t)
	wantStatus(t, two.send(o2), 200)

	o1.auth = one.protected(testNonce(), testVector.XRES)
	wantStatus(t, one.send(o1), 200)
}

// TS 24.229 §5.4.1.2.1 step 4: resynchronising one client's challenge leaves the other's.
func TestResyncOfOneClientKeepsTheOthersChallenge(t *testing.T) {
	h, one, two, o1, o2 := twoClients(t)

	n1, n2 := one.challenged(o1), two.challenged(o2)

	o1.auth = one.resync(n1, make([]byte, autsLen), false)
	wantStatus(t, one.send(o1), 401)
	h.hss.nextMAR(t)

	o2.auth = two.protected(n2, testVector.XRES)
	wantStatus(t, two.send(o2), 200)
}

// TS 24.229 §5.4.5.1.2A: the expiry of one client's binding releases its calls, though the other client of the
// device, of the same instance ID and reg-id, stays registered.
func TestExpiryOfOneClientReleasesItsCalls(t *testing.T) {
	sh := newSessionHarness(t)
	contact := sh.ue.flowContact(sh.ue.contact, "1")

	sh.ue.path = "<sip:flow1@" + sh.term.Addr().String() + ";lr;ob>"
	sh.ue.register(registerOptions{contact: contact, expires: "600"})
	sh.ue.register(registerOptions{contact: "<" + sh.ue.contact + ">", expires: "0"})

	two := sh.newUE()
	two.impu, two.path = secondIMPU, "<sip:flow2@"+sh.term.Addr().String()+";lr;ob>"
	two.register(registerOptions{contact: two.flowContact(two.contact, "1"), expires: "3600"})

	sh.answered(t)

	sh.clock.Advance(15 * time.Minute)

	bye(t, sh.term)
	bye(t, sh.icscf)
}

// TS 24.229 §5.4.3.2 step 1: a client does not originate as an identity of the other client's set.
func TestOriginatingAsTheOtherClientsIdentity(t *testing.T) {
	sh := newSessionHarness(t)

	two := sh.newUE()
	two.impu, two.path = secondIMPU, "<sip:flow2@"+sh.term.Addr().String()+";lr;ob>"
	sh.ue.path = "<sip:flow1@" + sh.term.Addr().String() + ";lr;ob>"

	sh.ue.register(registerOptions{contact: sh.ue.flowContact(sh.ue.contact, "1")})
	sh.ue.register(registerOptions{contact: "<" + sh.ue.contact + ">", expires: "0"})

	res := two.register(registerOptions{contact: two.flowContact(two.contact, "1")})

	sr, err := sip.ParseAddress(res.Header.Get("Service-Route"))
	if err != nil {
		t.Fatalf("Service-Route: %v", err)
	}

	r := sh.originating("INVITE", remoteTel, "<"+testMSISDN+">")
	r.Header.Set("Route", "<sip:"+sr.URI.User+"@"+sh.scscf.String()+";lr>")
	sh.orig.Send(sip.UDP, sh.scscf, r)

	if res := final(t, sh.orig); res.StatusCode != 403 {
		t.Fatalf("got %q, want 403", res.StartLine())
	}
}
