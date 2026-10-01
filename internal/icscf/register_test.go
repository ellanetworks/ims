package icscf

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/sip"
)

func TestRegisterToAssignedSCSCF(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscfs[0]
	u := h.newUE(loopback)

	h.hss.answerUAR(&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: s.uri()})

	register := u.register()
	u.send(register)

	uar := h.hss.nextUAR(t)

	want := cx.UserAuthorizationRequest{
		PrivateIdentity:   testIMPI,
		PublicIdentity:    testIMPU,
		VisitedNetwork:    "visited.example.org",
		AuthorizationType: cx.AuthorizationRegistration,
	}
	if !reflect.DeepEqual(uar, want) {
		t.Fatalf("UAR = %+v, want %+v", uar, want)
	}

	req, f := s.recv()

	if req.URI.String() != s.uri() {
		t.Fatalf("Request-URI = %s, want %s", req.URI, s.uri())
	}

	vias, err := req.Header.Vias()
	if err != nil || len(vias) != 2 || vias[0].Port != h.icscf.Port() {
		t.Fatalf("Vias = %+v, want the I-CSCF's on top of the sender's", vias)
	}

	for _, name := range []string{"Path", "P-Visited-Network-ID", "Authorization"} {
		if got, want := req.Header.Get(name), register.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	if req.Header.Has("Record-Route") {
		t.Fatalf("Record-Route = %q, want none", req.Header.Get("Record-Route"))
	}

	challenge := `Digest realm="` + homeDomain + `", nonce="bm9uY2U=", algorithm=AKAv1-MD5, qop="auth", ck="00", ik="11"`

	s.respond(req, f, 401, func(res *sip.Response) { res.Header.Add("WWW-Authenticate", challenge) })

	if got := u.wantFinal(401).Header.Get("WWW-Authenticate"); got != challenge {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, challenge)
	}

	h.hss.answerUAR(&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: s.uri()})
	u.send(u.register())
	h.hss.nextUAR(t)

	req, f = s.recv()
	s.respond(req, f, 200, func(res *sip.Response) {
		res.Header.Add("Path", req.Header.Get("Path"))
		res.Header.Add("Service-Route", "<sip:orig@scscf1."+homeDomain+";lr>")
		res.Header.Add("P-Profile-Key", "<sip:!.*!@"+homeDomain+">")
	})

	res := u.wantFinal(200)
	if res.Header.Get("Service-Route") == "" || res.Header.Get("Path") == "" {
		t.Fatalf("200 = %q, want Path and Service-Route", res)
	}

	if res.Header.Has("P-Profile-Key") {
		t.Fatal("P-Profile-Key relayed")
	}
}

func TestRegisterSelection(t *testing.T) {
	tests := []struct {
		name string
		caps *cx.ServerCapabilities
		want int
	}{
		{"no capabilities", nil, 0},
		{"mandatory capabilities", &cx.ServerCapabilities{Mandatory: []uint32{2}}, 1},
		{"most optional capabilities", &cx.ServerCapabilities{Optional: []uint32{2, 3}}, 1},
		{"server name", &cx.ServerCapabilities{ServerNames: []string{"sip:unknown." + homeDomain, "second"}}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{scscfs: 2, capabilities: [][]uint32{{1}, {2, 3}}})

			caps := tt.caps
			if caps != nil && len(caps.ServerNames) > 0 {
				caps = &cx.ServerCapabilities{ServerNames: []string{caps.ServerNames[0], h.scscfs[1].uri()}}
			}

			h.hss.answerUAR(&hssAnswer{result: success(tgpp.ResultFirstRegistration), caps: caps})

			u := h.newUE(loopback)
			u.send(u.register())
			h.hss.nextUAR(t)

			s := h.scscfs[tt.want]

			req, f := s.recv()
			if req.URI.String() != s.uri() {
				t.Fatalf("Request-URI = %s, want %s", req.URI, s.uri())
			}

			s.respond(req, f, 401)
			u.wantFinal(401)
		})
	}
}

func TestRegisterWithoutAMatchingSCSCF(t *testing.T) {
	h := newHarness(t, harnessOptions{capabilities: [][]uint32{{1}}})
	u := h.newUE(loopback)

	h.hss.answerUAR(&hssAnswer{result: success(tgpp.ResultFirstRegistration), caps: &cx.ServerCapabilities{Mandatory: []uint32{1, 7}}})
	u.send(u.register())
	h.hss.nextUAR(t)
	u.wantFinal(600)
	h.scscfs[0].sock.RecvNone(quiet)
}

func TestRegisterIdentities(t *testing.T) {
	tests := []struct {
		name string
		edit func(*sip.Request)
		impu string
		impi string
	}{
		{
			"derived IMPI",
			func(r *sip.Request) {
				r.Header.Del("Authorization")
				r.Header.Set("To", "<sip:alice@"+homeDomain+":5060;transport=udp>;tag=x")
			},
			"sip:alice@" + homeDomain,
			"alice@" + homeDomain,
		},
		{
			"derived IMPI from a tel URI",
			func(r *sip.Request) {
				r.Header.Del("Authorization")
				r.Header.Set("To", "<tel:+15551230001;phone-context=example.org>")
			},
			"tel:+15551230001",
			"+15551230001",
		},
		{
			"Authorization for another realm",
			func(r *sip.Request) {
				r.Header.Set("Authorization", `Digest username="other@example.org", realm="example.org", uri="sip:example.org"`)
			},
			testIMPU,
			"other@example.org",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			u := h.newUE(loopback)

			h.hss.answerUAR(&hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorUserUnknown)})
			u.send(u.register(tt.edit))

			uar := h.hss.nextUAR(t)
			if uar.PublicIdentity != tt.impu || uar.PrivateIdentity != tt.impi {
				t.Fatalf("UAR identities = %s %s, want %s %s", uar.PublicIdentity, uar.PrivateIdentity, tt.impu, tt.impi)
			}

			u.wantFinal(403)
		})
	}
}

func TestRegisterAuthorizationType(t *testing.T) {
	tests := []struct {
		name    string
		expires string
		contact string
		want    cx.AuthorizationType
	}{
		{"Expires", "600", "<sip:ue@127.0.0.1:5100>", cx.AuthorizationRegistration},
		{"Expires 0", "0", "<sip:ue@127.0.0.1:5100>", cx.AuthorizationDeregistration},
		{"contacts expires 0", "600", "<sip:ue@127.0.0.1:5100>;expires=0, <sip:ue@127.0.0.1:5101>;expires=0", cx.AuthorizationDeregistration},
		{"Expires 0 and a contact expires", "0", "<sip:ue@127.0.0.1:5100>;expires=3600", cx.AuthorizationRegistration},
		{"one contact left", "0", "<sip:ue@127.0.0.1:5100>, <sip:ue@127.0.0.1:5101>;expires=60", cx.AuthorizationRegistration},
		{"star", "0", "*", cx.AuthorizationDeregistration},
		{"no contact", "", "", cx.AuthorizationRegistration},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			u := h.newUE(loopback)

			h.hss.answerUAR(&hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorIdentityNotRegistered)})
			u.send(u.register(func(r *sip.Request) {
				r.Header.Del("Expires")
				r.Header.Del("Contact")

				if tt.expires != "" {
					r.Header.Set("Expires", tt.expires)
				}

				if tt.contact != "" {
					r.Header.Set("Contact", tt.contact)
				}
			}))

			if got := h.hss.nextUAR(t).AuthorizationType; got != tt.want {
				t.Fatalf("User-Authorization-Type = %s, want %s", got, tt.want)
			}

			u.wantFinal(403)
		})
	}
}

func TestRegisterVisitedNetwork(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"absent", "", homeDomain},
		{"token with parameters", "visited.example.org;x=1, other.example.org", "visited.example.org"},
		{"quoted", `"Visited \"network\"";x=1`, `Visited "network"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			u := h.newUE(loopback)

			h.hss.answerUAR(&hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorRoamingNotAllowed)})
			u.send(u.register(func(r *sip.Request) {
				r.Header.Del("P-Visited-Network-ID")

				if tt.header != "" {
					r.Header.Add("P-Visited-Network-ID", tt.header)
				}
			}))

			if got := h.hss.nextUAR(t).VisitedNetwork; got != tt.want {
				t.Fatalf("Visited-Network-Identifier = %q, want %q", got, tt.want)
			}

			u.wantFinal(403)
		})
	}
}

func TestRegisterHSSFailures(t *testing.T) {
	tests := []struct {
		name   string
		answer *hssAnswer
		want   int
	}{
		{"user unknown", &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorUserUnknown)}, 403},
		{"identities don't match", &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorIdentitiesDontMatch)}, 403},
		{"identity not registered", &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorIdentityNotRegistered)}, 403},
		{"roaming not allowed", &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorRoamingNotAllowed)}, 403},
		{"authorization rejected", &hssAnswer{result: tgpp.Result{Code: diameter.ResultAuthorizationRejected}}, 403},
		{"unable to comply", &hssAnswer{result: tgpp.Result{Code: diameter.ResultUnableToComply}}, 480},
		{"too busy", &hssAnswer{result: tgpp.Result{Code: diameter.ResultTooBusy}}, 480},
		{"timeout", nil, 480},
		{"unknown S-CSCF", &hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: "sip:unknown." + homeDomain}, 480},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			u := h.newUE(loopback)

			if tt.answer != nil {
				h.hss.answerUAR(tt.answer)
			}

			u.send(u.register())
			h.hss.nextUAR(t)
			u.wantFinal(tt.want)
			h.scscfs[0].sock.RecvNone(quiet)
		})
	}
}

func TestRegisterHSSDown(t *testing.T) {
	h := newHarness(t, harnessOptions{hssDown: true})
	u := h.newUE(loopback)

	u.send(u.register())
	u.wantFinal(480)
}

func TestRegisterFromOutsideTheTrustDomain(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(untrusted)

	u.send(u.register())
	u.wantFinal(403)
	h.hss.noCx(t)
}

func TestRegisterBadRequests(t *testing.T) {
	tests := []struct {
		name string
		edit func(*sip.Request)
	}{
		{"URN To", func(r *sip.Request) { r.Header.Set("To", "<urn:service:sos>") }},
		{"bad Contact", func(r *sip.Request) { r.Header.Set("Contact", "<sip:ue@") }},
		{"bad Expires", func(r *sip.Request) { r.Header.Set("Expires", "soon") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			u := h.newUE(loopback)

			u.send(u.register(tt.edit))
			u.wantFinal(400)
			h.hss.noCx(t)
		})
	}
}

func TestRegisterProxyChecks(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	u.send(u.register(func(r *sip.Request) { r.Header.Set("Max-Forwards", "0") }))
	u.wantFinal(483)

	u.send(u.register(func(r *sip.Request) { r.Header.Set("Proxy-Require", "foo") }))

	if got := u.wantFinal(420).Header.Get("Unsupported"); got != "foo" {
		t.Fatalf("Unsupported = %q, want foo", got)
	}

	h.hss.noCx(t)
}

func TestRegisterDropsOwnRoute(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscfs[0]
	u := h.newUE(loopback)

	h.hss.answerUAR(&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: s.uri()})
	u.send(u.register(func(r *sip.Request) { r.Header.Add("Route", "<"+h.icscfURI()+";lr>") }))
	h.hss.nextUAR(t)

	req, f := s.recv()
	if req.Header.Has("Route") {
		t.Fatalf("Route = %q, want none", req.Header.Get("Route"))
	}

	s.respond(req, f, 401)
	u.wantFinal(401)
}

func TestRegisterSCSCFTimeout(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscfs[0]
	u := h.newUE(loopback)

	h.hss.answerUAR(&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: s.uri()})
	u.send(u.register())
	h.hss.nextUAR(t)
	s.recv()

	u.wantFinal(504)
	h.hss.noCx(t)
}

func TestRegisterSCSCFRefuses(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscfs[0]
	u := h.newUE(loopback)

	h.hss.answerUAR(&hssAnswer{result: success(tgpp.ResultFirstRegistration)})
	u.send(u.register())
	h.hss.nextUAR(t)

	req, f := s.recv()
	s.respond(req, f, 480, func(res *sip.Response) { res.Header.Add("P-Profile-Key", "<sip:x@"+homeDomain+">") })

	if res := u.wantFinal(480); res.Header.Has("P-Profile-Key") {
		t.Fatal("P-Profile-Key relayed")
	}

	h.hss.noCx(t)
}

func TestRegisterReselection(t *testing.T) {
	tests := []struct {
		name     string
		assigned bool
		fail     func(s *fakeSCSCF, req *sip.Request, f sip.Flow)
	}{
		{"timeout with capabilities", false, func(*fakeSCSCF, *sip.Request, sip.Flow) {}},
		{"480 with capabilities", false, func(s *fakeSCSCF, req *sip.Request, f sip.Flow) { s.respond(req, f, 480) }},
		{"302 with a Server-Name", true, func(s *fakeSCSCF, req *sip.Request, f sip.Flow) {
			s.respond(req, f, 302, func(res *sip.Response) { res.Header.Set("Contact", "<sip:elsewhere@example.org>") })
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{scscfs: 2})
			first, second := h.scscfs[0], h.scscfs[1]
			u := h.newUE(loopback)

			if tt.assigned {
				h.hss.answerUAR(&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: first.uri()})
			} else {
				h.hss.answerUAR(&hssAnswer{result: success(tgpp.ResultFirstRegistration)})
			}

			u.send(u.register())
			h.hss.nextUAR(t)

			req, f := first.recv()

			if tt.assigned {
				h.hss.answerUAR(&hssAnswer{result: tgpp.Result{Code: diameter.ResultSuccess}})
			}

			tt.fail(first, req, f)

			if tt.assigned {
				if got := h.hss.nextUAR(t).AuthorizationType; got != cx.AuthorizationRegistrationAndCapabilities {
					t.Fatalf("User-Authorization-Type = %s, want REGISTRATION_AND_CAPABILITIES", got)
				}
			}

			req, f = second.recv()
			if want := second.uri() + ";scscf-reselection"; req.URI.String() != want {
				t.Fatalf("Request-URI = %s, want %s", req.URI, want)
			}

			second.respond(req, f, 401)
			u.wantFinal(401)
		})
	}
}

func TestRegisterNoReselection(t *testing.T) {
	tests := []struct {
		name string
		edit func(*sip.Request)
		want int
	}{
		{"integrity protected", func(r *sip.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), `"no"`, `"yes"`, 1))
		}, 480},
		{"no Authorization", func(r *sip.Request) { r.Header.Del("Authorization") }, 480},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{scscfs: 2})
			first := h.scscfs[0]
			u := h.newUE(loopback)

			h.hss.answerUAR(&hssAnswer{result: success(tgpp.ResultFirstRegistration)})
			u.send(u.register(tt.edit))
			h.hss.nextUAR(t)

			req, f := first.recv()
			first.respond(req, f, 480)

			u.wantFinal(tt.want)
			h.scscfs[1].sock.RecvNone(quiet)
		})
	}
}

func TestRegisterReselectionKeepsAssignedSCSCF(t *testing.T) {
	h := newHarness(t, harnessOptions{scscfs: 2})
	first := h.scscfs[0]
	u := h.newUE(loopback)

	h.hss.answerUAR(
		&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: first.uri()},
		&hssAnswer{result: success(tgpp.ResultSubsequentRegistration), name: first.uri()},
	)
	u.send(u.register())
	h.hss.nextUAR(t)

	req, f := first.recv()
	first.respond(req, f, 480)

	h.hss.nextUAR(t)
	u.wantFinal(480)
	h.scscfs[1].sock.RecvNone(quiet)
}
