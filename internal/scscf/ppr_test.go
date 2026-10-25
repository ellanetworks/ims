package scscf

import (
	"bytes"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/sip/siptest"
)

const bobIMPU = "sip:bob@" + homeDomain

// pushed is a PPR of the first implicit set of testSubscriptions, as the change makes it.
func pushed(t *testing.T, change func(sub *cx.IMSSubscription)) cx.PushProfileRequest {
	t.Helper()

	sub := testSubscriptions()[0]
	if change != nil {
		change(&sub)
	}

	data, err := cx.MarshalUserData(sub)
	if err != nil {
		t.Fatal(err)
	}

	return cx.PushProfileRequest{PrivateIdentity: testIMPI, UserData: data}
}

func (h *harness) push(ppr cx.PushProfileRequest) {
	h.t.Helper()

	if err := h.reg.PushProfile(h.t.Context(), ppr); err != nil {
		h.t.Fatalf("PushProfile: %v", err)
	}
}

// TS 24.229 §5.4.1.8 step 1, §5.4.2.1.2 step 4e IV
func TestPushProfileAddsIdentity(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()

	ueSub, pcscfSub := h.subscribeBoth(u, p, registerOptions{})

	h.push(pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[1].PublicIdentities = append(sub.ServiceProfiles[1].PublicIdentities, cx.ProfileIdentity{Identity: bobIMPU})
	}))

	for _, s := range []*subscriber{pcscfSub, ueSub} {
		n := s.recvNotify()
		wantState(t, n, "active;expires=600", 1)
		wantContact(t, n.contact(t, bobIMPU, u.contact), reginfo.Active, reginfo.Created)
		wantContact(t, n.contact(t, testAlias, u.contact), reginfo.Active, reginfo.Created)
	}

	if reg := h.registration(bobIMPU); reg.IMPU != testIMPU || len(reg.Identities) != 5 {
		t.Fatalf("registration = %+v, want bob added to the set", reg)
	}

	h.hss.noCx(t)
}

// TS 24.229 §5.4.1.8 step 2, TS 29.228 §6.5.2.1: an identity leaves the set, or is barred in it.
func TestPushProfileRemovesIdentity(t *testing.T) {
	for name, change := range map[string]func(sub *cx.IMSSubscription){
		"removed": func(sub *cx.IMSSubscription) {
			sub.ServiceProfiles[0].PublicIdentities = sub.ServiceProfiles[0].PublicIdentities[:2]
		},
		"barred": func(sub *cx.IMSSubscription) { sub.ServiceProfiles[0].PublicIdentities[2].Barred = true },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()
			p := h.newPCSCF()

			ueSub, pcscfSub := h.subscribeBoth(u, p, registerOptions{})

			h.push(pushed(t, change))

			for _, s := range []*subscriber{pcscfSub, ueSub} {
				n := s.recvNotify()
				wantState(t, n, "active;expires=600", 1)

				if r := n.registration(t, testTel); r.State != reginfo.Terminated || len(r.Contacts) != 1 {
					t.Fatalf("registration = %+v, want it terminated", r)
				}

				wantContact(t, n.contact(t, testTel, u.contact), reginfo.Terminated, reginfo.Rejected)

				if r := n.registration(t, testMSISDN); r.State != reginfo.Active {
					t.Fatalf("registration = %+v, want it active", r)
				}
			}

			// The identity is reported gone once (§5.4.2.1.2 step 4e I).
			u.register(registerOptions{})
			h.hss.nextSAR(t)

			if n := pcscfSub.recvNotify(); slices.Contains(n.aors(), testTel) {
				t.Fatalf("registrations = %v, want the identity gone", n.aors())
			}
		})
	}
}

// TS 24.229 §5.4.1.8 step 1: an identity unbarred joins the registered ones.
func TestPushProfileUnbarsIdentity(t *testing.T) {
	h := newHarness(t)
	h.hss.set(func(f *fakeHSS) { f.subscriptions[0].ServiceProfiles[1].PublicIdentities[0].Barred = true })

	u := h.newUE()
	p := h.newPCSCF()
	_, pcscfSub := h.subscribeBoth(u, p, registerOptions{})

	h.push(pushed(t, nil))

	n := pcscfSub.recvNotify()
	wantContact(t, n.contact(t, testAlias, u.contact), reginfo.Active, reginfo.Created)
}

// TS 29.228 §6.5.2.1, §6.2.2.1: the HSS uses an RTR to change the default identity or to leave none; the S-CSCF keeps
// what it had.
func TestPushProfileRejected(t *testing.T) {
	for name, change := range map[string]func(sub *cx.IMSSubscription){
		"default changed": func(sub *cx.IMSSubscription) { sub.ServiceProfiles[0].PublicIdentities[0].Barred = false },
		"all barred": func(sub *cx.IMSSubscription) {
			for i := range sub.ServiceProfiles {
				for j := range sub.ServiceProfiles[i].PublicIdentities {
					sub.ServiceProfiles[i].PublicIdentities[j].Barred = true
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()
			p := h.newPCSCF()
			_, pcscfSub := h.subscribeBoth(u, p, registerOptions{})

			before := h.registration(testIMPU)

			err := h.reg.PushProfile(t.Context(), pushed(t, change))
			if err == nil || errors.Is(err, ErrUserUnknown) || errors.Is(err, ErrUnsupportedUserData) {
				t.Fatalf("PushProfile = %v, want a failure to comply", err)
			}

			if after := h.registration(testIMPU); !bytes.Equal(after.UserData, before.UserData) || len(after.Identities) != len(before.Identities) {
				t.Fatalf("registration = %+v, want it unchanged", after)
			}

			pcscfSub.noNotify()
		})
	}
}

// TS 29.228 Table 6.2.2.1.1
func TestPushProfileErrors(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	u.register(registerOptions{})
	h.hss.nextSAR(t)

	unknown := pushed(t, nil)
	unknown.PrivateIdentity = "nobody@" + homeDomain

	h.hss.set(func(f *fakeHSS) { f.subscriptions = nil })

	for _, tt := range []struct {
		name string
		ppr  cx.PushProfileRequest
		want error
	}{
		{"unknown user", unknown, ErrUserUnknown},
		{"unknown user, charging only", cx.PushProfileRequest{
			PrivateIdentity: unknown.PrivateIdentity, Charging: &cx.ChargingInformation{PrimaryEventChargingFunction: "aaa://ecf"},
		}, ErrUserUnknown},
		{"malformed user data", cx.PushProfileRequest{PrivateIdentity: testIMPI, UserData: []byte("<IMSSubscription>")}, ErrUnsupportedUserData},
		{"charging only", cx.PushProfileRequest{
			PrivateIdentity: testIMPI, Charging: &cx.ChargingInformation{PrimaryEventChargingFunction: "aaa://ecf"},
		}, nil},
		{"allowed WAF and WWSF only", cx.PushProfileRequest{
			PrivateIdentity: testIMPI, AllowedWebRTC: &cx.AllowedWebRTCFunctions{WebServerFunctions: []string{"wwsf.example.com"}},
		}, nil},
		{"unchanged", pushed(t, nil), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := h.reg.PushProfile(t.Context(), tt.ppr); !errors.Is(err, tt.want) {
				t.Fatalf("PushProfile = %v, want %v", err, tt.want)
			}
		})
	}

	h.hss.noCx(t)
}

// TS 29.228 §6.2.2.1: the HSS names one of the private identities that share the set; the profile is that of every
// registration of the set.
func TestPushProfileSharedSet(t *testing.T) {
	h := newHarness(t)

	phone := h.newUE()
	phone.register(registerOptions{})
	h.hss.nextSAR(t)

	tablet := h.newUE()
	tablet.impi = "tablet@" + homeDomain
	tablet.impu = testAlias
	tablet.contact = "sip:tablet@127.0.0.1:5090"
	tablet.register(registerOptions{})
	h.hss.nextSAR(t)

	h.push(pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[1].PublicIdentities = append(sub.ServiceProfiles[1].PublicIdentities, cx.ProfileIdentity{Identity: bobIMPU})
	}))

	for _, impi := range []string{testIMPI, tablet.impi} {
		if !slices.ContainsFunc(h.registrations(impi), func(reg db.Registration) bool { return holds(reg.Identities, identityKeyOf(bobIMPU)) }) {
			t.Fatalf("registrations of %s = %+v, want bob in the set", impi, h.registrations(impi))
		}
	}
}

// TS 24.229 §5.4.1.8 step 3: the calls of an identity that leaves the set are released, those of the others go on.
func TestPushProfileReleasesCallsOfRemovedIdentity(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.nextSAR(t)
	sh.answered(t)

	sh.push(pushed(t, func(sub *cx.IMSSubscription) { sub.ServiceProfiles = sub.ServiceProfiles[:1] }))

	sh.term.RecvNone(50 * time.Millisecond)
	sh.icscf.RecvNone(50 * time.Millisecond)

	sh.push(pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles = sub.ServiceProfiles[:1]
		sub.ServiceProfiles[0].PublicIdentities = sub.ServiceProfiles[0].PublicIdentities[:2]
	}))

	bye(t, sh.term)
	bye(t, sh.icscf)
}

// An iFC that does not fire on REGISTER applies to the next requests, without a NOTIFY or a re-registration.
func TestPushProfileFilterCriteria(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.nextSAR(t)

	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	before := sh.registration(testIMPU).Bindings

	sh.push(pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[0].InitialFilterCriteria = []cx.InitialFilterCriteria{
			iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("MESSAGE")),
		}
	}))

	if after := sh.registration(testIMPU).Bindings; len(after) != 1 || !after[0].ExpiresAt.Equal(before[0].ExpiresAt) {
		t.Fatalf("bindings = %+v, want them unchanged", after)
	}

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", testTel))

	if req, _ := as.RecvRequest(); req.Method != "MESSAGE" {
		t.Fatalf("AS got %s, want the MESSAGE", req.StartLine())
	}
}

// TS 24.229 §5.4.1.8 step 4 b: an AS the REGISTER now triggers has the S-CSCF shorten the registration, so that the
// re-registration reaches it.
func TestPushProfileRegisterAS(t *testing.T) {
	h := newHarness(t)
	h.cfg.ReauthExpires = 2 * time.Minute
	h.restart()

	u := h.newUE()
	p := h.newPCSCF()
	_, pcscfSub := h.subscribeBoth(u, p, registerOptions{expires: "3600"})

	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	ppr := pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[0].InitialFilterCriteria = []cx.InitialFilterCriteria{
			iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("REGISTER")),
		}
	})
	h.push(ppr)

	n := pcscfSub.recvNotify()
	c := n.contact(t, testMSISDN, u.contact)
	wantContact(t, c, reginfo.Active, reginfo.Shortened)

	if c.Expires == nil || *c.Expires != 120 {
		t.Fatalf("contact expires %v, want 120", deref(c.Expires))
	}

	as.RecvNone(50 * time.Millisecond)

	// The HSS gives the profile it pushed at the re-registration.
	h.hss.set(func(f *fakeHSS) { f.userData = ppr.UserData })

	res := u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "3600"})
	wantStatus(t, res, 401)
	h.hss.nextMAR(t)

	nonce := sip.Unquote(challengeParams(t, res)["nonce"])
	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES), expires: "3600"}), 200)
	h.hss.wantSAR(t, cx.AssignmentReRegistration)

	if req, _ := as.RecvRequest(); req.Method != "REGISTER" {
		t.Fatalf("AS got %s, want a third-party REGISTER", req.StartLine())
	}

	// The same profile again changes nothing.
	h.push(ppr)
	pcscfSub.recvNotify()
	pcscfSub.noNotify()
}

// TS 29.228 §6.6.1: the HSS pushes the profile of an identity that the S-CSCF keeps unregistered.
func TestPushProfileUnregistered(t *testing.T) {
	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	sh := newSessionHarness(t)
	sh.hss.wantSAR(t, cx.AssignmentRegistration)

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", secondIMPU))

	if res := final(t, sh.icscf); res.StatusCode != 480 {
		t.Fatalf("got %q, want 480", res.StartLine())
	}

	if sar := sh.hss.nextSAR(t); sar.Type != cx.AssignmentUnregisteredUser {
		t.Fatalf("SAR = %+v, want UNREGISTERED_USER", sar)
	}

	sub := testSubscriptions()[1]
	sub.ServiceProfiles[0].InitialFilterCriteria = []cx.InitialFilterCriteria{
		iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("MESSAGE")),
	}

	data, err := cx.MarshalUserData(sub)
	if err != nil {
		t.Fatal(err)
	}

	sh.push(cx.PushProfileRequest{PrivateIdentity: testIMPI, UserData: data})

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", secondIMPU))

	if req, _ := as.RecvRequest(); req.Method != "MESSAGE" {
		t.Fatalf("AS got %s, want the MESSAGE", req.StartLine())
	}

	sh.hss.noCx(t)
}

func TestMayRegister(t *testing.T) {
	method := func(m string, negated bool, group int32) cx.ServicePointTrigger {
		return cx.ServicePointTrigger{Method: &m, Negated: negated, Groups: []int32{group}}
	}

	header := func(group int32) cx.ServicePointTrigger {
		return cx.ServicePointTrigger{SIPHeader: &cx.HeaderMatch{Header: "Contact"}, Groups: []int32{group}}
	}

	sescase := func(c cx.SessionCase, group int32) cx.ServicePointTrigger {
		return cx.ServicePointTrigger{SessionCase: &c, Groups: []int32{group}}
	}

	reregistration := method("REGISTER", false, 0)
	reregistration.RegistrationTypes = []cx.RegistrationType{cx.RegistrationTypeInitial}

	for _, tt := range []struct {
		name string
		tp   *cx.TriggerPoint
		want bool
	}{
		{"none", nil, true},
		{"REGISTER", &cx.TriggerPoint{ConjunctiveNormalForm: true, ServicePointTriggers: []cx.ServicePointTrigger{method("REGISTER", false, 0)}}, true},
		{"INVITE", &cx.TriggerPoint{ConjunctiveNormalForm: true, ServicePointTriggers: []cx.ServicePointTrigger{method("INVITE", false, 0)}}, false},
		{"not INVITE", &cx.TriggerPoint{ConjunctiveNormalForm: true, ServicePointTriggers: []cx.ServicePointTrigger{method("INVITE", true, 0)}}, true},
		{"initial REGISTER only", &cx.TriggerPoint{ConjunctiveNormalForm: true, ServicePointTriggers: []cx.ServicePointTrigger{reregistration}}, false},
		{"REGISTER and a header", &cx.TriggerPoint{ConjunctiveNormalForm: true, ServicePointTriggers: []cx.ServicePointTrigger{
			method("REGISTER", false, 0), header(1),
		}}, true},
		{"INVITE and a header", &cx.TriggerPoint{ConjunctiveNormalForm: true, ServicePointTriggers: []cx.ServicePointTrigger{
			method("INVITE", false, 0), header(1),
		}}, false},
		{"INVITE or a header", &cx.TriggerPoint{ConjunctiveNormalForm: true, ServicePointTriggers: []cx.ServicePointTrigger{
			method("INVITE", false, 0), header(0),
		}}, true},
		{"DNF: INVITE, or REGISTER terminating", &cx.TriggerPoint{ServicePointTriggers: []cx.ServicePointTrigger{
			method("INVITE", false, 0), method("REGISTER", false, 1), sescase(cx.SessionCaseTerminatingRegistered, 1),
		}}, false},
		{"DNF: REGISTER originating", &cx.TriggerPoint{ServicePointTriggers: []cx.ServicePointTrigger{
			method("REGISTER", false, 0), sescase(cx.SessionCaseOriginatingRegistered, 0),
		}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := mayRegister(tt.tp); got != tt.want {
				t.Fatalf("mayRegister = %v, want %v", got, tt.want)
			}
		})
	}
}
