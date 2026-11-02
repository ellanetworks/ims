package scscf

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

const testOutboundPath = "<sip:token1@pcscf." + homeDomain + ";lr;ob>"

func (u *ue) flowContact(uri, regID string) string {
	return "<" + uri + ">;+sip.instance=" + testInstance + ";reg-id=" + regID
}

func (h *harness) onlyBinding() db.Binding {
	h.t.Helper()

	reg := h.registration(testIMPU)
	if len(reg.Bindings) != 1 {
		h.t.Fatalf("bindings %+v, want one", reg.Bindings)
	}

	return reg.Bindings[0]
}

// RFC 5626 section 6
// TS 24.229 clause 5.4.1.2.2
func TestRegisteredBinding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		contact func(u *ue) string
		path    string
		regID   int64
	}{
		{"flow", func(u *ue) string { return u.flowContact(u.contact, "1") }, testOutboundPath, 1},
		{"first hop without ob", func(u *ue) string { return u.flowContact(u.contact, "1") }, testPath, 0},
		{"reg-id without instance", func(u *ue) string { return "<" + u.contact + ">;reg-id=1" }, testOutboundPath, 0},
		{"no reg-id", func(u *ue) string { return "<" + u.contact + ">;+sip.instance=" + testInstance }, testOutboundPath, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()
			u.path = tc.path

			u.register(registerOptions{contact: tc.contact(u)})

			if c := h.onlyBinding().Contact; c.RegID != tc.regID || c.URI != u.contact {
				t.Fatalf("contact %+v, want reg-id %d", c, tc.regID)
			}
		})
	}
}

// RFC 5626 section 6
func TestFlowNewURI(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	u.path = testOutboundPath

	u.register(registerOptions{contact: u.flowContact(u.contact, "1")})

	before := h.onlyBinding()

	moved := "sip:001010000000001@127.0.0.1:5999"
	u.register(registerOptions{contact: u.flowContact(moved, "1")})

	if b := h.onlyBinding(); b.ID != before.ID || b.Contact.URI != moved || b.Contact.RegID != 1 {
		t.Fatalf("binding %+v, want binding %d with the new URI", b, before.ID)
	}
}

func TestBadRegID(t *testing.T) {
	for _, v := range []string{"0", "x", "2147483648"} {
		t.Run(v, func(t *testing.T) {
			h := newHarness(t)
			u := h.newUE()

			wantStatus(t, u.send(registerOptions{contact: u.flowContact(u.contact, v)}), 400)
		})
	}
}

func (h *harness) contacts() []string {
	h.t.Helper()

	var out []string

	for _, b := range h.registration(testIMPU).Bindings {
		out = append(out, b.Contact.URI+"#"+strconv.FormatInt(b.Contact.RegID, 10))
	}

	slices.Sort(out)

	return out
}

func flowsHarness(t *testing.T) (*harness, *ue) {
	t.Helper()

	h := newHarness(t)
	u := h.newUE()
	u.path = testOutboundPath

	return h, u
}

const (
	flowA = "sip:001010000000001@127.0.0.1:5101"
	flowB = "sip:001010000000001@127.0.0.1:5102"
)

// TS 24.229 clause 5.4.1.2.1
// TS 24.229 clause 5.4.1.2.2
func TestFlowsAddUp(t *testing.T) {
	h, u := flowsHarness(t)

	u.register(registerOptions{contact: u.flowContact(flowA, "1")})
	u.register(registerOptions{contact: u.flowContact(flowB, "2")})

	if got, want := h.contacts(), []string{flowA + "#1", flowB + "#2"}; !slices.Equal(got, want) {
		t.Fatalf("contacts %v, want %v", got, want)
	}

	u.register(registerOptions{contact: "<" + u.contact + ">"})

	if got, want := h.contacts(), []string{u.contact + "#0"}; !slices.Equal(got, want) {
		t.Fatalf("contacts %v, want %v", got, want)
	}
}

// TS 24.229 clause 5.4.1.2.2
func TestProtectedRegisterNewFlow(t *testing.T) {
	h, u := flowsHarness(t)

	u.register(registerOptions{contact: u.flowContact(flowA, "1")})

	auth := u.protected(testNonce(), testVector.XRES)
	wantStatus(t, u.send(registerOptions{auth: auth, contact: u.flowContact(flowB, "2")}), 200)
	wantStatus(t, u.send(registerOptions{auth: auth, contact: "<" + u.contact + ">"}), 403)

	if got, want := h.contacts(), []string{flowA + "#1", flowB + "#2"}; !slices.Equal(got, want) {
		t.Fatalf("contacts %v, want %v", got, want)
	}
}

// TS 24.229 clause 5.4.1.2.2
func TestFlowRefreshAndReplace(t *testing.T) {
	h, u := flowsHarness(t)

	u.register(registerOptions{contact: u.flowContact(flowA, "1")})

	first := h.onlyBinding()
	at := h.registration(testIMPU).Bindings[0].RegisteredAt

	h.clock.Advance(time.Minute)
	u.register(registerOptions{contact: u.flowContact(flowA, "1")})

	if b := h.registration(testIMPU).Bindings[0]; !b.RegisteredAt.Equal(at) || b.Event != db.BindingRefreshed {
		t.Fatalf("binding %+v, want it refreshed", b)
	}

	h.clock.Advance(time.Minute)

	u.path = "<sip:token2@pcscf." + homeDomain + ";lr;ob>"
	u.register(registerOptions{contact: u.flowContact(flowA, "1")})

	b := h.registration(testIMPU).Bindings[0]
	if b.ID != first.ID || b.Contact.Path != u.path || !b.RegisteredAt.Equal(h.clock.Now()) || b.Event != db.BindingRegistered {
		t.Fatalf("binding %+v, want flow %d registered anew over %s", b, first.ID, u.path)
	}
}

// TS 24.229 clause 5.4.1.2.1
// TS 24.229 clause 5.4.1.2.2
func TestFlowLimit(t *testing.T) {
	h, u := flowsHarness(t)

	for i := 1; i <= maxFlowsPerInstance; i++ {
		u.register(registerOptions{contact: u.flowContact(flowA, strconv.Itoa(i))})
	}

	res := u.send(registerOptions{auth: u.unprotected(), contact: u.flowContact(flowA, strconv.Itoa(maxFlowsPerInstance+1))})
	wantStatus(t, res, 403)

	u.register(registerOptions{contact: u.flowContact(flowA, "1")})

	if got := h.contacts(); len(got) != maxFlowsPerInstance {
		t.Fatalf("contacts %v", got)
	}
}

// TS 24.229 clause 5.4.1.2.1
// TS 24.229 clause 5.4.1.2.2
// RFC 5626 section 6
func TestFirstHopLacksOutbound(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	o := registerOptions{contact: u.flowContact(u.contact, "1"), supported: "path, outbound"}
	nonce := u.challenged(o)

	o.auth = u.protected(nonce, testVector.XRES)
	wantStatus(t, u.send(o), 439)

	o.supported = ""
	u.register(o)

	if c := h.onlyBinding().Contact; c.Flow() {
		t.Fatalf("contact %+v, want no flow", c)
	}
}

// TS 24.229 clause 5.4.1.2.1
// TS 24.229 clause 5.4.1.2.2F
// RFC 5626 section 6
func TestRequireOutbound(t *testing.T) {
	h, u := flowsHarness(t)

	o := registerOptions{contact: u.flowContact(flowA, "1"), auth: u.unprotected()}

	res := u.send(o)
	wantStatus(t, res, 401)
	h.hss.nextMAR(t)

	if res.Header.Has("Require") {
		t.Errorf("401 Require = %q, want none without outbound in Supported", res.Header.Get("Require"))
	}

	o.supported = "outbound"
	res = u.send(o)
	wantStatus(t, res, 401)
	h.hss.nextMAR(t)

	if got := res.Header.Get("Require"); got != "outbound" {
		t.Errorf("401 Require = %q, want outbound", got)
	}

	o.supported = ""
	nonce := sip.Unquote(challengeParams(t, res)["nonce"])
	o.auth = u.protected(nonce, testVector.XRES)

	if res := u.send(o); res.StatusCode != 200 || res.Header.Has("Require") {
		t.Errorf("got %q with Require %q, want 200 without: no outbound in Supported", res.StartLine(), res.Header.Get("Require"))
	}

	o.supported = "outbound"
	if res := u.send(o); res.StatusCode != 200 || res.Header.Get("Require") != "outbound" {
		t.Errorf("got %q with Require %q, want 200 with outbound", res.StartLine(), res.Header.Get("Require"))
	}
}

// RFC 5626 section 6
func TestSeveralContactsWithRegID(t *testing.T) {
	_, u := flowsHarness(t)

	contact := u.flowContact(flowA, "1") + ", <" + flowB + ">"
	wantStatus(t, u.send(registerOptions{contact: contact, auth: u.unprotected()}), 400)
}

// TS 24.229 clause 5.4.1.4
func TestDeregisterFlow(t *testing.T) {
	h, u := flowsHarness(t)

	u.register(registerOptions{contact: u.flowContact(flowA, "1")})
	u.register(registerOptions{contact: u.flowContact(flowB, "2")})

	res := u.register(registerOptions{contact: u.flowContact(flowA, "1"), expires: "0", supported: "outbound"})

	if got, want := h.contacts(), []string{flowB + "#2"}; !slices.Equal(got, want) {
		t.Fatalf("contacts %v, want %v", got, want)
	}

	contacts, err := res.Header.Contacts()
	if err != nil {
		t.Fatal(err)
	}

	i := slices.IndexFunc(contacts, func(c sip.Address) bool { return c.URI.String() == flowA })
	if i < 0 {
		t.Fatalf("Contact %v, want flow 1", res.Header.Values("Contact"))
	}

	expires, _ := contacts[i].Params.Get("expires")
	if regID, _ := contacts[i].Params.Get("reg-id"); expires != "0" || regID != "1" {
		t.Fatalf("Contact %v, want flow 1 with expires=0", res.Header.Values("Contact"))
	}
}

// TS 24.229 clause 5.4.1.2.2
func TestFlowReplaceNotify(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	p := h.newPCSCF()
	p.uri += ";ob"

	contact := u.flowContact(u.contact, "1")
	_, pcscfSub := h.subscribeBoth(u, p, registerOptions{contact: contact})

	h.clock.Advance(time.Second)

	u.path = "<" + strings.Replace(p.uri, "flowtoken@", "flowtoken2@", 1) + ">"
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: contact}), 200)

	n := pcscfSub.recvNotify()

	var events []string

	for _, c := range n.registration(t, testMSISDN).Contacts {
		if c.URI == u.contact {
			events = append(events, string(c.State)+"/"+string(c.Event))
		}
	}

	slices.Sort(events)

	if want := []string{"active/created", "terminated/unregistered"}; !slices.Equal(events, want) {
		t.Fatalf("contacts %v, want %v", events, want)
	}
}

// RFC 5626 section 6
// TS 24.229 clause 5.4.1.2.1
func TestNoRequireOutboundWithoutFlow(t *testing.T) {
	h, u := flowsHarness(t)

	res := u.send(registerOptions{auth: u.unprotected(), contact: "<" + u.contact + ">;+sip.instance=" + testInstance})
	wantStatus(t, res, 401)
	h.hss.nextMAR(t)

	if res.Header.Has("Require") {
		t.Fatalf("401 Require = %q, want none", res.Header.Get("Require"))
	}
}

// RFC 5626 section 6
func TestFlowDeregistrationRequiresOutbound(t *testing.T) {
	_, u := flowsHarness(t)

	u.register(registerOptions{contact: u.flowContact(flowA, "1")})

	res := u.register(registerOptions{contact: u.flowContact(flowA, "1"), expires: "0", supported: "outbound"})
	if res.Header.Get("Require") != "outbound" {
		t.Fatalf("Require = %q, want outbound", res.Header.Get("Require"))
	}
}

// RFC 5626 section 6
func TestRegIDOnAZeroExpiryContact(t *testing.T) {
	_, u := flowsHarness(t)

	contact := "<" + flowA + ">, <" + flowB + ">, " + u.flowContact(u.contact, "1") + ";expires=0"
	if res := u.send(registerOptions{contact: contact, auth: u.unprotected()}); res.StatusCode == 400 {
		t.Fatalf("got %q", res.StartLine())
	}
}

// RFC 3261 section 10.3
func TestStaleDeregistration(t *testing.T) {
	h, u := flowsHarness(t)

	u.register(registerOptions{contact: u.flowContact(flowA, "1")})
	u.register(registerOptions{contact: u.flowContact(flowB, "2")})

	stale := u.cseq - 2
	u.cseq = stale - 1

	res := u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: u.flowContact(flowA, "1"), expires: "0"})
	wantStatus(t, res, 500)

	if got := h.contacts(); len(got) != 2 {
		t.Fatalf("contacts %v, want both flows kept", got)
	}
}

// TS 33.203 clause 6.1
func TestFlowsAuthenticateTogether(t *testing.T) {
	h, one := flowsHarness(t)
	two := h.newUE()
	two.path = "<sip:token2@pcscf." + homeDomain + ";lr;ob>"

	o1 := registerOptions{contact: one.flowContact(flowA, "1")}
	o2 := registerOptions{contact: two.flowContact(flowB, "2")}

	n1, n2 := one.challenged(o1), two.challenged(o2)

	o1.auth, o2.auth = one.protected(n1, testVector.XRES), two.protected(n2, testVector.XRES)
	wantStatus(t, one.send(o1), 200)
	wantStatus(t, two.send(o2), 200)

	if got, want := h.contacts(), []string{flowA + "#1", flowB + "#2"}; !slices.Equal(got, want) {
		t.Fatalf("contacts %v, want %v", got, want)
	}
}

func TestReauthenticateEachFlow(t *testing.T) {
	h, one := flowsHarness(t)
	two := h.newUE()
	two.path = "<sip:token2@pcscf." + homeDomain + ";lr;ob>"

	one.register(registerOptions{contact: one.flowContact(flowA, "1")})
	two.register(registerOptions{contact: two.flowContact(flowB, "2")})

	if err := h.reg.Reauthenticate(t.Context(), testIMPI); err != nil {
		t.Fatal(err)
	}

	one.register(registerOptions{contact: one.flowContact(flowA, "1")})

	res := two.send(registerOptions{auth: two.protected(testNonce(), testVector.XRES), contact: two.flowContact(flowB, "2")})
	wantStatus(t, res, 401)
}

// TS 24.229 clause 5.4.1.2.2
// TS 24.229 clause 5.4.1.2.2F
// RFC 5626 section 6
func TestFlowPerRegistrationSet(t *testing.T) {
	h, u := flowsHarness(t)
	contact := u.flowContact(flowA, "1")

	res := u.register(registerOptions{contact: contact})
	firstRoute := res.Header.Get("Service-Route")

	u.impu = secondIMPU
	res = u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: contact})
	wantStatus(t, res, 200)

	if res.Header.Get("Service-Route") == firstRoute {
		t.Fatalf("Service-Route %q shared by both sets", firstRoute)
	}

	second := h.registration(secondIMPU).Bindings[0]

	h.clock.Advance(time.Minute)

	u.impu, u.path = testIMPU, "<sip:token2@pcscf."+homeDomain+";lr;ob>"
	u.register(registerOptions{contact: contact})

	if b := h.registration(secondIMPU).Bindings[0]; b.Contact.Path != testOutboundPath || !b.RegisteredAt.Equal(second.RegisteredAt) {
		t.Fatalf("second set's binding %+v changed by the first set's replace", b)
	}

	h.clock.Advance(time.Minute)

	u.impu = secondIMPU
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), contact: contact}), 200)

	if b := h.registration(secondIMPU).Bindings[0]; b.Contact.Path != u.path || b.Event != db.BindingRegistered ||
		!b.RegisteredAt.Equal(h.clock.Now()) {
		t.Fatalf("second set's binding %+v, want it replaced", b)
	}
}

// TS 24.229 clause 5.4.1.2.2
func TestUAsOfOneInstanceRegisterSetsOfTheirOwn(t *testing.T) {
	h, one := flowsHarness(t)
	two := h.newUE()
	two.impu, two.path = secondIMPU, "<sip:token2@pcscf."+homeDomain+";lr;ob>"

	o1 := registerOptions{contact: one.flowContact(one.contact, "1")}
	o2 := registerOptions{contact: two.flowContact(two.contact, "1")}

	n1, n2 := one.challenged(o1), two.challenged(o2)

	o1.auth, o2.auth = one.protected(n1, testVector.XRES), two.protected(n2, testVector.XRES)
	wantStatus(t, one.send(o1), 200)
	wantStatus(t, two.send(o2), 200)

	for impu, contact := range map[string]string{testIMPU: one.contact, secondIMPU: two.contact} {
		if b := h.registration(impu).Bindings; len(b) != 1 || b[0].Contact.URI != contact {
			t.Fatalf("%s bindings %+v, want %s alone", impu, b, contact)
		}
	}

	wantStatus(t, one.send(registerOptions{auth: o1.auth, contact: o1.contact}), 200)
	wantStatus(t, two.send(registerOptions{auth: o2.auth, contact: o2.contact}), 200)
}

func TestSameContact(t *testing.T) {
	flow := db.Contact{URI: "sip:a@127.0.0.1:5060", Instance: testInstance, RegID: 1}
	other := flow
	other.URI = "sip:b@127.0.0.1:5062"

	for name, tc := range map[string]struct {
		a, b db.Contact
		want bool
	}{
		"same flow":                   {flow, flow, true},
		"same flow of another UA":     {flow, other, false},
		"another flow of the address": {flow, db.Contact{URI: flow.URI, Instance: testInstance, RegID: 2}, false},
		"another address":             {db.Contact{URI: flow.URI}, db.Contact{URI: other.URI}, false},
		"equivalent address":          {db.Contact{URI: flow.URI}, db.Contact{URI: "SIP:a@127.0.0.1:5060"}, true},
	} {
		if got := SameContact(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: SameContact = %t, want %t", name, got, tc.want)
		}
	}
}
