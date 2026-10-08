package scscf

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

var wantIdentities = []db.PublicIdentity{
	{URI: testIMPU, Key: testIMPU, Barred: true},
	{URI: testMSISDN, Key: testMSISDN, DisplayName: "Alice"},
	{URI: testTel, Key: testTel},
	{URI: testAlias, Key: testAlias},
}

const wantAssociated = `"Alice" <` + testMSISDN + `>, <` + testTel + `>, <` + testAlias + `>`

func (h *harness) registrations(impi string) []db.Registration {
	h.t.Helper()

	regs, err := h.db.ListRegistrationsByIMPI(context.Background(), impi)
	if err != nil {
		h.t.Fatalf("ListRegistrationsByIMPI: %v", err)
	}

	return regs
}

func (h *harness) registration(impu string) db.Registration {
	h.t.Helper()

	for _, reg := range h.registrations(testIMPI) {
		if holds(reg.Identities, identityKeyOf(impu)) {
			return reg
		}
	}

	h.t.Fatalf("no registration holds %s", impu)

	return db.Registration{}
}

func (h *harness) wantUnregistered() {
	h.t.Helper()

	if regs := h.registrations(testIMPI); len(regs) != 0 {
		h.t.Fatalf("registrations = %+v, want none", regs)
	}
}

func contacts(t *testing.T, res *sip.Response) map[string]string {
	t.Helper()

	addrs, err := res.Header.Contacts()
	if err != nil {
		t.Fatalf("Contact: %v", err)
	}

	out := make(map[string]string, len(addrs))

	for _, a := range addrs {
		out[a.URI.String()], _ = a.Params.Get("expires")
	}

	return out
}

func TestInitialRegistration(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	res := u.send(registerOptions{auth: u.unprotected(), expires: "600"})
	wantStatus(t, res, 401)

	mar := h.hss.nextMAR(t)
	if mar.PrivateIdentity != testIMPI || mar.PublicIdentity != testIMPU || mar.NumberOfItems != 1 ||
		mar.Scheme != cx.SchemeDigestAKAv1MD5 || mar.ServerName != "sip:scscf."+homeDomain+":5060" || mar.Resync != nil {
		t.Fatalf("MAR = %+v", mar)
	}

	want := map[string]string{
		"realm":     `"` + homeDomain + `"`,
		"nonce":     `"` + testNonce() + `"`,
		"algorithm": "AKAv1-MD5",
		"qop":       `"auth"`,
		"ck":        `"` + hex.EncodeToString(testVector.CK) + `"`,
		"ik":        `"` + hex.EncodeToString(testVector.IK) + `"`,
	}
	if got := challengeParams(t, res); !reflect.DeepEqual(got, want) {
		t.Fatalf("WWW-Authenticate = %v, want %v", got, want)
	}

	res = u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "600"})
	wantStatus(t, res, 200)

	if sar := h.hss.wantSAR(t, cx.AssignmentRegistration); sar.UserDataAlreadyAvailable ||
		!reflect.DeepEqual(sar.PublicIdentities, []string{testIMPU}) {
		t.Fatalf("SAR = %+v", sar)
	}

	reg := h.registration(testIMPU)
	if len(reg.Bindings) != 1 {
		t.Fatalf("bindings = %+v, want one", reg.Bindings)
	}

	contact := reg.Bindings[0].Contact

	wantHeaders := map[string]string{
		"Path":             testPath,
		"Service-Route":    "<sip:orig-" + strconv.FormatInt(contact.ID, 10) + "@scscf." + homeDomain + ":5060;lr>",
		"P-Associated-URI": wantAssociated,
		"Contact":          "<" + u.contact + ">;+sip.instance=" + testInstance + ";+g.3gpp.smsip;expires=600",
	}
	for name, value := range wantHeaders {
		if got := res.Header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}

	if !bytes.Contains([]byte(res.Header.Get("To")), []byte("tag=")) {
		t.Errorf("To = %q, want a tag", res.Header.Get("To"))
	}

	sub, _ := cx.MarshalUserData(testSubscriptions()[0])

	wantReg := db.Registration{
		ID:         reg.ID,
		IMPI:       testIMPI,
		IMPU:       testIMPU,
		Identities: wantIdentities,
		UserData:   sub,
		Bindings: []db.Binding{{
			Contact: db.Contact{
				ID:       contact.ID,
				IMPI:     testIMPI,
				URI:      u.contact,
				Instance: "urn:gsma:imei:35622410-483840-0",
				Params:   ";+sip.instance=" + testInstance + ";+g.3gpp.smsip",
				Path:     testPath,
			},
			CallID:       u.callID,
			CSeq:         2,
			ExpiresAt:    testEpoch.Add(600 * time.Second),
			Event:        db.BindingRegistered,
			IMPU:         testIMPU,
			RegisteredAt: testEpoch,
		}},
	}
	if !reflect.DeepEqual(reg, wantReg) {
		t.Fatalf("registration = %+v, want %+v", reg, wantReg)
	}

	h.hss.noCx(t)
}

func TestWrongResponse(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	nonce := u.challenged(registerOptions{})

	res := u.send(registerOptions{auth: u.protected(nonce, []byte("wrong-xres"))})
	wantStatus(t, res, 403)

	if sar := h.hss.wantSAR(t, cx.AssignmentAuthenticationFailure); !reflect.DeepEqual(sar.PublicIdentities, []string{testIMPU}) {
		t.Fatalf("SAR = %+v", sar)
	}

	h.wantUnregistered()

	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES)}), 500)
	h.hss.noCx(t)
}

func TestChallengeChecks(t *testing.T) {
	tests := []struct {
		name   string
		change func(u *ue, o *registerOptions, nonce string)
	}{
		{"other Call-ID", func(u *ue, o *registerOptions, nonce string) {
			u.callID = "other@127.0.0.1"
			o.auth = u.protected(nonce, testVector.XRES)
		}},
		{"other algorithm", func(u *ue, o *registerOptions, nonce string) {
			o.auth = strings.Replace(u.protected(nonce, testVector.XRES), "algorithm=AKAv1-MD5", "algorithm=MD5", 1)
		}},
		{"other IMPU", func(u *ue, o *registerOptions, nonce string) {
			u.impu = testMSISDN
			o.auth = u.protected(nonce, testVector.XRES)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()

			nonce := u.challenged(registerOptions{})

			var o registerOptions
			tt.change(u, &o, nonce)

			wantStatus(t, u.send(o), 403)
			h.hss.wantSAR(t, cx.AssignmentAuthenticationFailure)
		})
	}
}

func TestUnprotectedResponseIsIgnored(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	nonce := u.challenged(registerOptions{})

	auth := u.protected(nonce, testVector.XRES)
	auth = auth[:len(auth)-len(`"yes"`)] + `"no"`

	wantStatus(t, u.send(registerOptions{auth: auth}), 401)
	h.hss.nextMAR(t)
	h.hss.noCx(t)
}

func TestResync(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run("protected "+strconv.FormatBool(protected), func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()

			nonce := u.challenged(registerOptions{})

			auts := bytes.Repeat([]byte{0x0a}, autsLen)
			wantStatus(t, u.send(registerOptions{auth: u.resync(nonce, auts, protected)}), 401)

			mar := h.hss.nextMAR(t)
			if mar.Resync == nil || !bytes.Equal(mar.Resync.RAND, testVector.RAND) || !bytes.Equal(mar.Resync.AUTS, auts) {
				t.Fatalf("MAR resync = %+v, want RAND‖AUTS", mar.Resync)
			}

			wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
			h.hss.wantSAR(t, cx.AssignmentRegistration)
		})
	}
}

func TestResyncLimit(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	nonce := u.challenged(registerOptions{})
	auts := bytes.Repeat([]byte{0x0a}, autsLen)

	for range maxResyncs {
		wantStatus(t, u.send(registerOptions{auth: u.resync(nonce, auts, false)}), 401)
		h.hss.nextMAR(t)
	}

	wantStatus(t, u.send(registerOptions{auth: u.resync(nonce, auts, false)}), 403)
	h.hss.wantSAR(t, cx.AssignmentAuthenticationFailure)
	h.hss.noCx(t)
}

func TestNetworkAuthenticationFailure(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	nonce := u.challenged(registerOptions{})

	auth := fmt.Sprintf(`Digest username="%s", realm="%s", uri="sip:%s", nonce="%s", response="", `+
		`algorithm=AKAv1-MD5, integrity-protected="no"`, u.impi, homeDomain, homeDomain, nonce)

	res := u.send(registerOptions{auth: auth})
	wantStatus(t, res, 403)

	if res.Header.Has("WWW-Authenticate") {
		t.Errorf("WWW-Authenticate = %q, want none", res.Header.Get("WWW-Authenticate"))
	}

	h.hss.wantSAR(t, cx.AssignmentAuthenticationFailure)
	h.hss.noCx(t)
}

func TestRegAwaitAuthExpiry(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	nonce := u.challenged(registerOptions{})

	h.clock.Advance(regAwaitAuth)

	if sar := h.hss.wantSAR(t, cx.AssignmentAuthenticationTimeout); !reflect.DeepEqual(sar.PublicIdentities, []string{testIMPU}) {
		t.Fatalf("SAR = %+v", sar)
	}

	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES)}), 500)
	h.wantUnregistered()
	h.hss.noCx(t)
}

func TestRegAwaitAuthExpiryKeepsRegistration(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	u.challenged(registerOptions{})
	h.clock.Advance(regAwaitAuth)
	h.hss.wantSAR(t, cx.AssignmentAuthenticationTimeout)

	h.registration(testIMPU)
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
}

func TestResponseWithoutIntegrityProtectedIsIgnored(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	nonce := u.challenged(registerOptions{})

	auth := u.protected(nonce, testVector.XRES)
	auth = auth[:strings.LastIndex(auth, ", integrity-protected=")]

	wantStatus(t, u.send(registerOptions{auth: auth}), 401)
	h.hss.nextMAR(t)
	h.wantUnregistered()
}

func TestStrangersChallengeDoesNotBlockRefresh(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	h.hss.set(func(h *fakeHSS) { h.vector.RAND = bytes.Repeat([]byte{0x11}, 16) })

	stranger := h.newUE()
	stranger.challenged(registerOptions{})

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
	h.hss.noCx(t)
}

func TestProtectedRegisterWithoutRegistration(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 500)
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "0"}), 500)
	h.hss.noCx(t)
}

func TestReRegistrationWithoutChallenge(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{expires: "600"})
	h.hss.nextSAR(t)

	before := h.registration(testIMPU)

	h.clock.Advance(5 * time.Minute)

	res := u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "1200"})
	wantStatus(t, res, 200)
	h.hss.noCx(t)

	if got := res.Header.Get("Service-Route"); got != serviceRoute(h.reg.cfg.Name, before.Bindings[0].Contact.ID) {
		t.Errorf("Service-Route = %q", got)
	}

	if got := res.Header.Get("P-Associated-URI"); got != wantAssociated {
		t.Errorf("P-Associated-URI = %q, want %q", got, wantAssociated)
	}

	after := h.registration(testIMPU)
	if b := after.Bindings[0]; after.ID != before.ID || b.Contact.ID != before.Bindings[0].Contact.ID || b.CSeq != 3 ||
		!b.ExpiresAt.Equal(h.clock.Now().Add(1200*time.Second)) {
		t.Fatalf("registration = %+v", after)
	}
}

func TestReRegistrationWithChallenge(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	before := h.registration(testIMPU)

	u.register(registerOptions{})

	if sar := h.hss.wantSAR(t, cx.AssignmentReRegistration); !sar.UserDataAlreadyAvailable {
		t.Fatalf("SAR = %+v, want User-Data-Already-Available", sar)
	}

	after := h.registration(testIMPU)
	if after.ID != before.ID || !reflect.DeepEqual(after.Identities, wantIdentities) ||
		!bytes.Equal(after.UserData, before.UserData) {
		t.Fatalf("registration = %+v, want %+v kept", after, before)
	}
}

func TestOutOfOrderRegister(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	before := h.registration(testIMPU)

	u.cseq = 0
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 500)

	if after := h.registration(testIMPU); !reflect.DeepEqual(after, before) {
		t.Fatalf("registration = %+v, want %+v", after, before)
	}
}

func TestIMPUAsReceived(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	u.impu = "sip:+15551230001@IMS.mnc001.mcc001.3gppnetwork.org;user=phone"

	wantStatus(t, u.send(registerOptions{auth: u.unprotected()}), 401)

	if mar := h.hss.nextMAR(t); mar.PublicIdentity != u.impu {
		t.Fatalf("MAR Public-Identity = %q, want %q", mar.PublicIdentity, u.impu)
	}

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)

	if sar := h.hss.wantSAR(t, cx.AssignmentRegistration); !reflect.DeepEqual(sar.PublicIdentities, []string{u.impu}) {
		t.Fatalf("SAR = %+v, want %s", sar, u.impu)
	}

	if reg := h.registration(testIMPU); reg.IMPU != u.impu || !reflect.DeepEqual(reg.Identities, wantIdentities) {
		t.Fatalf("registration = %+v", reg)
	}

	u.impu = "sip:+15551230001@" + homeDomain
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 403)
	h.hss.nextSAR(t)
}

func TestAllBarredSet(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	h.hss.set(func(h *fakeHSS) {
		h.subscriptions = []cx.IMSSubscription{{
			PrivateIdentity: testIMPI,
			ServiceProfiles: []cx.ServiceProfile{{PublicIdentities: []cx.ProfileIdentity{{Identity: testIMPU, Barred: true}}}},
		}}
	})

	nonce := u.challenged(registerOptions{})

	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES)}), 403)
	h.hss.wantSAR(t, cx.AssignmentRegistration)
	h.hss.wantSAR(t, cx.AssignmentAdministrativeDeregistration)
	h.wantUnregistered()
}

func TestRejectAfterRegistrationSAR(t *testing.T) {
	outside, err := cx.MarshalUserData(cx.IMSSubscription{
		PrivateIdentity: testIMPI,
		ServiceProfiles: []cx.ServiceProfile{{PublicIdentities: []cx.ProfileIdentity{{Identity: secondIMPU}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		set  func(h *fakeHSS)
		code int
		undo cx.AssignmentType
	}{
		{"invalid User-Data", func(h *fakeHSS) { h.userData = []byte("<IMSSubscription") }, 480, cx.AssignmentDeregistrationTooMuchData},
		{"no User-Data", func(h *fakeHSS) { h.noUserData = true }, 500, cx.AssignmentAdministrativeDeregistration},
		{"IMPU outside the profile", func(h *fakeHSS) { h.userData = outside }, 403, cx.AssignmentAdministrativeDeregistration},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()

			nonce := u.challenged(registerOptions{})

			h.hss.set(tt.set)

			wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES)}), tt.code)
			h.hss.wantSAR(t, cx.AssignmentRegistration)

			if sar := h.hss.wantSAR(t, tt.undo); !reflect.DeepEqual(sar.PublicIdentities, []string{u.impu}) {
				t.Fatalf("SAR = %+v, want %s for %s", sar, tt.undo, u.impu)
			}

			h.wantUnregistered()
		})
	}
}

func TestRejectAfterSARKeepsARegisteredSet(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	registered, err := cx.MarshalUserData(testSubscriptions()[0])
	if err != nil {
		t.Fatal(err)
	}

	h.hss.set(func(h *fakeHSS) { h.userData = registered })

	u.impu = secondIMPU
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 403)
	h.hss.wantSAR(t, cx.AssignmentRegistration)
	h.hss.noCx(t)

	h.registration(testIMPU)
}

func TestRegisterAnotherIMPU(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	u.impu = secondIMPU
	res := u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)})
	wantStatus(t, res, 200)

	if sar := h.hss.wantSAR(t, cx.AssignmentRegistration); sar.UserDataAlreadyAvailable ||
		!reflect.DeepEqual(sar.PublicIdentities, []string{secondIMPU}) {
		t.Fatalf("SAR = %+v", sar)
	}

	h.hss.noCx(t)

	if got := res.Header.Get("P-Associated-URI"); got != "<"+secondIMPU+">" {
		t.Errorf("P-Associated-URI = %q", got)
	}

	first, second := h.registration(testIMPU), h.registration(secondIMPU)
	if first.ID == second.ID || first.Bindings[0].Contact.ID != second.Bindings[0].Contact.ID {
		t.Fatalf("registrations %+v and %+v, want two sets bound to one contact", first, second)
	}

	u.impu = unknownIMPU
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 403)
	h.hss.nextSAR(t)
}

func TestNewContactReplacesOldOne(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	u.impu = secondIMPU
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
	h.hss.nextSAR(t)

	old := h.registration(testIMPU).Bindings[0].Contact

	u.impu = testIMPU
	u.contact = "sip:001010000000001@127.0.0.2:5060"
	u.callID = "new@127.0.0.1"

	res := u.register(registerOptions{})

	h.hss.wantSAR(t, cx.AssignmentReRegistration)

	if sar := h.hss.wantSAR(t, cx.AssignmentAdministrativeDeregistration); !reflect.DeepEqual(sar.PublicIdentities, []string{secondIMPU}) {
		t.Fatalf("SAR = %+v", sar)
	}

	regs := h.registrations(testIMPI)
	if len(regs) != 1 || len(regs[0].Bindings) != 1 || regs[0].Bindings[0].Contact.URI != u.contact {
		t.Fatalf("registrations = %+v, want the first set bound to %s only", regs, u.contact)
	}

	if got := contacts(t, res); !reflect.DeepEqual(got, map[string]string{u.contact: "3600"}) {
		t.Errorf("Contact = %v", got)
	}

	if got, want := res.Header.Get("Service-Route"), serviceRoute(h.reg.cfg.Name, regs[0].Bindings[0].Contact.ID); got != want ||
		regs[0].Bindings[0].Contact.ID == old.ID {
		t.Errorf("Service-Route = %q, want %q for the new contact", got, want)
	}

	u.contact = old.URI
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 403)
	h.hss.noCx(t)
}

func TestSeveralContacts(t *testing.T) {
	t.Run("same address", func(t *testing.T) {
		h := newHarness(t)
		u := h.newUE()

		res := u.register(registerOptions{contact: "<sip:a@127.0.0.1:5070>, <sip:b@127.0.0.1:5070>;+g.3gpp.smsip, <sip:a@127.0.0.1:5070>"})

		h.hss.nextSAR(t)

		want := map[string]string{"sip:a@127.0.0.1:5070": "3600", "sip:b@127.0.0.1:5070": "3600"}
		if got := contacts(t, res); !reflect.DeepEqual(got, want) {
			t.Fatalf("Contact = %v, want %v", got, want)
		}

		if reg := h.registration(testIMPU); len(reg.Bindings) != 2 || reg.Bindings[1].Contact.Params != ";+g.3gpp.smsip" {
			t.Fatalf("bindings = %+v", reg.Bindings)
		}
	})

	t.Run("different addresses", func(t *testing.T) {
		h := newHarness(t)
		u := h.newUE()

		res := u.register(registerOptions{contact: "<sip:a@127.0.0.2>;q=0.5, <sip:b@127.0.0.3>;q=0.9"})

		h.hss.nextSAR(t)

		if got := contacts(t, res); !reflect.DeepEqual(got, map[string]string{"sip:b@127.0.0.3": "3600"}) {
			t.Fatalf("Contact = %v", got)
		}
	})
}

func TestDeregistration(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts registerOptions
	}{
		{"contact", registerOptions{expires: "0"}},
		{"contact expires", registerOptions{expires: "600", contact: "<sip:001010000000001@127.0.0.1>;expires=0"}},
		{"star", registerOptions{expires: "0", contact: "*"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()

			if tt.opts.contact != "" && tt.opts.contact != "*" {
				u.contact = "sip:001010000000001@127.0.0.1"
			}

			u.register(registerOptions{})
			h.hss.nextSAR(t)

			o := tt.opts
			o.auth = u.protected(testNonce(), testVector.XRES)

			res := u.send(o)
			wantStatus(t, res, 200)

			if sar := h.hss.wantSAR(t, cx.AssignmentUserDeregistration); !reflect.DeepEqual(sar.PublicIdentities, []string{testIMPU}) {
				t.Fatalf("SAR = %+v", sar)
			}

			h.wantUnregistered()

			if got := contacts(t, res); !reflect.DeepEqual(got, map[string]string{u.contact: "0"}) {
				t.Errorf("Contact = %v", got)
			}
		})
	}
}

func TestPartialDeregistration(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{contact: "<sip:a@127.0.0.1:5070>, <sip:b@127.0.0.1:5070>", expires: "600"})
	h.hss.nextSAR(t)

	res := u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: "<sip:a@127.0.0.1:5070>;expires=0"})
	wantStatus(t, res, 200)
	h.hss.noCx(t)

	want := map[string]string{"sip:a@127.0.0.1:5070": "0", "sip:b@127.0.0.1:5070": "600"}
	if got := contacts(t, res); !reflect.DeepEqual(got, want) {
		t.Fatalf("Contact = %v, want %v", got, want)
	}

	if reg := h.registration(testIMPU); len(reg.Bindings) != 1 || reg.Bindings[0].Contact.URI != "sip:b@127.0.0.1:5070" {
		t.Fatalf("bindings = %+v", reg.Bindings)
	}

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: "<sip:b@127.0.0.1:5070>;expires=0"}), 200)
	h.hss.wantSAR(t, cx.AssignmentUserDeregistration)
	h.wantUnregistered()
}

func TestDeregistrationWithoutBinding(t *testing.T) {
	t.Run("other contact", func(t *testing.T) {
		h := newHarness(t)
		u := h.newUE()

		u.register(registerOptions{})
		h.hss.nextSAR(t)

		u.contact = "sip:001010000000001@127.0.0.2:5060"
		wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "0"}), 481)
		h.hss.noCx(t)
		h.registration(testIMPU)
	})

	t.Run("unregistered IMPU", func(t *testing.T) {
		h := newHarness(t)
		u := h.newUE()

		u.register(registerOptions{})
		h.hss.nextSAR(t)

		u.impu = secondIMPU
		wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "0"}), 481)
		h.hss.noCx(t)
		h.registration(testIMPU)
	})

	t.Run("after a challenge", func(t *testing.T) {
		h := newHarness(t)
		u := h.newUE()

		nonce := u.challenged(registerOptions{expires: "0"})
		wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES), expires: "0"}), 481)
		h.hss.noCx(t)
	})
}

func TestSharedIMPU(t *testing.T) {
	h := newHarness(t)

	phone := h.newUE()
	phone.register(registerOptions{})
	h.hss.nextSAR(t)

	tablet := h.newUE()
	tablet.impi = "tablet@" + homeDomain
	tablet.impu = testAlias
	tablet.contact = "sip:tablet@127.0.0.1:5090"

	res := tablet.register(registerOptions{})

	h.hss.nextSAR(t)

	want := map[string]string{phone.contact: "3600", tablet.contact: "3600"}
	if got := contacts(t, res); !reflect.DeepEqual(got, want) {
		t.Fatalf("Contact = %v, want %v", got, want)
	}

	if regs := h.registrations(tablet.impi); len(regs) != 1 || !reflect.DeepEqual(regs[0].Identities, wantIdentities) {
		t.Fatalf("tablet registrations = %+v", regs)
	}

	h.registration(testIMPU)
}

func TestTimeoutDeregistration(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{expires: "600"})
	h.hss.nextSAR(t)

	h.clock.Advance(600*time.Second - time.Millisecond)
	h.registration(testIMPU)
	h.hss.noCx(t)

	h.clock.Advance(sweepInterval)

	if sar := h.hss.wantSAR(t, cx.AssignmentTimeoutDeregistration); !reflect.DeepEqual(sar.PublicIdentities, []string{testIMPU}) {
		t.Fatalf("SAR = %+v", sar)
	}

	h.wantUnregistered()

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 500)
}

func TestSlowSARDoesNotDelayOtherExpiries(t *testing.T) {
	h := newHarness(t)

	phone := h.newUE()
	phone.register(registerOptions{expires: "600"})
	h.hss.nextSAR(t)

	tablet := h.newUE()
	tablet.impi = "tablet@" + homeDomain
	tablet.impu = testAlias
	tablet.contact = "sip:tablet@127.0.0.1:5090"
	tablet.register(registerOptions{expires: "600"})
	h.hss.nextSAR(t)

	gate := make(chan struct{})

	h.hss.set(func(h *fakeHSS) { h.sarGate = gate })

	h.clock.Advance(600*time.Second - sweepInterval)

	stop := make(chan struct{})
	ticking := make(chan struct{})

	go func() {
		defer close(ticking)

		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				h.clock.Advance(sweepInterval)
			}
		}
	}()

	impis := map[string]bool{}

	for len(impis) < 2 {
		sar := h.hss.nextSAR(t)
		if sar.Type != cx.AssignmentTimeoutDeregistration {
			t.Fatalf("SAR = %+v, want TIMEOUT_DEREGISTRATION", sar)
		}

		impis[sar.PrivateIdentity] = true
	}

	close(gate)
	close(stop)
	<-ticking

	if !impis[testIMPI] || !impis[tablet.impi] {
		t.Fatalf("timeout SARs for %v, want both private identities while the first is unanswered", impis)
	}
}

func TestSweepWaitsForBusyIMPI(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{expires: "600"})
	h.hss.nextSAR(t)

	if !h.reg.tryLock(testIMPI) {
		t.Fatal("IMPI busy")
	}

	h.clock.Advance(600 * time.Second)
	h.hss.noCx(t)

	h.reg.unlock(testIMPI)
	h.clock.Advance(sweepInterval)

	h.hss.wantSAR(t, cx.AssignmentTimeoutDeregistration)
	h.wantUnregistered()
}

func TestRegisterWaitsForBackgroundWork(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	if err := h.reg.lock(t.Context(), testIMPI); err != nil {
		t.Fatal(err)
	}

	u.sock.Send(sip.UDP, h.scscf, u.request(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}))
	u.sock.RecvNone(100 * time.Millisecond)

	h.reg.unlock(testIMPI)
	wantStatus(t, u.recv(), 200)
}

func TestExpires(t *testing.T) {
	tests := []struct {
		name    string
		opts    registerOptions
		granted string
	}{
		{"default", registerOptions{}, "3600"},
		{"header", registerOptions{expires: "300"}, "300"},
		{"clamped", registerOptions{expires: "7200"}, "3600"},
		{"contact param over header", registerOptions{expires: "300", contact: "<sip:001010000000001@127.0.0.1>;expires=900"}, "900"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()

			res := u.register(tt.opts)

			c, err := sip.ParseAddress(res.Header.Get("Contact"))
			if err != nil {
				t.Fatal(err)
			}

			if got, _ := c.Params.Get("expires"); got != tt.granted {
				t.Fatalf("expires = %s, want %s", got, tt.granted)
			}

			n, _ := strconv.Atoi(tt.granted)
			if b := h.registration(testIMPU).Bindings[0]; !b.ExpiresAt.Equal(testEpoch.Add(time.Duration(n) * time.Second)) {
				t.Fatalf("ExpiresAt = %v, want %s after %v", b.ExpiresAt, tt.granted, testEpoch)
			}
		})
	}
}

func TestIntervalTooBrief(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	for _, o := range []registerOptions{
		{expires: "30"},
		{expires: "3600", contact: "<sip:001010000000001@127.0.0.1>;expires=59"},
	} {
		o.auth = u.unprotected()

		res := u.send(o)
		wantStatus(t, res, 423)

		if got := res.Header.Get("Min-Expires"); got != "60" {
			t.Fatalf("Min-Expires = %q, want 60", got)
		}
	}

	h.hss.noCx(t)
}

func TestBadRequests(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	for name, o := range map[string]registerOptions{
		"no Contact":             {noContact: true},
		"star without expires 0": {contact: "*", expires: "60"},
		"star without expires":   {contact: "*"},
		"star and a contact":     {contact: "*, <sip:a@127.0.0.1>", expires: "0"},
		"bad Expires":            {expires: "soon"},
		// RFC 3261 §25.1
		"q-value not a number": {contact: "<sip:a@127.0.0.1>;q=NaN"},
		"q-value above 1":      {contact: "<sip:a@127.0.0.1>;q=5"},
		"q-value exponent":     {contact: "<sip:a@127.0.0.1>;q=1e-1"},
	} {
		t.Run(name, func(t *testing.T) {
			o.auth = u.unprotected()
			wantStatus(t, u.send(o), 400)
		})
	}

	h.hss.noCx(t)
}

func TestHSSErrors(t *testing.T) {
	tests := []struct {
		name       string
		set        func(h *fakeHSS)
		atSAR      bool
		code       int
		retryAfter bool
	}{
		{"MAR user unknown", func(h *fakeHSS) { h.marResult = tgpp.ResultErrorUserUnknown }, false, 403, false},
		{"MAR identities don't match", func(h *fakeHSS) { h.marResult = tgpp.ResultErrorIdentitiesDontMatch }, false, 403, false},
		{"MAR unable to comply", func(h *fakeHSS) { h.marResult = diameter.ResultUnableToComply }, false, 500, false},
		{"MAR without an AKAv1 vector", func(h *fakeHSS) { h.akaScheme = cx.SchemeDigestAKAv2MD5 }, false, 500, false},
		{"SAR user unknown", func(h *fakeHSS) { h.sarResult = tgpp.ResultErrorUserUnknown }, true, 403, false},
		{"SAR identities don't match", func(h *fakeHSS) { h.sarResult = tgpp.ResultErrorIdentitiesDontMatch }, true, 403, false},
		{"SAR unable to comply", func(h *fakeHSS) { h.sarResult = diameter.ResultUnableToComply }, true, 500, false},
		{"MAR too busy", func(h *fakeHSS) { h.marResult = diameter.ResultTooBusy }, false, 500, true},
		{"MAR roaming not allowed", func(h *fakeHSS) { h.marResult = tgpp.ResultErrorRoamingNotAllowed }, false, 403, false},
		{"MAR auth scheme not supported", func(h *fakeHSS) { h.marResult = tgpp.ResultErrorAuthSchemeNotSupported }, false, 403, false},
		{"SAR identity already registered", func(h *fakeHSS) { h.sarResult = tgpp.ResultErrorIdentityAlreadyRegistered }, true, 500, false},
		{"SAR error in assignment type", func(h *fakeHSS) { h.sarResult = tgpp.ResultErrorInAssignmentType }, true, 500, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()

			h.hss.set(tt.set)

			var res *sip.Response

			if tt.atSAR {
				nonce := u.challenged(registerOptions{})
				res = u.send(registerOptions{auth: u.protected(nonce, testVector.XRES)})

				h.hss.nextSAR(t)
			} else {
				res = u.send(registerOptions{auth: u.unprotected()})

				h.hss.nextMAR(t)
			}

			wantStatus(t, res, tt.code)

			if got := res.Header.Has("Retry-After"); got != tt.retryAfter {
				t.Fatalf("Retry-After present = %t, want %t", got, tt.retryAfter)
			}

			h.wantUnregistered()
		})
	}
}

func TestHSSDown(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	h.loop.SetDown(true)

	res := u.send(registerOptions{auth: u.unprotected()})
	wantStatus(t, res, 500)

	if res.Header.Get("Retry-After") != strconv.Itoa(retryAfter) {
		t.Fatalf("Retry-After = %q, want %d", res.Header.Get("Retry-After"), retryAfter)
	}
}

func TestRegisterWaitsForTheOneInFlight(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	gate := make(chan struct{})

	h.hss.set(func(h *fakeHSS) { h.marGate = gate })

	u.sock.Send(sip.UDP, h.scscf, u.request(registerOptions{auth: u.unprotected()}))
	h.hss.nextMAR(t)

	u.sock.Send(sip.UDP, h.scscf, u.request(registerOptions{auth: u.unprotected()}))
	u.sock.RecvNone(100 * time.Millisecond)
	h.hss.noCx(t)

	close(gate)
	wantStatus(t, u.recv(), 401)
	h.hss.nextMAR(t)
	wantStatus(t, u.recv(), 401)
}

func TestVerifyRFC2617Example(t *testing.T) {
	c := &credentials{
		username: "Mufasa",
		realm:    "testrealm@host.com",
		uri:      "/dir/index.html",
		response: "6629fae49393a05397450978507c4ef1",
		qop:      "auth",
		nc:       "00000001",
		cnonce:   "0a4f113b",
	}

	if !verify(c, "GET", "dcd98b7102dd2f0e8b11d0f600bfb0c093", []byte("Circle Of Life")) {
		t.Fatal("verify = false, want true")
	}

	c.nc = "00000002"
	if verify(c, "GET", "dcd98b7102dd2f0e8b11d0f600bfb0c093", []byte("Circle Of Life")) {
		t.Fatal("verify with another nc = true, want false")
	}
}

func TestIdentityKey(t *testing.T) {
	for in, want := range map[string]string{
		"sip:+15551230001@IMS.Example.org;user=Phone;transport=tcp": "sip:+15551230001@ims.example.org;user=phone",
		"SIP:alice@example.org:5060":                                "sip:alice@example.org:5060",
		"sip:Alice@example.org":                                     "sip:Alice@example.org",
		"tel:+1-555-123-0001":                                       "tel:+15551230001",
		"tel:1234;phone-context=IMS.Example.org":                    "tel:1234;phone-context=ims.example.org",
		"sip:example.org":                                           "sip:example.org",
	} {
		if got := identityKeyOf(in); got != want {
			t.Errorf("identityKeyOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrivateIdentity(t *testing.T) {
	u, err := sip.ParseURI("sip:001010000000001@IMS.mnc001.mcc001.3gppnetwork.org:5060;transport=tcp")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := privateIdentity(u), "001010000000001@IMS.mnc001.mcc001.3gppnetwork.org"; got != want {
		t.Fatalf("privateIdentity = %q, want %q", got, want)
	}
}

func TestReauthInterval(t *testing.T) {
	h := newHarness(t)
	h.cfg.ReauthInterval = 10 * time.Minute
	h.restart()

	u := h.newUE()

	u.register(registerOptions{expires: "3600"})
	h.hss.nextSAR(t)

	h.clock.Advance(5 * time.Minute)

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "3600"}), 200)
	h.hss.noCx(t)

	h.clock.Advance(6 * time.Minute)

	res := u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "3600"})
	wantStatus(t, res, 401)
	h.hss.nextMAR(t)

	nonce := sip.Unquote(challengeParams(t, res)["nonce"])

	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES), expires: "3600"}), 200)
	h.hss.wantSAR(t, cx.AssignmentReRegistration)

	h.clock.Advance(5 * time.Minute)

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "3600"}), 200)
	h.hss.noCx(t)

	h.clock.Advance(20 * time.Minute)

	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "0"}), 200)
	h.hss.wantSAR(t, cx.AssignmentUserDeregistration)
}
