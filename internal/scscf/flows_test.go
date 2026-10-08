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

func (h *harness) onlyContact() db.Contact {
	h.t.Helper()

	reg := h.registration(testIMPU)
	if len(reg.Bindings) != 1 {
		h.t.Fatalf("bindings %+v, want one", reg.Bindings)
	}

	return reg.Bindings[0].Contact
}

// RFC 5626 §6, TS 24.229 §5.4.1.2.2 step 6d: a contact is a flow when it has an instance ID and a
// reg-id and the first Path URI has "ob"; the reg-id is ignored otherwise.
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

			if c := h.onlyContact(); c.RegID != tc.regID || c.URI != u.contact {
				t.Fatalf("contact %+v, want reg-id %d", c, tc.regID)
			}
		})
	}
}

// RFC 5626 §6: the Contact URI does not identify a flow; a flow that registers a new one keeps its
// binding.
func TestFlowNewURI(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()
	u.path = testOutboundPath

	u.register(registerOptions{contact: u.flowContact(u.contact, "1")})

	before := h.onlyContact()

	moved := "sip:001010000000001@127.0.0.1:5999"
	u.register(registerOptions{contact: u.flowContact(moved, "1")})

	if c := h.onlyContact(); c.ID != before.ID || c.URI != moved || c.RegID != 1 {
		t.Fatalf("contact %+v, want binding %d with the new URI", c, before.ID)
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

// contacts lists the contacts bound to the test IMPU, by URI and reg-id.
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

// TS 24.229 §5.4.1.2.1 step 3, §5.4.1.2.2 step 6d: a new flow adds to the others; a contact address
// without reg-id replaces them all (step 4A).
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

// TS 24.229 §5.4.1.2.2 step 2: a new flow needs no initial registration, a new contact address does.
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

// TS 24.229 §5.4.1.2.2 step 6d, NOTE 6: a flow registered over the same Path is refreshed; over a new
// one it is replaced, registered anew.
func TestFlowRefreshAndReplace(t *testing.T) {
	h, u := flowsHarness(t)

	u.register(registerOptions{contact: u.flowContact(flowA, "1")})

	first := h.onlyContact()
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
	if b.Contact.ID != first.ID || b.Contact.Path != u.path || !b.RegisteredAt.Equal(h.clock.Now()) || b.Event != db.BindingRegistered {
		t.Fatalf("binding %+v, want flow %d registered anew over %s", b, first.ID, u.path)
	}
}

// TS 24.229 §5.4.1.2.1, §5.4.1.2.2: a UE has at most maxFlowsPerInstance flows; it can still refresh
// them.
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

// TS 24.229 §5.4.1.2.1 step 4B, §5.4.1.2.2 step 3, RFC 5626 §6
func TestFirstHopLacksOutbound(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	o := registerOptions{contact: u.flowContact(u.contact, "1"), supported: "path, outbound"}
	nonce := u.challenged(o)

	o.auth = u.protected(nonce, testVector.XRES)
	wantStatus(t, u.send(o), 439)

	// Without "outbound" in Supported, the reg-id is ignored.
	o.supported = ""
	u.register(o)

	if c := h.onlyContact(); c.Flow() {
		t.Fatalf("contact %+v, want no flow", c)
	}
}

// TS 24.229 §5.4.1.2.1 step 6, §5.4.1.2.2F h, RFC 5626 §6
func TestRequireOutbound(t *testing.T) {
	h, u := flowsHarness(t)

	o := registerOptions{contact: u.flowContact(flowA, "1"), auth: u.unprotected()}

	res := u.send(o)
	wantStatus(t, res, 401)
	h.hss.nextMAR(t)

	if got := res.Header.Get("Require"); got != "outbound" {
		t.Errorf("401 Require = %q, want outbound", got)
	}

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

// RFC 5626 §6
func TestSeveralContactsWithRegID(t *testing.T) {
	_, u := flowsHarness(t)

	contact := u.flowContact(flowA, "1") + ", <" + flowB + ">"
	wantStatus(t, u.send(registerOptions{contact: contact, auth: u.unprotected()}), 400)
}

// TS 24.229 §5.4.1.4 step 6a ii, step 10: a flow deregisters alone, and the 200 lists it with its
// reg-id and expiry 0.
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

// TS 24.229 §5.4.1.2.2 step 6d ii b: a replaced flow is notified as terminated, its successor as a
// new contact.
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
			events = append(events, c.State+"/"+string(c.Event))
		}
	}

	slices.Sort(events)

	if want := []string{"active/created", "terminated/unregistered"}; !slices.Equal(events, want) {
		t.Fatalf("contacts %v, want %v", events, want)
	}
}
