package icscf

import (
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/sip"
)

func TestInviteToAssignedSCSCF(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscf
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
			h := newHarness(t, harnessOptions{capabilities: []uint32{1}})
			s := h.scscf
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
	s := h.scscf
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

func TestInviteIdentities(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{"tel:+1-555-123-0002", "tel:+15551230002"},
		{"tel:+1.555.(123).0002;phone-context=" + homeDomain, "tel:+15551230002"},
		{"sip:+1-555-123-0002@" + homeDomain + ";user=phone", "tel:+15551230002"},
		{"sips:alice@" + strings.ToUpper(homeDomain), "sip:alice@" + homeDomain},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			s := h.scscf
			u := h.newUE(loopback)

			h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri()})

			invite := u.invite(tt.target)
			u.send(invite)

			if lir := h.hss.nextLIR(t); lir.PublicIdentity != tt.want {
				t.Fatalf("LIR identity = %s, want %s", lir.PublicIdentity, tt.want)
			}

			req, f := s.recv()
			s.respond(req, f, 486)
			u.ack(invite, u.wantFinal(486))
		})
	}
}

func TestInviteToSCSCFOnAnotherAddress(t *testing.T) {
	h := newHarness(t, harnessOptions{scscfAddr: netip.MustParseAddr("127.0.0.3")})
	s := h.scscf
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri()})

	invite := u.invite(callee)
	u.send(invite)
	h.hss.nextLIR(t)

	req, f := s.recv()
	if f.Remote != h.icscf {
		t.Fatalf("INVITE from %s, want it from the I-CSCF's %s", f.Remote, h.icscf)
	}

	s.respond(req, f, 486)
	u.ack(invite, u.wantFinal(486))
}

func TestInviteToGRUU(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscf
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri()})

	target := "sip:+15551230002@" + homeDomain + ";user=phone;gr=urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6"
	invite := u.invite(target)
	u.send(invite)

	if lir := h.hss.nextLIR(t); lir.PublicIdentity != "tel:+15551230002" {
		t.Fatalf("LIR identity = %s, want tel:+15551230002", lir.PublicIdentity)
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
			h.scscf.sock.RecvNone(quiet)
		})
	}
}

func TestInviteCancelledDuringLIR(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	gate := make(chan struct{})
	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: h.scscf.uri(), gate: gate})

	invite := u.invite(callee)
	u.send(invite)
	h.hss.nextLIR(t)

	u.cancel(invite)

	if got := u.finals(invite); got["CANCEL"] != 200 || got["INVITE"] != 487 {
		t.Fatalf("responses = %v, want 200 to CANCEL and 487 to INVITE", got)
	}

	close(gate)
	h.scscf.sock.RecvNone(quiet)
}

func TestInviteCancelledBeforeAnAnswer(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(tgpp.ResultUnregisteredService)})

	invite := u.invite("sip:alice@" + homeDomain)
	u.send(invite)
	h.hss.nextLIR(t)
	h.scscf.recv()

	u.cancel(invite)

	if got := u.finals(invite); got["CANCEL"] != 200 || got["INVITE"] != 487 {
		t.Fatalf("responses = %v, want 200 to CANCEL and 487 to INVITE", got)
	}
}

// TS 24.229 §5.3.2.1: the request goes to the server the HSS names, another S-CSCF than ours here.
func TestInviteToAnotherAssignedSCSCF(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	other := h.newAS(loopback)
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: other.uri()})

	invite := u.invite(callee)
	u.send(invite)
	h.hss.nextLIR(t)

	req, f := other.recv()
	if got, want := routes(t, req), []string{other.uri() + ";lr"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes = %v, want %v", got, want)
	}

	other.respond(req, f, 486)
	u.ack(invite, u.wantFinal(486))
	h.scscf.sock.RecvNone(quiet)
}

// TS 24.229 §5.3.2.1, TS 29.228 §6.1.4.1 step 2: the AS hosting a PSI that the HSS names, whether or not it sets the
// PSI direct routing indication, which is optional (TS 29.229 §6.3.59), by address or domain name (RFC 3263).
func TestPSIDirectRouting(t *testing.T) {
	for _, tt := range []struct {
		name           string
		direct, byName bool
	}{
		{"with the indication", true, false},
		{"without the indication", false, false},
		{"by domain name", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{hosts: map[string][]netip.Addr{"as." + homeDomain: {loopback}}})
			as := h.newAS(loopback)
			u := h.newUE(untrusted)

			name := as.uri()
			if tt.byName {
				name = "sip:as." + homeDomain + ":" + strconv.Itoa(int(as.sock.Addr().Port()))
			}

			h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: name, direct: tt.direct})

			msg := u.invite("sip:group7@"+homeDomain, func(r *sip.Request) {
				r.Method = "MESSAGE"
				r.Header.Set("CSeq", "1 MESSAGE")
			})
			u.send(msg)

			if lir := h.hss.nextLIR(t); lir.PublicIdentity != "sip:group7@"+homeDomain || lir.Originating {
				t.Fatalf("LIR = %+v, want the terminating query of the PSI", lir)
			}

			req, f := as.recv()
			if got, want := routes(t, req), []string{name + ";lr"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("Routes = %v, want %v", got, want)
			}

			as.respond(req, f, 200)
			u.wantFinal(200)
			h.scscf.sock.RecvNone(quiet)
		})
	}
}

// TS 24.229 §5.3.2.1: with the PSI direct routing indication, the name of our S-CSCF stays a route to it.
func TestPSIDirectRoutingToOurSCSCF(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscf
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri(), direct: true})

	invite := u.invite("sip:group7@" + homeDomain)
	u.send(invite)
	h.hss.nextLIR(t)

	req, f := s.recv()
	if got, want := routes(t, req), []string{s.uri() + ";lr"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes = %v, want %v", got, want)
	}

	s.respond(req, f, 486)
	u.ack(invite, u.wantFinal(486))
}

// TS 24.229 §5.3.2.1: a server the HSS names that the I-CSCF cannot reach makes a 480: a domain name without an
// address, another address family than the request's, an address outside the trust domain, or no response.
func TestPSIDirectRoutingFailures(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	silent := h.newAS(loopback)

	for _, tt := range []struct {
		name, server string
	}{
		{"unknown domain name", "sip:nowhere." + homeDomain},
		{"another address family", "sip:[::1]:5060"},
		{"outside the trust domain", "sip:" + untrusted.String() + ":5060"},
		{"silent", silent.uri()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u := h.newUE(loopback)

			h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: tt.server, direct: true})

			msg := u.invite("sip:group7@"+homeDomain, func(r *sip.Request) {
				r.Method = "MESSAGE"
				r.Header.Set("CSeq", "1 MESSAGE")
			})
			u.send(msg)
			h.hss.nextLIR(t)

			u.wantFinal(480)
		})
	}

	h.scscf.sock.RecvNone(quiet)
}

func TestInviteFromOutsideTheTrustDomain(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscf
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

// TS 24.229 §5.3.2.1A, TS 23.228 §5.4.12.5 c: an AS of the trust domain sends a request on behalf of the user in
// P-Served-User, or else P-Asserted-Identity, to the I-CSCF: an originating location query, and the S-CSCF the HSS
// names gets the request with "orig"; the AS hosting a PSI, without.
func TestOrigOnTheICSCF(t *testing.T) {
	const psi = "sip:mcptt-tpf@" + homeDomain

	for _, tt := range []struct {
		name, servedUser, identity string
		as                         bool
	}{
		{"P-Served-User", "<" + psi + ">;sescase=orig;regstate=unreg", psi, false},
		{"P-Asserted-Identity", "", testIMPU, false},
		{"to the AS hosting the PSI", "<" + psi + ">", psi, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			u := h.newUE(loopback)

			to, want := h.scscf, h.scscf.uri()+";lr;orig"
			if tt.as {
				to = h.newAS(loopback)
				want = to.uri() + ";lr"
			}

			h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: to.uri(), direct: tt.as})

			invite := u.invite(callee, func(r *sip.Request) {
				r.Header.Add("Route", "<"+h.icscfURI()+";lr;orig>")
				r.Header.Add("P-Asserted-Identity", "<"+testIMPU+">")

				if tt.servedUser != "" {
					r.Header.Add("P-Served-User", tt.servedUser)
				}
			})
			u.send(invite)

			if lir := h.hss.nextLIR(t); lir.PublicIdentity != tt.identity || !lir.Originating {
				t.Fatalf("LIR = %+v, want the originating query of %s", lir, tt.identity)
			}

			req, f := to.recv()
			if got := routes(t, req); !reflect.DeepEqual(got, []string{want}) {
				t.Fatalf("Routes = %v, want %v", got, want)
			}

			if req.URI.String() != callee || req.Header.Get("P-Asserted-Identity") != "<"+testIMPU+">" {
				t.Fatalf("got:\n%s\nwant the request of the AS", req)
			}

			to.respond(req, f, 486)
			u.ack(invite, u.wantFinal(486))
		})
	}
}

// TS 24.229 §5.3.2.1A: an originating request on behalf of no identity, or of one the HSS does not know.
func TestOrigOnTheICSCFRefused(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	invite := u.invite(callee, func(r *sip.Request) { r.Header.Add("Route", "<"+h.icscfURI()+";lr;orig>") })
	u.send(invite)
	u.ack(invite, u.wantFinal(403))
	h.hss.noCx(t)

	h.hss.answerLIR(&hssAnswer{result: success(tgpp.ResultErrorUserUnknown)})

	invite = u.invite(callee, func(r *sip.Request) {
		r.Header.Add("Route", "<"+h.icscfURI()+";lr;orig>")
		r.Header.Add("P-Asserted-Identity", "<sip:nobody@"+homeDomain+">")
	})
	u.send(invite)
	h.hss.nextLIR(t)
	u.ack(invite, u.wantFinal(404))

	h.scscf.sock.RecvNone(quiet)
}

// TS 24.229 §5.3.2.1: a request from outside the trust domain is terminating: the Route it names past the I-CSCF
// goes, and the location query decides.
func TestInviteFromOutsideTheTrustDomainWithRoute(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscf
	u := h.newUE(untrusted)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri()})

	invite := u.invite(callee, func(r *sip.Request) {
		r.Header.Add("Route", "<"+h.icscfURI()+";lr>, <"+s.uri()+";lr>, <sip:odi-xyz@"+s.sock.Addr().String()+";lr>")
	})
	u.send(invite)
	h.hss.nextLIR(t)

	req, f := s.recv()
	if got, want := routes(t, req), []string{s.uri() + ";lr"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes = %v, want %v", got, want)
	}

	s.respond(req, f, 486)
	u.ack(invite, u.wantFinal(486))
}

func TestOrigBelowTheTopRoute(t *testing.T) {
	for _, method := range []string{"INVITE", "BYE"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			s := h.scscf
			u := h.newUE(untrusted)

			req := u.invite(callee, func(r *sip.Request) {
				r.Method = method
				r.Header.Set("CSeq", "1 "+method)
				r.Header.Add("Route", "<"+h.icscfURI()+";lr>, <"+s.uri()+";lr;orig>")

				if method == "BYE" {
					r.Header.Set("To", "<"+callee+">;tag=abc")
				}
			})
			u.send(req)

			res := u.wantFinal(403)
			if method == "INVITE" {
				u.ack(req, res)
			}

			s.sock.RecvNone(quiet)
			h.hss.noCx(t)
		})
	}
}

func TestOrigOnAnotherHop(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscf
	u := h.newUE(loopback)

	invite := u.invite(callee, func(r *sip.Request) {
		r.Header.Add("Route", "<"+s.uri()+";lr;orig>")
		r.Header.Add("P-Asserted-Identity", "<sip:bob@"+homeDomain+">")
	})
	u.send(invite)

	req, f := s.recv()
	if got, want := routes(t, req), []string{s.uri() + ";lr;orig"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes = %v, want %v", got, want)
	}

	s.respond(req, f, 486)
	u.ack(invite, u.wantFinal(486))
	h.hss.noCx(t)
}

func TestRequestWithRouteSkipsLIR(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscf
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
	s := h.scscf
	u := h.newUE(loopback)

	bye := u.invite(callee, func(r *sip.Request) {
		r.Method = "BYE"
		r.Header.Set("CSeq", "2 BYE")
		r.Header.Set("To", "<"+callee+">;tag=abc")
		r.Header.Add("Route", "<"+h.icscfURI()+";lr>, <"+s.uri()+";lr>")
		r.Header.Add("P-Charging-Vector", "icid-value=1234")
	})
	u.send(bye)

	req, f := s.recv()
	if req.Header.Get("P-Charging-Vector") != "icid-value=1234" {
		t.Fatal("P-Charging-Vector of the trust domain dropped")
	}

	s.respond(req, f, 200)
	u.wantFinal(200)
	h.hss.noCx(t)
}

// The dialogs of the S-CSCF do not go through the I-CSCF: one from outside the trust domain would reach the S-CSCF
// as if from the trust domain.
func TestSubsequentRequestFromOutsideTheTrustDomain(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscf
	u := h.newUE(untrusted)

	bye := u.invite(callee, func(r *sip.Request) {
		r.Method = "BYE"
		r.Header.Set("CSeq", "2 BYE")
		r.Header.Set("To", "<"+callee+">;tag=abc")
		r.Header.Add("Route", "<"+h.icscfURI()+";lr>, <"+s.uri()+";lr>")
	})
	u.send(bye)
	u.wantFinal(403)

	s.sock.RecvNone(quiet)
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

func TestInviteSCSCFTimeout(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: h.scscf.uri()})

	invite := u.invite(callee)
	u.send(invite)
	h.hss.nextLIR(t)
	h.scscf.recv()

	u.ack(invite, u.wantFinal(408))
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

func message(u *ue, target string) *sip.Request {
	return u.invite(target, func(r *sip.Request) {
		r.Method = "MESSAGE"
		r.Header.Set("CSeq", "1 MESSAGE")
	})
}

func TestMessageSCSCFTimeout(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	s := h.scscf
	u := h.newUE(loopback)

	h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: s.uri()})

	sent := time.Now()

	u.send(message(u, callee))
	h.hss.nextLIR(t)

	req, f := s.recv()
	s.respond(req, f, 100)

	u.wantFinal(480)

	if elapsed := time.Since(sent); elapsed >= 64*testT1 {
		t.Fatalf("480 after %s, want it before the transaction ends at %s", elapsed, 64*testT1)
	}
}

func TestSessionSCSCFRefusesConnections(t *testing.T) {
	for _, method := range []string{"INVITE", "MESSAGE"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t, harnessOptions{down: true})
			u := h.newUE(loopback)

			h.hss.answerLIR(&hssAnswer{result: success(diameter.ResultSuccess), name: h.scscf.uri()})

			req := u.invite(callee)
			if method == "MESSAGE" {
				req = message(u, callee)
			}

			u.send(req)
			h.hss.nextLIR(t)

			res := u.wantFinal(480)
			if method == "INVITE" {
				u.ack(req, res)
			}
		})
	}
}
