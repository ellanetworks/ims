package scscf

import (
	"testing"

	"github.com/ellanetworks/ims/internal/db"
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
