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

// eventually polls cond until it holds, for a change the S-CSCF makes in the background.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(siptest.Timeout)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

const confPSI = "sip:conf@" + homeDomain

// TS 29.228 §6.6.1, §6.2.2.1: the HSS pushes the profile of a PSI the S-CSCF keeps unregistered.
func TestPushProfilePSI(t *testing.T) {
	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	psi := cx.IMSSubscription{
		PrivateIdentity: "conf@" + homeDomain,
		ServiceProfiles: []cx.ServiceProfile{{PublicIdentities: []cx.ProfileIdentity{
			{Identity: confPSI, Type: cx.IdentityDistinctPSI},
		}}},
	}

	sh := newSessionHarnessWith(t, func(h *harness) {
		h.hss.set(func(f *fakeHSS) { f.subscriptions = append(f.subscriptions, psi) })
	})
	sh.hss.nextSAR(t)

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", confPSI))

	if res := final(t, sh.icscf); res.StatusCode != 480 {
		t.Fatalf("got %q, want 480: no service for the PSI", res.StartLine())
	}

	if sar := sh.hss.nextSAR(t); sar.Type != cx.AssignmentUnregisteredUser {
		t.Fatalf("SAR = %+v, want UNREGISTERED_USER", sar)
	}

	changed := cx.IMSSubscription{
		PrivateIdentity: psi.PrivateIdentity,
		ServiceProfiles: []cx.ServiceProfile{{
			PublicIdentities:      psi.ServiceProfiles[0].PublicIdentities,
			InitialFilterCriteria: []cx.InitialFilterCriteria{iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("MESSAGE"))},
		}},
	}

	data, err := cx.MarshalUserData(changed)
	if err != nil {
		t.Fatal(err)
	}

	sh.push(cx.PushProfileRequest{PrivateIdentity: psi.PrivateIdentity, UserData: data})

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", confPSI))

	if req, _ := as.RecvRequest(); req.Method != "MESSAGE" {
		t.Fatalf("AS got %s, want the MESSAGE", req.StartLine())
	}

	sh.hss.noCx(t)
}

// TS 24.229 §5.4.1.8 step 2, NOTE 2: barring an identity leaves its calls in progress.
func TestPushProfileBarringKeepsCalls(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.nextSAR(t)
	sh.answered(t)

	sh.push(pushed(t, func(sub *cx.IMSSubscription) { sub.ServiceProfiles[0].PublicIdentities[2].Barred = true }))

	sh.term.RecvNone(100 * time.Millisecond)
	sh.icscf.RecvNone(50 * time.Millisecond)

	if id := sh.registration(testMSISDN).Identities[2]; id.Key != identityKeyOf(testTel) || !id.Barred {
		t.Fatalf("identity = %+v, want the tel URI barred", id)
	}
}

// TS 29.228 §6.2.2.1: on an error the S-CSCF keeps what it had: no call is released.
func TestPushProfileFailureKeepsCalls(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.nextSAR(t)
	sh.answered(t)

	before := sh.registration(testMSISDN)

	err := sh.reg.PushProfile(t.Context(), pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[0].PublicIdentities = []cx.ProfileIdentity{{Identity: testTel}}
	}))
	if !errors.Is(err, ErrUnableToComply) {
		t.Fatalf("PushProfile = %v, want ErrUnableToComply: the default identity changes", err)
	}

	sh.term.RecvNone(100 * time.Millisecond)
	sh.icscf.RecvNone(50 * time.Millisecond)

	if after := sh.registration(testMSISDN); !bytes.Equal(after.UserData, before.UserData) {
		t.Fatal("profile changed by a push refused")
	}
}

// TS 24.229 §5.4.2.1.2 step 3: the NOTIFY of a change of a shared set lists the contacts of every user of it.
func TestPushProfileSharedSetNotify(t *testing.T) {
	h := newHarness(t)
	p := h.newPCSCF()

	phone := h.newUE()
	phone.path = p.path()
	phone.register(registerOptions{})
	h.hss.nextSAR(t)

	s := h.pcscfSubscriber(p)
	s.subscribed()

	tablet := h.newUE()
	tablet.impi = "tablet@" + homeDomain
	tablet.impu = testAlias
	tablet.contact = "sip:tablet@127.0.0.1:5090"
	tablet.register(registerOptions{})
	h.hss.nextSAR(t)
	s.recvNotify()

	h.push(pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[1].PublicIdentities = append(sub.ServiceProfiles[1].PublicIdentities, cx.ProfileIdentity{Identity: bobIMPU})
	}))

	n := s.recvNotify()
	for _, c := range []string{phone.contact, tablet.contact} {
		wantContact(t, n.contact(t, bobIMPU, c), reginfo.Active, reginfo.Created)
	}

	h.push(pushed(t, func(sub *cx.IMSSubscription) { sub.ServiceProfiles = sub.ServiceProfiles[:1] }))

	n = s.recvNotify()
	if r := n.registration(t, testAlias); r.State != reginfo.Terminated || len(r.Contacts) != 2 {
		t.Fatalf("registration = %+v, want the alias terminated with both contacts", r)
	}

	for _, c := range []string{phone.contact, tablet.contact} {
		wantContact(t, n.contact(t, testAlias, c), reginfo.Terminated, reginfo.Rejected)
	}
}

// TS 29.228 Table 6.2.2.1.1: a private identity the S-CSCF knows, with a profile of a set it does not hold.
func TestPushProfileUnknownSet(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	u.register(registerOptions{})
	h.hss.nextSAR(t)

	data, err := cx.MarshalUserData(cx.IMSSubscription{
		PrivateIdentity: testIMPI,
		ServiceProfiles: []cx.ServiceProfile{{PublicIdentities: []cx.ProfileIdentity{{Identity: unknownIMPU}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.reg.PushProfile(t.Context(), cx.PushProfileRequest{PrivateIdentity: testIMPI, UserData: data}); !errors.Is(err, ErrUnableToComply) {
		t.Fatalf("PushProfile = %v, want ErrUnableToComply", err)
	}
}

// RFC 3680 §5.1: an identity added by the push that shortens the registration is created, the others shortened.
func TestPushProfileAddsIdentityAndRegisterAS(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()
	_, s := h.subscribeBoth(u, p, registerOptions{})

	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	h.push(pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[1].PublicIdentities = append(sub.ServiceProfiles[1].PublicIdentities, cx.ProfileIdentity{Identity: bobIMPU})
		sub.ServiceProfiles[0].InitialFilterCriteria = []cx.InitialFilterCriteria{
			iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("REGISTER")),
		}
	}))

	n := s.recvNotify()
	wantContact(t, n.contact(t, bobIMPU, u.contact), reginfo.Active, reginfo.Created)
	wantContact(t, n.contact(t, testMSISDN, u.contact), reginfo.Active, reginfo.Shortened)
}

// TS 24.229 §5.4.1.7: an AS already triggered for one service profile, now for another, gets a third-party REGISTER of
// the other's identity: the registration is shortened.
func TestPushProfileRegisterASForAnotherProfile(t *testing.T) {
	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	trigger := iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("REGISTER"))

	h := newHarness(t)
	h.hss.set(func(f *fakeHSS) {
		f.subscriptions[0].ServiceProfiles[0].InitialFilterCriteria = []cx.InitialFilterCriteria{trigger}
	})

	u := h.newUE()
	p := h.newPCSCF()
	_, s := h.subscribeBoth(u, p, registerOptions{})

	h.push(pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[0].InitialFilterCriteria = []cx.InitialFilterCriteria{trigger}
		sub.ServiceProfiles[1].InitialFilterCriteria = []cx.InitialFilterCriteria{trigger}
	}))

	wantContact(t, s.recvNotify().contact(t, testMSISDN, u.contact), reginfo.Active, reginfo.Shortened)
}

// TS 29.228 §6.5.2.1: the subscriptions to an identity removed end; the others go on.
func TestPushProfileEndsSubscriptionsToRemovedIdentity(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()
	u.path = p.path()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	all := h.pcscfSubscriber(p)
	all.subscribed()

	tel := h.ueSubscriber(u)
	tel.impu = testTel
	tel.subscribed()

	h.push(pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[0].PublicIdentities = sub.ServiceProfiles[0].PublicIdentities[:2]
	}))

	if n := tel.recvNotify(); n.state != "terminated;reason=rejected" {
		t.Fatalf("Subscription-State %q, want the subscription to the identity removed terminated", n.state)
	}

	if n := all.recvNotify(); n.state != "active;expires=600" {
		t.Fatalf("Subscription-State %q, want the other subscription active", n.state)
	}

	h.waitSubscriptions(1)
}

// TS 29.228 §6.5.2.1: the identity a UE registered, removed, gives way to the default identity.
func TestPushProfileRemovesRegisteredIdentity(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	u.impu = testAlias
	u.register(registerOptions{})
	h.hss.nextSAR(t)

	h.push(pushed(t, func(sub *cx.IMSSubscription) { sub.ServiceProfiles = sub.ServiceProfiles[:1] }))

	eventually(t, "the default identity in place of the one removed", func() bool {
		reg := h.registration(testMSISDN)
		return reg.IMPU == testMSISDN && reg.Bindings[0].IMPU == identityKeyOf(testMSISDN)
	})
}

// reRegisterWith refreshes the registration of the UE, the HSS downloading the first implicit set of
// testSubscriptions as change makes it (TS 29.228 §6.6).
func (sh *sessionHarness) reRegisterWith(t *testing.T, change func(sub *cx.IMSSubscription)) {
	t.Helper()

	sub := testSubscriptions()[0]
	change(&sub)

	data, err := cx.MarshalUserData(sub)
	if err != nil {
		t.Fatal(err)
	}

	sh.hss.set(func(f *fakeHSS) { f.userData, f.override = data, true })

	// Every re-registration notifies the HSS (TS 23.228 §5.2.2.4 step 6), a refresh without a challenge included.
	wantStatus(t, sh.ue.send(registerOptions{auth: sh.ue.protected(testNonce(), testVector.XRES)}), 200)
	sh.hss.wantSAR(t, cx.AssignmentReRegistration)
}

// TS 24.229 §5.4.1.8, TS 29.228 §6.6: a profile the HSS downloads with a re-registration changes it as a push does.
func TestReRegistrationProfileChange(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.nextSAR(t)

	sh.answered(t)

	sh.reRegisterWith(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[0].PublicIdentities = sub.ServiceProfiles[0].PublicIdentities[:2]
	})

	bye(t, sh.term)
	bye(t, sh.icscf)

	if holds(sh.registration(testMSISDN).Identities, identityKeyOf(testTel)) {
		t.Fatal("tel URI kept in the set")
	}
}

// TS 24.229 §5.4.1.8 step 2, NOTE 2: an identity barred, or another identity removed, with a re-registration leaves
// the calls in progress.
func TestReRegistrationProfileChangeKeepsCalls(t *testing.T) {
	for name, change := range map[string]func(sub *cx.IMSSubscription){
		"barred":          func(sub *cx.IMSSubscription) { sub.ServiceProfiles[0].PublicIdentities[2].Barred = true },
		"another removed": func(sub *cx.IMSSubscription) { sub.ServiceProfiles = sub.ServiceProfiles[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			sh := newSessionHarness(t)
			sh.hss.nextSAR(t)
			sh.answered(t)

			sh.reRegisterWith(t, change)

			sh.term.RecvNone(100 * time.Millisecond)
			sh.icscf.RecvNone(50 * time.Millisecond)
		})
	}
}

// TS 24.229 §5.4.1.8 step 3: an identity removed with a re-registration releases its calls, not those of the other
// identities of the contact.
func TestReRegistrationReleasesCallsOfRemovedIdentity(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.nextSAR(t)

	tel := sh.answeredTo(t, testTel)
	sh.answeredTo(t, testAlias)

	sh.reRegisterWith(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[0].PublicIdentities = sub.ServiceProfiles[0].PublicIdentities[:2]
	})

	if got := bye(t, sh.term); got != tel {
		t.Fatalf("BYE of call %s, want that to the tel URI, %s", got, tel)
	}

	if got := bye(t, sh.icscf); got != tel {
		t.Fatalf("BYE of call %s, want that to the tel URI, %s", got, tel)
	}

	sh.term.RecvNone(100 * time.Millisecond)
	sh.icscf.RecvNone(50 * time.Millisecond)
}

// A refresh while the HSS cannot be reached keeps the registration, with the profile held.
func TestReRegistrationHSSDown(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.nextSAR(t)

	sh.loop.SetDown(true)

	wantStatus(t, sh.ue.send(registerOptions{auth: sh.ue.protected(testNonce(), testVector.XRES)}), 200)

	if reg := sh.registration(testMSISDN); len(reg.Bindings) != 1 || !holds(reg.Identities, identityKeyOf(testTel)) {
		t.Fatalf("registration = %+v, want it kept", reg)
	}
}

// TS 29.228 §6.1.2.1: a profile kept unregistered outlives a restart, and is released at the HSS when it expires.
func TestUnregisteredProfileAfterRestart(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.wantSAR(t, cx.AssignmentRegistration)

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", secondIMPU))
	final(t, sh.icscf)

	if sar := sh.hss.nextSAR(t); sar.Type != cx.AssignmentUnregisteredUser {
		t.Fatalf("SAR = %+v, want UNREGISTERED_USER", sar)
	}

	sh.restart()

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", secondIMPU))
	final(t, sh.icscf)
	sh.hss.noCx(t)

	sh.clock.Advance(sh.cfg.MaxExpires)
	sh.clock.Advance(2 * sweepInterval)

	for {
		sar := sh.hss.nextSAR(t)
		if sar.PrivateIdentity != "" {
			continue
		}

		if sar.Type != cx.AssignmentTimeoutDeregistration || !slices.Equal(sar.PublicIdentities, []string{secondIMPU}) {
			t.Fatalf("SAR = %+v, want TIMEOUT_DEREGISTRATION of the identity", sar)
		}

		break
	}
}

// TS 29.228 §6.5.2.1: a push that adds an identity of another set, whose removal from it the HSS did not push first,
// is refused: the other set keeps it.
func TestPushProfileOfSeveralSets(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	u.register(registerOptions{})
	h.hss.nextSAR(t)

	u.impu = secondIMPU
	u.register(registerOptions{})
	h.hss.nextSAR(t)

	err := h.reg.PushProfile(t.Context(), pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[1].PublicIdentities = append(sub.ServiceProfiles[1].PublicIdentities, cx.ProfileIdentity{Identity: secondIMPU})
	}))
	if !errors.Is(err, ErrUnableToComply) {
		t.Fatalf("PushProfile = %v, want ErrUnableToComply", err)
	}

	if reg := h.registration(secondIMPU); len(reg.Identities) != 1 {
		t.Fatalf("identities = %+v, want the second set unchanged", reg.Identities)
	}

	if reg := h.registration(testMSISDN); len(reg.Identities) != 4 {
		t.Fatalf("identities = %+v, want the first set unchanged", reg.Identities)
	}
}

// TS 29.228 §6.5.2.1: a registration whose profile holds an identity of another registered set, whose removal from it
// the HSS did not push first, fails, and stores nothing: each set keeps its identities.
func TestRegisterIdentityOfAnotherRegisteredSet(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.nextSAR(t)

	// The set of the second identity is kept unregistered.
	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", secondIMPU))
	final(t, sh.icscf)

	if sar := sh.hss.nextSAR(t); sar.Type != cx.AssignmentUnregisteredUser {
		t.Fatalf("SAR = %+v, want UNREGISTERED_USER", sar)
	}

	sh.hss.set(func(f *fakeHSS) {
		f.subscriptions[1] = cx.IMSSubscription{PrivateIdentity: testIMPI, ServiceProfiles: []cx.ServiceProfile{{
			PublicIdentities: []cx.ProfileIdentity{{Identity: secondIMPU}, {Identity: testAlias}},
		}}}
	})

	sh.ue.impu = secondIMPU
	nonce := sh.ue.challenged(registerOptions{})
	wantStatus(t, sh.ue.send(registerOptions{auth: sh.ue.protected(nonce, testVector.XRES)}), 500)
	sh.hss.wantSAR(t, cx.AssignmentRegistration)

	sets, err := sh.db.FindSets(t.Context(), []string{identityKeyOf(secondIMPU)})
	if err != nil || len(sets) != 1 || sets[0].Unregistered == nil || len(sets[0].Identities) != 1 {
		t.Fatalf("sets = %+v, %v; want the set kept unregistered unchanged", sets, err)
	}

	if reg := sh.registration(testAlias); len(reg.Identities) != 4 || len(reg.Bindings) != 1 {
		t.Fatalf("registration = %+v, want it unchanged", reg)
	}
}

// A registration of a set that an expired registration of the private identity, not yet removed, refers to takes it
// over: the set stays, with its new identity.
func TestRegisterSetOfExpiredRegistration(t *testing.T) {
	const third = "sip:third@" + homeDomain

	h := newHarness(t)
	u := h.newUE()
	u.impu = secondIMPU
	u.register(registerOptions{})
	h.hss.nextSAR(t)

	expired := h.registration(secondIMPU)
	for i := range expired.Bindings {
		expired.Bindings[i].ExpiresAt = h.clock.Now().Add(-time.Second)
	}

	if _, err := h.db.SaveRegistration(t.Context(), expired); err != nil {
		t.Fatal(err)
	}

	h.hss.set(func(f *fakeHSS) {
		f.subscriptions[1] = cx.IMSSubscription{PrivateIdentity: testIMPI, ServiceProfiles: []cx.ServiceProfile{{
			PublicIdentities: []cx.ProfileIdentity{{Identity: secondIMPU}, {Identity: third}},
		}}}
	})

	u.impu = third
	u.register(registerOptions{})
	h.hss.nextSAR(t)

	regs := h.registrations(testIMPI)
	if len(regs) != 1 || regs[0].SetID != expired.SetID || len(regs[0].Identities) != 2 {
		t.Fatalf("registrations = %+v, want one of set %d, with both identities", regs, expired.SetID)
	}
}

// TS 29.228 §6.5.2.1: the private identities learn of the changes of a set in no order; each applies the set as it is
// by then. An identity removed, then back, keeps its calls.
func TestPushProfilesOutOfOrder(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.nextSAR(t)
	sh.answered(t)

	if err := sh.reg.lock(t.Context(), testIMPI); err != nil {
		t.Fatal(err)
	}

	sh.push(pushed(t, func(sub *cx.IMSSubscription) {
		sub.ServiceProfiles[0].PublicIdentities = sub.ServiceProfiles[0].PublicIdentities[:2]
	}))
	sh.push(pushed(t, nil))

	sh.reg.unlock(testIMPI)

	sh.term.RecvNone(100 * time.Millisecond)
	sh.icscf.RecvNone(50 * time.Millisecond)

	if reg := sh.registration(testTel); reg.IMPU != testIMPU || len(reg.Identities) != 4 {
		t.Fatalf("registration = %+v, want the tel URI back in the set", reg)
	}
}

// A request to an identity whose registration expired, not yet removed, is served with the profile the HSS sends,
// which is not kept: the set of the registration keeps its identities.
func TestUnregisteredProfileOfExpiredRegistration(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.nextSAR(t)

	expired := sh.registration(testTel)
	for i := range expired.Bindings {
		expired.Bindings[i].ExpiresAt = sh.clock.Now().Add(-time.Second)
	}

	if _, err := sh.db.SaveRegistration(t.Context(), expired); err != nil {
		t.Fatal(err)
	}

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", testTel))
	final(t, sh.icscf)

	if sar := sh.hss.nextSAR(t); sar.Type != cx.AssignmentUnregisteredUser {
		t.Fatalf("SAR = %+v, want UNREGISTERED_USER", sar)
	}

	if reg := sh.registration(testTel); reg.SetID != expired.SetID || len(reg.Identities) != 4 {
		t.Fatalf("registration = %+v, want its set unchanged", reg)
	}

	if sets, err := sh.db.ListUnregisteredSets(t.Context()); err != nil || len(sets) != 0 {
		t.Fatalf("sets kept unregistered = %+v, %v; want none", sets, err)
	}
}
