package icscf

import (
	"reflect"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/sip"
)

func TestInviteToAssignedSCSCF(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscfs[0]
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri()})

	invite := u.invite("sip:alice@"+homeDomain, func(r *sip.Request) {
		r.Header.Add("Route", "<"+h.icscfURI()+";lr>")
		r.Header.Add("P-Profile-Key", "<sip:!.*!@"+homeDomain+">")
		r.Header.Add("P-Charging-Vector", "icid-value=1234")
		r.Header.Add("P-Asserted-Identity", "<"+testIMPU+">")
	})
	u.send(invite)

	if lir := h.hss.nextLIR(t); !reflect.DeepEqual(lir, cx.LocationInfoRequest{PublicIdentity: "sip:alice@" + homeDomain}) {
		t.Fatalf("LIR = %+v", lir)
	}

	req, f := s.recv()

	if req.URI.String() != "sip:alice@"+homeDomain {
		t.Fatalf("Request-URI = %s, want it unchanged", req.URI)
	}

	if got, want := routes(t, req), []string{s.uri() + ";lr"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes = %v, want %v", got, want)
	}

	switch {
	case req.Header.Has("Record-Route"):
		t.Fatal("Record-Route added")
	case req.Header.Has("P-Profile-Key"):
		t.Fatal("P-Profile-Key forwarded")
	case req.Header.Get("P-Charging-Vector") != "icid-value=1234":
		t.Fatalf("P-Charging-Vector = %q, want it kept", req.Header.Get("P-Charging-Vector"))
	case req.Header.Get("P-Asserted-Identity") == "":
		t.Fatal("P-Asserted-Identity removed from a trusted request")
	}

	s.respond(req, f, 180)

	if res, _ := u.sock.RecvResponse(); res.StatusCode != 180 && res.StatusCode != 100 {
		t.Fatalf("got %q, want a provisional response", res.StartLine())
	}

	s.respond(req, f, 486)
	u.ack(invite, u.wantFinal(486))
}

func TestInviteSelection(t *testing.T) {
	tests := []struct {
		name   string
		answer *hssAnswer
	}{
		{"unregistered service", &hssAnswer{result: success(tgpp.ResultUnregisteredService)}},
		{"capabilities", &hssAnswer{result: success(tgpp.ResultUnregisteredService), caps: &cx.ServerCapabilities{Mandatory: []uint32{1}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{capabilities: [][]uint32{{1}}})
			s := h.scscfs[0]
			u := h.newUE(loopback)

			h.hss.answerLIR(tt.answer)

			invite := u.invite("sip:alice@" + homeDomain)
			u.send(invite)
			h.hss.nextLIR(t)

			req, f := s.recv()
			if got, want := routes(t, req), []string{s.uri() + ";lr"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("Routes = %v, want %v", got, want)
			}

			s.respond(req, f, 480)
			u.ack(invite, u.wantFinal(480))
		})
	}
}

func TestInviteToTelNumber(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscfs[0]
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri()})

	invite := u.invite("sip:+15551230002;cpc=ordinary@" + homeDomain + ";user=phone")
	u.send(invite)

	if lir := h.hss.nextLIR(t); lir.PublicIdentity != "tel:+15551230002" {
		t.Fatalf("LIR identity = %s, want tel:+15551230002", lir.PublicIdentity)
	}

	req, f := s.recv()
	if req.URI.String() != "tel:+15551230002;cpc=ordinary" {
		t.Fatalf("Request-URI = %s, want the tel URI with its parameters", req.URI)
	}

	s.respond(req, f, 486)
	u.ack(invite, u.wantFinal(486))
}

func TestInviteToGRUU(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscfs[0]
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri()})

	target := "sip:+15551230002@" + homeDomain + ";user=phone;gr=urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6"
	invite := u.invite(target)
	u.send(invite)

	if lir := h.hss.nextLIR(t); lir.PublicIdentity != "sip:+15551230002@"+homeDomain {
		t.Fatalf("LIR identity = %s", lir.PublicIdentity)
	}

	req, f := s.recv()
	if req.URI.String() != target {
		t.Fatalf("Request-URI = %s, want it unchanged", req.URI)
	}

	s.respond(req, f, 486)
	u.ack(invite, u.wantFinal(486))
}

func TestInviteHSSFailures(t *testing.T) {
	tests := []struct {
		name   string
		answer *hssAnswer
		want   int
	}{
		{"not registered", &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorIdentityNotRegistered)}, 480},
		{"user unknown", &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorUserUnknown)}, 404},
		{"unable to comply", &hssAnswer{result: tgpp.Result{Code: diameter.ResultUnableToComply}}, 480},
		{"timeout", nil, 480},
		{"unknown server", &hssAnswer{result: success(diameter.ResultSuccess), name: "sip:as.example.org"}, 480},
		{"no S-CSCF with the capabilities", &hssAnswer{
			result: success(tgpp.ResultUnregisteredService), caps: &cx.ServerCapabilities{Mandatory: []uint32{9}},
		}, 480},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			u := h.newUE(loopback)

			if tt.answer != nil {
				h.hss.answerLIR(tt.answer)
			}

			invite := u.invite(callee)
			u.send(invite)
			h.hss.nextLIR(t)
			u.ack(invite, u.wantFinal(tt.want))
			h.scscfs[0].sock.RecvNone(quiet)
		})
	}
}

func TestInviteCancelledDuringLIR(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	invite := u.invite(callee)
	u.send(invite)
	h.hss.nextLIR(t)

	cancel, err := sip.NewCancel(invite)
	if err != nil {
		t.Fatal(err)
	}

	u.send(cancel)

	got := map[string]int{}

	for len(got) < 2 {
		res, _ := u.sock.RecvResponse()
		if res.IsProvisional() {
			continue
		}

		cseq, _ := res.Header.CSeq()
		got[cseq.Method] = res.StatusCode

		if cseq.Method == "INVITE" {
			u.ack(invite, res)
		}
	}

	if got["CANCEL"] != 200 || got["INVITE"] != 487 {
		t.Fatalf("responses = %v, want 200 to CANCEL and 487 to INVITE", got)
	}

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: h.scscfs[0].uri()})
	h.scscfs[0].sock.RecvNone(cxTimeout + quiet)
}

func TestInviteFromOutsideTheTrustDomain(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscfs[0]
	u := h.newUE(untrusted)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri()})

	invite := u.invite("sip:+15551230002;cpc=test@"+homeDomain+";user=phone;iotl=homea-homeb", func(r *sip.Request) {
		for _, name := range []string{"P-Charging-Vector", "P-Charging-Function-Addresses", "P-Asserted-Identity", "P-Served-User", "Feature-Caps"} {
			r.Header.Add(name, "<sip:x@example.org>")
		}
	})
	u.send(invite)
	h.hss.nextLIR(t)

	req, f := s.recv()

	for _, name := range []string{"P-Charging-Vector", "P-Charging-Function-Addresses", "P-Asserted-Identity", "P-Served-User", "Feature-Caps"} {
		if req.Header.Has(name) {
			t.Errorf("%s forwarded from outside the trust domain", name)
		}
	}

	if req.URI.String() != "tel:+15551230002" {
		t.Errorf("Request-URI = %s, want tel:+15551230002", req.URI)
	}

	s.respond(req, f, 486, func(res *sip.Response) {
		res.Header.Add("Reason", "Q.850;cause=17")
		res.Header.Add("P-Asserted-Identity", "<sip:bob@"+homeDomain+">")
	})

	res := u.wantFinal(486)
	if res.Header.Has("Reason") || res.Header.Has("P-Asserted-Identity") {
		t.Fatalf("486 = %q, want trust-domain header fields removed", res)
	}

	u.ack(invite, res)
}

func TestOriginating(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"P-Served-User", map[string]string{
			"P-Served-User":       "<sip:alice@" + homeDomain + ";user=phone>;sescase=orig",
			"P-Asserted-Identity": "<sip:bob@" + homeDomain + ">",
		}, "sip:alice@" + homeDomain},
		{"P-Asserted-Identity", map[string]string{"P-Asserted-Identity": "<tel:+15551230001>, <sip:bob@" + homeDomain + ">"}, "tel:+15551230001"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			s := h.scscfs[0]
			u := h.newUE(loopback)

			h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri()})

			invite := u.invite(callee, func(r *sip.Request) {
				r.Header.Add("Route", "<"+h.icscfURI()+";lr;orig>")

				for name, value := range tt.headers {
					r.Header.Add(name, value)
				}
			})
			u.send(invite)

			if lir := h.hss.nextLIR(t); lir.PublicIdentity != tt.want || !lir.Originating {
				t.Fatalf("LIR = %+v, want originating for %s", lir, tt.want)
			}

			req, f := s.recv()
			if got, want := routes(t, req), []string{s.uri() + ";lr;orig"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("Routes = %v, want %v", got, want)
			}

			if req.URI.String() != callee {
				t.Fatalf("Request-URI = %s, want %s", req.URI, callee)
			}

			s.respond(req, f, 486)
			u.ack(invite, u.wantFinal(486))
		})
	}
}

func TestOriginatingFailures(t *testing.T) {
	tests := []struct {
		name     string
		addr     bool
		identity bool
		answer   *hssAnswer
		want     int
	}{
		{"untrusted", false, true, nil, 403},
		{"no served user", true, false, nil, 403},
		{"not registered", true, true, &hssAnswer{result: tgpp.Experimental(tgpp.ResultErrorIdentityNotRegistered)}, 404},
		{"timeout", true, true, nil, 480},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})

			addr := untrusted
			if tt.addr {
				addr = loopback
			}

			u := h.newUE(addr)

			if tt.answer != nil {
				h.hss.answerLIR(tt.answer)
			}

			invite := u.invite(callee, func(r *sip.Request) {
				r.Header.Add("Route", "<"+h.icscfURI()+";lr;orig>")

				if tt.identity {
					r.Header.Add("P-Asserted-Identity", "<"+testIMPU+">")
				}
			})
			u.send(invite)

			if tt.want != 403 {
				h.hss.nextLIR(t)
			}

			u.ack(invite, u.wantFinal(tt.want))
		})
	}
}

func TestInviteReselection(t *testing.T) {
	h := newHarness(t, harnessOptions{scscfs: 2})
	first, second := h.scscfs[0], h.scscfs[1]
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(tgpp.ResultUnregisteredService)})

	invite := u.invite("sip:alice@" + homeDomain)
	u.send(invite)
	h.hss.nextLIR(t)
	first.recv()

	req, f := second.recv()
	if req.URI.String() != "sip:alice@"+homeDomain+";scscf-reselection" {
		t.Fatalf("Request-URI = %s, want scscf-reselection", req.URI)
	}

	if got, want := routes(t, req), []string{second.uri() + ";lr"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes = %v, want %v", got, want)
	}

	second.respond(req, f, 486)
	u.ack(invite, u.wantFinal(486))
}

func TestInviteSCSCFTimeoutWithServerName(t *testing.T) {
	h := newHarness(t, harnessOptions{scscfs: 2})
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: h.scscfs[0].uri()})

	invite := u.invite(callee)
	u.send(invite)
	h.hss.nextLIR(t)
	h.scscfs[0].recv()

	u.ack(invite, u.wantFinal(408))
	h.scscfs[1].sock.RecvNone(quiet)
}

func TestInviteUseProxy(t *testing.T) {
	h := newHarness(t, harnessOptions{scscfs: 2})
	first, second := h.scscfs[0], h.scscfs[1]
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: first.uri()})

	invite := u.invite(callee)
	u.send(invite)
	h.hss.nextLIR(t)

	req, f := first.recv()
	first.respond(req, f, 305, func(res *sip.Response) { res.Header.Set("Contact", "<"+second.uri()+">") })

	req, f = second.recv()
	if got, want := routes(t, req), []string{second.uri() + ";lr"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes = %v, want %v", got, want)
	}

	second.respond(req, f, 486)
	u.ack(invite, u.wantFinal(486))
}

func TestRequestWithRouteSkipsLIR(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscfs[0]
	u := h.newUE(loopback)

	message := u.invite(callee, func(r *sip.Request) {
		r.Method = "MESSAGE"
		r.Header.Set("CSeq", "1 MESSAGE")
		r.Header.Add("Route", "<"+h.icscfURI()+";lr>, <"+s.uri()+";lr>")
	})
	u.send(message)

	req, f := s.recv()
	if got, want := routes(t, req), []string{s.uri() + ";lr"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes = %v, want %v", got, want)
	}

	s.respond(req, f, 202)
	u.wantFinal(202)
	h.hss.noCx(t)
}

func TestRequestToUnknownRoute(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	invite := u.invite(callee, func(r *sip.Request) { r.Header.Add("Route", "<sip:as.example.org;lr>") })
	u.send(invite)
	u.ack(invite, u.wantFinal(480))
	h.hss.noCx(t)
}

func TestSubsequentRequest(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscfs[0]
	u := h.newUE(untrusted)

	bye := u.invite(callee, func(r *sip.Request) {
		r.Method = "BYE"
		r.Header.Set("CSeq", "2 BYE")
		r.Header.Set("To", "<"+callee+">;tag=abc")
		r.Header.Add("Route", "<"+h.icscfURI()+";lr>, <"+s.uri()+";lr>")
		r.Header.Add("P-Charging-Vector", "icid-value=1234")
	})
	u.send(bye)

	req, f := s.recv()
	if req.Header.Has("P-Charging-Vector") {
		t.Fatal("P-Charging-Vector forwarded from outside the trust domain")
	}

	s.respond(req, f, 200)
	u.wantFinal(200)
	h.hss.noCx(t)
}

func TestSubsequentRequestWithoutRoute(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	bye := u.invite(callee, func(r *sip.Request) {
		r.Method = "BYE"
		r.Header.Set("CSeq", "2 BYE")
		r.Header.Set("To", "<"+callee+">;tag=abc")
	})
	u.send(bye)
	u.wantFinal(480)
}

func TestOptionsToICSCF(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	options := u.invite(h.icscfURI(), func(r *sip.Request) {
		r.Method = "OPTIONS"
		r.Header.Set("CSeq", "1 OPTIONS")
	})
	u.send(options)
	u.wantFinal(200)
	h.hss.noCx(t)
}

func TestUnsupportedURIScheme(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	invite := u.invite("urn:service:sos")
	u.send(invite)
	u.ack(invite, u.wantFinal(416))
	h.hss.noCx(t)
}

func TestInviteProxyChecks(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	invite := u.invite(callee, func(r *sip.Request) { r.Header.Set("Max-Forwards", "0") })
	u.send(invite)
	u.ack(invite, u.wantFinal(483))

	invite = u.invite(callee, func(r *sip.Request) { r.Header.Set("Proxy-Require", "foo") })
	u.send(invite)
	u.ack(invite, u.wantFinal(420))
	h.hss.noCx(t)
}

func TestMessageReselection(t *testing.T) {
	h := newHarness(t, harnessOptions{scscfs: 2})
	first, second := h.scscfs[0], h.scscfs[1]
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(tgpp.ResultUnregisteredService)})

	message := u.invite("sip:alice@"+homeDomain, func(r *sip.Request) {
		r.Method = "MESSAGE"
		r.Header.Set("CSeq", "1 MESSAGE")
	})
	u.send(message)
	h.hss.nextLIR(t)
	first.recv()

	req, f := second.recv()
	if req.URI.String() != "sip:alice@"+homeDomain+";scscf-reselection" {
		t.Fatalf("Request-URI = %s, want scscf-reselection", req.URI)
	}

	second.respond(req, f, 202)
	u.wantFinal(202)
}
