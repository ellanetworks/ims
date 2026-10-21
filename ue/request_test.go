package ue

import (
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

// respond is a response of the UE's agent to req.
func respond(u *UE, req *sip.Request, code int) *sip.Response {
	res := sip.NewResponse(req, code, "")
	u.respondHeaders(req, res)

	return res
}

func TestResponsesCarryAccessNetworkInfo(t *testing.T) {
	n := newNetwork(t)
	u, _ := n.newUE(Config{})

	req := siptest.NewRequest("BYE", "sip:ue@127.0.0.1", sip.UDP, n.PCSCF.Addr())

	if got := respond(u.UE, req, 200).Header.Get("P-Access-Network-Info"); got == "" {
		t.Fatal("response without P-Access-Network-Info")
	}
}

// RFC 3261 §8.1.2, RFC 3263 §4.1
func TestRequestTransportFollowsTheRoute(t *testing.T) {
	u, _ := newNetwork(t).newUE(Config{Plain: true})

	for _, tc := range []struct {
		route string
		want  sip.Transport
	}{
		{"", sip.UDP},
		{"<sip:127.0.0.1:5063;lr>", sip.UDP},
		{"<sip:127.0.0.1:5063;transport=tcp;lr>, <sip:127.0.0.1:5060;lr>", sip.TCP},
	} {
		req := sip.NewRequest("BYE", sip.URI{Scheme: "sip", Host: "127.0.0.1"})
		if tc.route != "" {
			req.Header.Add("Route", tc.route)
		}

		if got := u.requestTransport(req); got != tc.want {
			t.Errorf("Route %q: transport %s, want %s", tc.route, got, tc.want)
		}
	}
}
