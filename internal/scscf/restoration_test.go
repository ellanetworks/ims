package scscf

import (
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/sip/siptest"
)

// newRestorationHarness registers the UE with an HSS of the features.
func newRestorationHarness(t *testing.T, features cx.Features) *sessionHarness {
	t.Helper()

	sh := newSessionHarnessWith(t, func(h *harness) {
		h.hss.set(func(f *fakeHSS) { f.features = features })
	})

	sh.hss.wantSAR(t, cx.AssignmentRegistration)

	return sh
}

// pcscfSource is the Response-Source of the P-CSCF's 404 (Not Found) for no binding (TS 24.229 §7.2.17).
var pcscfSource = sip.Field{Name: "Response-Source", Value: "fe=<urn:3gpp:fe:p-cscf.term>"}

// pcscfNotFound sends a terminating INVITE, which the P-CSCF answers with a 404 (Not Found) for no binding, and
// returns the final response upstream.
func (sh *sessionHarness) pcscfNotFound(t *testing.T) *sip.Response {
	t.Helper()

	return sh.notFound(t, pcscfSource)
}

// notFound sends a terminating INVITE, answered with a 404 (Not Found) with the fields, and returns the final response
// upstream.
func (sh *sessionHarness) notFound(t *testing.T, fields ...sip.Field) *sip.Response {
	t.Helper()

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("INVITE", testTel))

	got, f := sh.term.RecvRequest()
	reply(t, sh.term, got, f, 404, "", fields...)

	return final(t, sh.icscf)
}

// TS 24.229 §5.4.3.3, TS 23.380 §5.4.2.1 steps 3 to 6: the 404 of a P-CSCF that has no binding for the UE has the
// S-CSCF answer 480 upstream, and unregister the public identity at the HSS with the P-CSCF restoration indication,
// keeping its profile for the requests to it until the UE registers again.
func TestPCSCFRestoration(t *testing.T) {
	sh := newRestorationHarness(t, cx.FeaturePCSCFRestoration)

	if f := cx.Features(sh.registeredHSS().Features); f&cx.FeaturePCSCFRestoration == 0 {
		t.Fatalf("HSS features = %v, want the P-CSCF restoration stored", f)
	}

	wantStatus(t, sh.pcscfNotFound(t), 480)

	sar := sh.hss.wantSAR(t, cx.AssignmentUnregisteredUser)
	if !sar.PCSCFRestoration || len(sar.PublicIdentities) != 1 || sar.PublicIdentities[0] != testIMPU ||
		sar.Features&cx.FeaturePCSCFRestoration == 0 {
		t.Fatalf("SAR = %+v, want the restoration of %s", sar, testIMPU)
	}

	eventually(t, "the registration removed", func() bool { return len(sh.registrations(testIMPI)) == 0 })

	sets, err := sh.db.ListUnregisteredSets(t.Context())
	if err != nil || len(sets) != 1 || sets[0].Unregistered.Identity != testIMPU || len(sets[0].UserData) == 0 {
		t.Fatalf("unregistered sets = %+v, %v; want the set kept for %s", sets, err, testIMPU)
	}

	// The identity is served unregistered, with the profile kept.
	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("INVITE", testTel))
	wantStatus(t, final(t, sh.icscf), 480)
	sh.hss.noCx(t)
}

// TS 29.229 Table 7.1.1: the S-CSCF does not ask an HSS without the feature for the restoration. The binding the
// P-CSCF does not have is deregistered all the same.
func TestPCSCFRestorationUnsupported(t *testing.T) {
	sh := newRestorationHarness(t, 0)

	wantStatus(t, sh.pcscfNotFound(t), 480)

	if sar := sh.hss.wantSAR(t, cx.AssignmentAdministrativeDeregistration); sar.PCSCFRestoration {
		t.Fatalf("SAR = %+v, want no restoration indication", sar)
	}

	eventually(t, "the registration removed", func() bool { return len(sh.registrations(testIMPI)) == 0 })

	if sets, _ := sh.db.ListUnregisteredSets(t.Context()); len(sets) != 0 {
		t.Fatalf("unregistered sets = %+v, want none", sets)
	}
}

// TS 29.228 §6.1.2.1: an HSS none of whose serving nodes for the user supports the restoration refuses it, and keeps
// the registration: the S-CSCF deregisters the identity instead.
func TestPCSCFRestorationNoServingNode(t *testing.T) {
	sh := newRestorationHarness(t, cx.FeaturePCSCFRestoration)
	sh.hss.set(func(f *fakeHSS) { f.restorationResult = tgpp.ResultErrorServingNodeFeatureUnsupported })

	wantStatus(t, sh.pcscfNotFound(t), 480)

	if sar := sh.hss.wantSAR(t, cx.AssignmentUnregisteredUser); !sar.PCSCFRestoration {
		t.Fatalf("SAR = %+v, want the restoration indication", sar)
	}

	if sar := sh.hss.wantSAR(t, cx.AssignmentAdministrativeDeregistration); sar.PCSCFRestoration {
		t.Fatalf("SAR = %+v, want no restoration indication", sar)
	}

	eventually(t, "the registration removed", func() bool { return len(sh.registrations(testIMPI)) == 0 })

	if sets, _ := sh.db.ListUnregisteredSets(t.Context()); len(sets) != 0 {
		t.Fatalf("unregistered sets = %+v, want none", sets)
	}
}

// A 404 of the UE is relayed, and starts no restoration: one through the P-CSCF next to the S-CSCF, which gives the
// Response-Source of its own, and one that another entity gives as its Response-Source (TS 24.229 §7.2.17.5).
func TestPCSCFRestorationNotForTheUE(t *testing.T) {
	for name, tc := range map[string]struct {
		onPath bool
		fields []sip.Field
	}{
		"through the P-CSCF next to the S-CSCF": {onPath: true},
		"with the UE as Response-Source":        {fields: []sip.Field{{Name: "Response-Source", Value: "fe=<urn:3gpp:fe:ue>"}}},
	} {
		t.Run(name, func(t *testing.T) {
			sh := newSessionHarnessWith(t, func(h *harness) {
				h.hss.set(func(f *fakeHSS) { f.features = cx.FeaturePCSCFRestoration })
				h.onPath = func(string) bool { return tc.onPath }
				h.restart()
			})

			sh.hss.wantSAR(t, cx.AssignmentRegistration)

			wantStatus(t, sh.notFound(t, tc.fields...), 404)
			sh.hss.noCx(t)

			if regs := sh.registrations(testIMPI); len(regs) != 1 {
				t.Fatalf("registrations = %+v, want the registration kept", regs)
			}
		})
	}
}

// TS 24.229 §5.4.3.3: through a P-CSCF that gives no Response-Source, every 404 (Not Found) is the P-CSCF's.
func TestPCSCFRestorationWithoutResponseSource(t *testing.T) {
	sh := newRestorationHarness(t, cx.FeaturePCSCFRestoration)

	wantStatus(t, sh.notFound(t), 480)

	if sar := sh.hss.wantSAR(t, cx.AssignmentUnregisteredUser); !sar.PCSCFRestoration {
		t.Fatalf("SAR = %+v, want the restoration indication", sar)
	}
}

// TS 23.380 §5.4.1: no restoration while the UE has another registration flow. The flow the P-CSCF has no binding
// for is deregistered, and the request goes on over the other (RFC 5626 §7).
func TestPCSCFRestorationOtherFlow(t *testing.T) {
	sh := newRestorationHarness(t, cx.FeaturePCSCFRestoration)

	sh.ue.path = "<sip:term@" + sh.term.Addr().String() + ";lr;ob>"
	flowA := "sip:a@" + sh.term.Addr().String()
	flowB := "sip:b@" + sh.term.Addr().String()

	// The flows come after the contact address of the instance, and before it (RFC 5626 §7).
	sh.clock.Advance(time.Second)
	sh.ue.register(registerOptions{contact: sh.ue.flowContact(flowA, "1")})
	sh.hss.wantSAR(t, cx.AssignmentReRegistration)
	sh.clock.Advance(time.Second)
	sh.ue.register(registerOptions{contact: sh.ue.flowContact(flowB, "2")})
	sh.hss.wantSAR(t, cx.AssignmentReRegistration)

	first := sh.ue.contact + "#0"
	if got, want := sh.contacts(), []string{first, flowA + "#1", flowB + "#2"}; !slices.Equal(got, want) {
		t.Fatalf("contacts %v, want %v", got, want)
	}

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("INVITE", testTel))

	for _, tc := range []struct {
		flow string
		code int
	}{{flowB, 404}, {flowA, 486}} {
		got, f := sh.term.RecvRequest()
		for got.Method != "INVITE" {
			got, f = sh.term.RecvRequest()
		}

		if got.URI.String() != tc.flow {
			t.Fatalf("INVITE to %s, want %s", got.URI, tc.flow)
		}

		var fields []sip.Field
		if tc.code == 404 {
			fields = append(fields, pcscfSource)
		}

		reply(t, sh.term, got, f, tc.code, "", fields...)
	}

	// The failed flow's 430 may be the best response, as a 480 (RFC 5626 §11.5): not the P-CSCF's 404.
	if res := final(t, sh.icscf); res.StatusCode != 480 && res.StatusCode != 486 {
		t.Fatalf("got %q, want 480 or 486", res.StartLine())
	}

	eventually(t, "the flow removed", func() bool { return slices.Equal(sh.contacts(), []string{first, flowA + "#1"}) })

	sh.hss.noCx(t)
}

// RFC 3680 §5.1: the subscribers to the reg event learn that the contact was deactivated, which has a UE register
// again (TS 24.229 §5.1.1.6).
func TestPCSCFRestorationNotify(t *testing.T) {
	h := newHarness(t)
	h.hss.set(func(f *fakeHSS) { f.features = cx.FeaturePCSCFRestoration })

	u := h.newUE()
	p := h.newPCSCF()

	ueSub, pcscfSub := h.subscribeBoth(u, p, registerOptions{})

	req := siptest.NewRequest("INVITE", testTel, sip.UDP, h.icscf.Addr())
	req.Header.Prepend("Route", "<sip:"+h.scscf.String()+";lr>")
	h.icscf.Send(sip.UDP, h.scscf, req)

	got, f := p.inbox.RecvRequest()
	reply(t, p.inbox, got, f, 404, "", pcscfSource)
	wantStatus(t, final(t, h.icscf), 480)

	h.hss.wantSAR(t, cx.AssignmentUnregisteredUser)

	for _, s := range []*subscriber{pcscfSub, ueSub} {
		req, flow := s.inbox.RecvRequest()
		for req.Method == "ACK" {
			req, flow = s.inbox.RecvRequest()
		}

		n := s.checkNotify(req)
		s.inbox.Send(flow.Transport, flow.Remote, sip.NewResponse(req, 200, ""))

		wantState(t, n, "terminated;reason=deactivated", 1)
		wantContact(t, n.contact(t, testMSISDN, u.contact), reginfo.Terminated, reginfo.Deactivated)
	}
}
