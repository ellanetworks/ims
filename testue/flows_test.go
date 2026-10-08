package testue

import (
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
)

// RFC 5626 §4.2, TS 24.229 §5.1.1.2.1: a UE registering a flow puts its reg-id in its Contact and
// outbound in Supported, and takes the expiry of its own flow from the 200.
func TestRegisterFlow(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{Plain: true, RegID: 2})

	done := start(u.Register)

	req, f := n.recv(n.pcscf)

	contacts, err := req.Header.Contacts()
	if err != nil || len(contacts) != 1 {
		t.Fatalf("Contact %q", req.Header.Values("Contact"))
	}

	if v, _ := contacts[0].Params.Get("reg-id"); v != "2" || !contacts[0].Params.Has("+sip.instance") {
		t.Fatalf("Contact %s, want reg-id 2 and an instance ID", contacts[0])
	}

	if !slices.Contains(req.Header.Elements("Supported"), "outbound") {
		t.Fatalf("Supported %q, want outbound", req.Header.Values("Supported"))
	}

	n.challenge(req, f, n.pcscf, false)

	req, f = n.recv(n.pcscf)

	res := sip.NewResponse(req, 200, "")
	uri := "<" + contacts[0].URI.String() + ">"
	res.Header.Add("Contact", uri+";reg-id=1;expires=100")
	res.Header.Add("Contact", uri+";reg-id=2;expires=3600")
	res.Header.Add("Require", "outbound")
	res.Header.Add("Service-Route", "<sip:orig@scscf."+domain+":6060;lr>")
	res.Header.Add("P-Associated-URI", n.associated)
	n.reply(n.pcscf, f, res)

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	if s := u.State(); !s.Outbound || time.Until(s.Expires) < time.Hour-time.Minute {
		t.Fatalf("state %+v, want flow 2 registered for an hour with outbound", s)
	}
}
