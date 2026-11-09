package icscf

import (
	"net/netip"
	"strconv"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/sip"
)

const (
	peerHost = "scscf-b." + homeDomain
	goneName = "sip:scscf-gone." + homeDomain + ":6060"
)

func peerHarness(t *testing.T) (*harness, *fakeSCSCF) {
	t.Helper()

	h := newHarness(t, harnessOptions{hosts: map[string][]netip.Addr{peerHost: {loopback}}})
	peer := h.newAS(loopback)
	peer.name = "sip:" + peerHost + ":" + strconv.Itoa(int(peer.sock.Addr().Port()))

	return h, peer
}

func TestRegisterToAnotherSCSCF(t *testing.T) {
	h, peer := peerHarness(t)
	u := h.newUE(loopback)

	h.hss.answerUAR(&hssAnswer{
		result: success(tgpp.ResultSubsequentRegistration), name: peer.uri(), features: cx.FeatureIMSRestoration,
	})
	u.send(u.register())
	h.hss.nextUAR(t)

	req, f := peer.recv()
	if req.URI.String() != peer.uri() {
		t.Fatalf("Request-URI = %s, want %s", req.URI, peer.uri())
	}

	peer.respond(req, f, 200)
	u.wantFinal(200)
	h.hss.noCx(t)
	h.scscf.sock.RecvNone(quiet)
}

func wantCapabilitiesUAR(t *testing.T, h *harness) {
	t.Helper()

	if uar := h.hss.nextUAR(t); uar.AuthorizationType != cx.AuthorizationRegistrationAndCapabilities {
		t.Fatalf("second UAR type = %s, want REGISTRATION_AND_CAPABILITIES", uar.AuthorizationType)
	}
}

func TestRegisterReassignedWhenTheSCSCFCannotBeResolved(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	h.hss.answerUAR(
		&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: goneName, features: cx.FeatureIMSRestoration},
		&hssAnswer{result: success(diameter.ResultSuccess), features: cx.FeatureIMSRestoration},
	)
	u.send(u.register())
	h.hss.nextUAR(t)
	wantCapabilitiesUAR(t, h)

	req, f := h.scscf.recv()
	if want := h.scscf.uri() + ";scscf-reselection"; req.URI.String() != want {
		t.Fatalf("Request-URI = %s, want %s", req.URI, want)
	}

	h.scscf.respond(req, f, 401)
	u.wantFinal(401)
}

func TestRegisterReassignedWhenTheSCSCFDoesNotAnswer(t *testing.T) {
	h, peer := peerHarness(t)
	u := h.newUE(loopback)

	h.hss.answerUAR(
		&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: peer.uri(), features: cx.FeatureIMSRestoration},
		&hssAnswer{result: success(diameter.ResultSuccess), features: cx.FeatureIMSRestoration},
	)
	u.send(u.register())
	h.hss.nextUAR(t)

	peer.recv()
	wantCapabilitiesUAR(t, h)

	req, f := h.scscf.recv()
	h.scscf.respond(req, f, 200)
	u.wantFinal(200)
}

func TestRegisterReassignedWithoutHSSSupport(t *testing.T) {
	h, peer := peerHarness(t)
	u := h.newUE(loopback)

	h.hss.answerUAR(
		&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: peer.uri()},
		&hssAnswer{result: success(diameter.ResultSuccess)},
	)
	u.send(u.register())
	h.hss.nextUAR(t)

	peer.recv()
	wantCapabilitiesUAR(t, h)

	req, f := h.scscf.recv()
	h.scscf.respond(req, f, 200)
	u.wantFinal(200)
}

func TestRegisterReassignedWhenTheSCSCFRefuses(t *testing.T) {
	for _, code := range []int{302, 480} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			h, peer := peerHarness(t)
			u := h.newUE(loopback)

			h.hss.answerUAR(
				&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: peer.uri(), features: cx.FeatureIMSRestoration},
				&hssAnswer{result: success(diameter.ResultSuccess), features: cx.FeatureIMSRestoration},
			)
			u.send(u.register())
			h.hss.nextUAR(t)

			req, f := peer.recv()
			peer.respond(req, f, code, func(r *sip.Response) {
				if code == 302 {
					r.Header.Set("Contact", "<sip:elsewhere."+homeDomain+">")
				}
			})
			wantCapabilitiesUAR(t, h)

			req, f = h.scscf.recv()
			h.scscf.respond(req, f, 200)
			u.wantFinal(200)
		})
	}
}

func TestRegisterNotReassignedWhenTheHSSKeepsTheFailedSCSCF(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	h.hss.answerUAR(
		&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: goneName},
		&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: goneName},
	)
	u.send(u.register())
	h.hss.nextUAR(t)
	wantCapabilitiesUAR(t, h)

	u.wantFinal(480)
	h.scscf.sock.RecvNone(quiet)
}

func TestRegisterNotReassignedWhenTheLocalSCSCFDoesNotAnswer(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	h.hss.answerUAR(&hssAnswer{
		result: success(tgpp.ResultSubsequentRegistration), name: h.scscf.uri(), features: cx.FeatureIMSRestoration,
	})
	u.send(u.register())
	h.hss.nextUAR(t)

	h.scscf.recv()
	u.wantFinal(504)
	h.hss.noCx(t)
}

func TestRegisterReassignmentRefusedByTheHSS(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	h.hss.answerUAR(
		&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: goneName, features: cx.FeatureIMSRestoration},
		&hssAnswer{result: success(diameter.ResultAuthorizationRejected)},
	)
	u.send(u.register())
	h.hss.nextUAR(t)
	wantCapabilitiesUAR(t, h)

	u.wantFinal(403)
	h.scscf.sock.RecvNone(quiet)
}

func wantCapabilitiesLIR(t *testing.T, h *harness) {
	t.Helper()

	if lir := h.hss.nextLIR(t); lir.AuthorizationType != cx.AuthorizationRegistrationAndCapabilities {
		t.Fatalf("second LIR type = %s, want REGISTRATION_AND_CAPABILITIES", lir.AuthorizationType)
	}
}

func terminatingInvite(h *harness, u *ue) *sip.Request {
	return u.invite("sip:alice@"+homeDomain, func(r *sip.Request) {
		r.Header.Add("Route", "<"+h.icscfURI()+";lr>")
		r.Header.Add("P-Asserted-Identity", "<"+testIMPU+">")
	})
}

func TestInviteToAnotherSCSCF(t *testing.T) {
	h, peer := peerHarness(t)
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: peer.uri(), features: cx.FeatureIMSRestoration})
	u.send(terminatingInvite(h, u))
	h.hss.nextLIR(t)

	req, f := peer.recv()
	if got := routes(t, req); len(got) == 0 || got[0] != peer.uri()+";lr" {
		t.Fatalf("routes = %v, want the peer S-CSCF first", got)
	}

	peer.respond(req, f, 486)
	u.wantFinal(486)
	h.hss.noCx(t)
}

func TestInviteReassignedWhenTheSCSCFDoesNotAnswer(t *testing.T) {
	h, peer := peerHarness(t)
	u := h.newUE(loopback)

	h.hss.answerLIR(
		&hssAnswer{result: success(diameter.ResultSuccess), name: peer.uri(), features: cx.FeatureIMSRestoration},
		&hssAnswer{result: success(diameter.ResultSuccess), features: cx.FeatureIMSRestoration},
	)
	u.send(terminatingInvite(h, u))
	h.hss.nextLIR(t)

	peer.recv()
	wantCapabilitiesLIR(t, h)

	req, f := h.scscf.recv()
	if got := routes(t, req); len(got) == 0 || got[0] != h.scscf.uri()+";lr;scscf-reselection" {
		t.Fatalf("routes = %v, want this node's S-CSCF first", got)
	}

	h.scscf.respond(req, f, 486)
	u.wantFinal(486)
}

func TestInviteReassignedWhenTheSCSCFCannotBeResolved(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	h.hss.answerLIR(
		&hssAnswer{result: success(diameter.ResultSuccess), name: goneName, features: cx.FeatureIMSRestoration},
		&hssAnswer{result: success(diameter.ResultSuccess), features: cx.FeatureIMSRestoration},
	)
	u.send(terminatingInvite(h, u))
	h.hss.nextLIR(t)
	wantCapabilitiesLIR(t, h)

	req, f := h.scscf.recv()
	h.scscf.respond(req, f, 486)
	u.wantFinal(486)
}

func TestInviteNotReassignedWhenTheHSSKeepsTheFailedSCSCF(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	h.hss.answerLIR(
		&hssAnswer{result: success(diameter.ResultSuccess), name: goneName},
		&hssAnswer{result: success(diameter.ResultSuccess), name: goneName},
	)
	u.send(terminatingInvite(h, u))
	h.hss.nextLIR(t)
	wantCapabilitiesLIR(t, h)

	u.wantFinal(480)
	h.scscf.sock.RecvNone(quiet)
}

func TestInviteNotReassignedForPSIDirectRouting(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{
		result: success(diameter.ResultSuccess), name: goneName, direct: true, features: cx.FeatureIMSRestoration,
	})
	u.send(terminatingInvite(h, u))
	h.hss.nextLIR(t)

	u.wantFinal(480)
	h.hss.noCx(t)
}
