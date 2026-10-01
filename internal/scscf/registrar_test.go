package scscf

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

var wantIdentities = []db.PublicIdentity{
	{URI: testIMPU},
	{URI: testBarred, Barred: true},
	{URI: testMSISDN},
	{URI: testTel},
}

func (h *harness) registration() db.Registration {
	h.t.Helper()

	reg, err := h.db.GetRegistration(context.Background(), testIMPI)
	if err != nil {
		h.t.Fatalf("GetRegistration: %v", err)
	}

	return reg
}

func (h *harness) wantUnregistered() {
	h.t.Helper()

	if _, err := h.db.GetRegistration(context.Background(), testIMPI); !errors.Is(err, db.ErrNotFound) {
		h.t.Fatalf("GetRegistration err = %v, want ErrNotFound", err)
	}
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

	reg := h.registration()

	wantHeaders := map[string]string{
		"Path":              testPath,
		"Service-Route":     "<sip:orig-" + strconv.FormatInt(reg.ID, 10) + "@scscf." + homeDomain + ":5060;lr>",
		"P-Associated-URI":  "<" + testIMPU + ">, <" + testMSISDN + ">, <" + testTel + ">",
		"Contact":           "<" + u.contact + ">;+sip.instance=" + testInstance + ";+g.3gpp.smsip;expires=600",
		"P-Charging-Vector": "icid-value=" + testICID,
	}
	for name, value := range wantHeaders {
		if got := res.Header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}

	if res.Header.Get("To") == "" || !bytes.Contains([]byte(res.Header.Get("To")), []byte("tag=")) {
		t.Errorf("To = %q, want a tag", res.Header.Get("To"))
	}

	wantReg := db.Registration{
		ID:           reg.ID,
		IMPI:         testIMPI,
		Contact:      "<" + u.contact + ">",
		InstanceID:   "<urn:gsma:imei:35622410-483840-0>",
		CallID:       u.callID,
		CSeq:         2,
		UEAddress:    loopback,
		Path:         testPath,
		Identities:   wantIdentities,
		RegisteredAt: testEpoch,
		ExpiresAt:    testEpoch.Add(600 * time.Second),
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

	// The challenge is gone: even the right answer is refused.
	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES)}), 403)
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
			o.auth = u.protected(nonce, testVector.XRES) + ", algorithm=MD5"
		}},
		{"other nonce", func(u *ue, o *registerOptions, _ string) {
			o.auth = u.protected(base64.StdEncoding.EncodeToString(make([]byte, 32)), testVector.XRES)
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

	// The P-CSCF says the REGISTER didn't come over the SAs: a new challenge.
	auth := u.protected(nonce, testVector.XRES)
	auth = auth[:len(auth)-len(`"yes"`)] + `"no"`

	wantStatus(t, u.send(registerOptions{auth: auth}), 401)
	h.hss.nextMAR(t)
	h.hss.noCx(t)
}

func TestResync(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	nonce := u.challenged(registerOptions{})

	auts := bytes.Repeat([]byte{0x0a}, autsLen)
	res := u.send(registerOptions{
		auth: u.protected(nonce, nil) + `, auts="` + base64.StdEncoding.EncodeToString(auts) + `"`,
	})
	wantStatus(t, res, 401)

	mar := h.hss.nextMAR(t)
	if mar.Resync == nil || !bytes.Equal(mar.Resync.RAND, testVector.RAND) || !bytes.Equal(mar.Resync.AUTS, auts) {
		t.Fatalf("MAR resync = %+v, want RAND‖AUTS", mar.Resync)
	}

	// The new challenge registers.
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
	h.hss.wantSAR(t, cx.AssignmentRegistration)
}

func TestRegAwaitAuthExpiry(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	nonce := u.challenged(registerOptions{})

	h.clock.Advance(regAwaitAuth)

	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES)}), 403)
	h.wantUnregistered()
	h.hss.noCx(t)
}

func TestReRegistrationWithoutChallenge(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{expires: "600"})
	h.hss.nextSAR(t)

	before := h.registration()

	h.clock.Advance(5 * time.Minute)

	res := u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "1200"})
	wantStatus(t, res, 200)
	h.hss.noCx(t)

	if got := res.Header.Get("Service-Route"); got != serviceRoute(homeDomain, sipPort, before.ID) {
		t.Errorf("Service-Route = %q", got)
	}

	after := h.registration()
	if after.ID != before.ID || after.CSeq != 3 || !after.ExpiresAt.Equal(h.clock.Now().Add(1200*time.Second)) ||
		!after.RegisteredAt.Equal(before.RegisteredAt) {
		t.Fatalf("registration = %+v", after)
	}
}

func TestReRegistrationWithChallenge(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	before := h.registration()

	u.register(registerOptions{})

	if sar := h.hss.wantSAR(t, cx.AssignmentReRegistration); !sar.UserDataAlreadyAvailable {
		t.Fatalf("SAR = %+v, want User-Data-Already-Available", sar)
	}

	after := h.registration()
	if after.ID != before.ID || !reflect.DeepEqual(after.Identities, wantIdentities) {
		t.Fatalf("registration = %+v", after)
	}
}

func TestReRegistrationFromNewContact(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{})
	h.hss.nextSAR(t)

	before := h.registration()

	u.contact = "sip:001010000000001@127.0.0.2:5060"
	u.callID = "new@127.0.0.1"

	res := u.register(registerOptions{})

	h.hss.wantSAR(t, cx.AssignmentReRegistration)

	after := h.registration()
	if after.ID == before.ID || after.Contact != "<"+u.contact+">" || after.CallID != u.callID ||
		!reflect.DeepEqual(after.Identities, wantIdentities) {
		t.Fatalf("registration = %+v, want a new one at %s", after, u.contact)
	}

	if got := res.Header.Get("Service-Route"); got != serviceRoute(homeDomain, sipPort, after.ID) {
		t.Errorf("Service-Route = %q", got)
	}

	// The old contact can no longer refresh.
	u.contact = "sip:001010000000001@127.0.0.1:" + strconv.Itoa(int(u.sock.Addr().Port()))
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 403)
	h.hss.noCx(t)
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

			if res.Header.Has("Contact") {
				t.Errorf("Contact = %q, want none", res.Header.Get("Contact"))
			}
		})
	}
}

func TestTimeoutDeregistration(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	u.register(registerOptions{expires: "600"})
	h.hss.nextSAR(t)

	h.clock.Advance(600*time.Second - sweepInterval)
	h.registration()
	h.hss.noCx(t)

	h.clock.Advance(sweepInterval)

	if sar := h.hss.wantSAR(t, cx.AssignmentTimeoutDeregistration); !reflect.DeepEqual(sar.PublicIdentities, []string{testIMPU}) {
		t.Fatalf("SAR = %+v", sar)
	}

	h.wantUnregistered()

	// An expired registration can't be refreshed without a challenge.
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 403)
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
			if reg := h.registration(); !reg.ExpiresAt.Equal(testEpoch.Add(time.Duration(n) * time.Second)) {
				t.Fatalf("ExpiresAt = %v, want %s after %v", reg.ExpiresAt, tt.granted, testEpoch)
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
		"two contacts":           {contact: "<sip:a@127.0.0.1>, <sip:b@127.0.0.1>"},
		"bad Expires":            {expires: "soon"},
	} {
		t.Run(name, func(t *testing.T) {
			o.auth = u.unprotected()
			wantStatus(t, u.send(o), 400)
		})
	}

	h.hss.noCx(t)
}

func TestBarredIdentity(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	u.impu = testBarred

	nonce := u.challenged(registerOptions{})

	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES)}), 403)
	h.hss.wantSAR(t, cx.AssignmentRegistration)
	h.wantUnregistered()
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
		{"MAR unable to comply", func(h *fakeHSS) { h.marResult = diameter.ResultUnableToComply }, false, 500, true},
		{"MAR without an AKAv1 vector", func(h *fakeHSS) { h.akaScheme = cx.SchemeDigestAKAv2MD5 }, false, 500, false},
		{"SAR user unknown", func(h *fakeHSS) { h.sarResult = tgpp.ResultErrorUserUnknown }, true, 403, false},
		{"SAR identities don't match", func(h *fakeHSS) { h.sarResult = tgpp.ResultErrorIdentitiesDontMatch }, true, 403, false},
		{"SAR unable to comply", func(h *fakeHSS) { h.sarResult = diameter.ResultUnableToComply }, true, 500, true},
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

	h.hss.shutdown()
	h.waitHSS(diameter.PeerDown)

	res := u.send(registerOptions{auth: u.unprotected()})
	wantStatus(t, res, 500)

	if res.Header.Get("Retry-After") != strconv.Itoa(retryAfter) {
		t.Fatalf("Retry-After = %q, want %d", res.Header.Get("Retry-After"), retryAfter)
	}
}

func TestRegisterWhileMARInFlight(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	gate := make(chan struct{})

	h.hss.set(func(h *fakeHSS) { h.marGate = gate })

	u.sock.Send(sip.UDP, h.scscf, u.request(registerOptions{auth: u.unprotected()}))
	h.hss.nextMAR(t)

	res := u.send(registerOptions{auth: u.unprotected()})
	wantStatus(t, res, 500)

	if !res.Header.Has("Retry-After") {
		t.Fatal("500 without Retry-After")
	}

	close(gate)
	wantStatus(t, u.recv(), 401)
}

func TestVerifyRFC2617Example(t *testing.T) {
	// RFC 2617 §3.5, with the password as the AKA XRES.
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

func TestNormalizeIdentity(t *testing.T) {
	for in, want := range map[string]string{
		"sip:+15551230001@IMS.Example.org;user=phone": "sip:+15551230001@ims.example.org",
		"SIP:alice@example.org:5060":                  "sip:alice@example.org:5060",
		"tel:+1-555-123-0001;phone-context=x":         "tel:+15551230001",
		"sip:example.org":                             "sip:example.org",
	} {
		if got := normalizeIdentity(in); got != want {
			t.Errorf("normalizeIdentity(%q) = %q, want %q", in, got, want)
		}
	}
}
