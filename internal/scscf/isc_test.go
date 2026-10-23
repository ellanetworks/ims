package scscf

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

func iscTrigger(as *siptest.Socket, handling cx.DefaultHandling, spts ...cx.ServicePointTrigger) cx.InitialFilterCriteria {
	tp := &cx.TriggerPoint{ConjunctiveNormalForm: true}

	for i, s := range spts {
		s.Groups = []int32{int32(i)}
		tp.ServicePointTriggers = append(tp.ServicePointTriggers, s)
	}

	return cx.InitialFilterCriteria{
		Trigger:           tp,
		ApplicationServer: cx.ApplicationServer{ServerName: "sip:" + as.Addr().String(), DefaultHandling: &handling},
	}
}

// newISCHarness registers the UE with the iFCs in the profile of its MSISDN.
func newISCHarness(t *testing.T, ifcs func(as *siptest.Socket) []cx.InitialFilterCriteria) (*sessionHarness, *siptest.Socket) {
	t.Helper()

	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	sh := newSessionHarnessWith(t, func(h *harness) {
		h.hss.set(func(h *fakeHSS) { h.subscriptions[0].ServiceProfiles[0].InitialFilterCriteria = ifcs(as) })
	})

	return sh, as
}

// proxied is a request as an AS acting as a SIP proxy sends it on: without its own Route, through its Via,
// record-routed (TS 24.229 §5.7.4).
func proxied(req *sip.Request, as *siptest.Socket, rr bool) *sip.Request {
	out := req.Clone()
	out.Header.PopFirst("Route")
	out.Header.Prepend("Via", sip.NewVia(sip.UDP, as.Addr()).String())

	if rr {
		out.Header.Prepend("Record-Route", "<sip:"+as.Addr().String()+";lr>")
	}

	return out
}

// proxiedResponse is a response as the AS acting as a SIP proxy relays it, to the next Via.
func proxiedResponse(t *testing.T, res *sip.Response) (*sip.Response, netip.AddrPort) {
	t.Helper()

	out := res.Clone()
	out.Header.PopFirst("Via")

	via, err := out.Header.TopVia()
	if err != nil {
		t.Fatal(err)
	}

	return out, netip.AddrPortFrom(netip.MustParseAddr(via.Host), via.Port)
}

// responseOn is the next response a socket gets, past the retransmitted requests.
func responseOn(t *testing.T, s *siptest.Socket) *sip.Response {
	t.Helper()

	for {
		if res, ok := s.Recv().Msg.(*sip.Response); ok && res.StatusCode != 100 {
			return res
		}
	}
}

func methodSPT(m string) cx.ServicePointTrigger { return cx.ServicePointTrigger{Method: &m} }

// TS 24.229 §5.4.3.2 steps 3, 5 and 13, §5.4.3.4, §5.7.4: the INVITE through an AS acting as a SIP proxy, which
// sends it back with the original dialog identifier; the S-CSCF record-routes both legs, and the INFO, REFER and
// UPDATE of the dialog go through the AS, the target refresh with the registration token (TS 24.229 §5.4.3.2).
func TestISCProxyAS(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		return []cx.InitialFilterCriteria{iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("INVITE"))}
	})

	req := sh.originating("INVITE", remoteTel, "<"+testMSISDN+">")
	sh.orig.Send(sip.UDP, sh.scscf, req)

	got, _ := as.RecvRequest()

	routes := got.Header.Values("Route")
	if len(routes) != 2 || !strings.Contains(routes[0], as.Addr().String()) || !strings.Contains(routes[1], "sip:odi-") {
		t.Fatalf("AS got Route %v, want its own then the S-CSCF's with an original dialog identifier", routes)
	}

	if v := got.Header.Get("P-Served-User"); v != "<"+testMSISDN+">;sescase=orig;regstate=reg" {
		t.Fatalf("AS got P-Served-User %q, want the caller", v)
	}

	as.Send(sip.UDP, sh.scscf, proxied(got, as, true))

	invite, fi := sh.icscf.RecvRequest()

	// The S-CSCF's entries before the AS face either way, the one towards the AS with the registration token.
	rrs := invite.Header.Values("Record-Route")
	if len(rrs) != 4 || !strings.Contains(rrs[1], as.Addr().String()) || !strings.Contains(rrs[2], tokenParam+"=") ||
		strings.Contains(rrs[3], tokenParam) {
		t.Fatalf("I-CSCF got Record-Route %v, want the S-CSCF, the AS, the S-CSCF twice", rrs)
	}

	if invite.Header.Has("P-Served-User") || invite.Header.Has("Feature-Caps") {
		t.Fatalf("I-CSCF got:\n%s\nwant no P-Served-User nor registration token", invite)
	}

	reply(t, sh.icscf, invite, fi, 200, "callee")

	res, _ := as.RecvResponse()
	for res.StatusCode == 100 {
		res, _ = as.RecvResponse()
	}

	relayed, to := proxiedResponse(t, res)
	as.Send(sip.UDP, to, relayed)

	ok := final(t, sh.orig)
	if ok.StatusCode != 200 {
		t.Fatalf("caller got %q, want 200", ok.StartLine())
	}

	routeSet, err := ok.Header.RecordRoutes()
	if err != nil {
		t.Fatal(err)
	}

	slices.Reverse(routeSet)

	for i, method := range []string{"INFO", "REFER", "UPDATE"} {
		r := siptest.NewRequest(method, "sip:callee@"+sh.icscf.Addr().String(), sip.UDP, sh.orig.Addr())
		r.Header.Set("Call-ID", req.Header.CallID())
		r.Header.Set("From", req.Header.Get("From"))
		r.Header.Set("To", ok.Header.Get("To"))
		r.Header.Set("CSeq", strings.Repeat("2", i+1)+" "+method)

		for _, a := range routeSet {
			r.Header.Add("Route", a.String())
		}

		sh.orig.Send(sip.UDP, sh.scscf, r)

		at, _ := as.RecvRequest()
		if at.Method != method {
			t.Fatalf("AS got %s, want the %s of the dialog", at.Method, method)
		}

		if token := strings.Contains(at.Header.Get("Feature-Caps"), registrationToken); token != (method == "UPDATE") {
			t.Fatalf("AS got:\n%s\nwant the registration token in a target refresh only", at)
		}

		as.Send(sip.UDP, sh.scscf, proxied(at, as, false))

		if got, _ := sh.icscf.RecvRequest(); got.Method != method || got.URI.String() != "sip:callee@"+sh.icscf.Addr().String() ||
			got.Header.Has("Feature-Caps") {
			t.Fatalf("callee got:\n%s\nwant the %s without the registration token", got, method)
		}
	}
}

// TS 24.229 §5.4.3.2: once the request came back from the AS, the S-CSCF waits for the final response however long it
// takes, and the default handling does not apply.
func TestISCReturnedRequestWaits(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		return []cx.InitialFilterCriteria{iscTrigger(as, cx.DefaultHandlingSessionTerminated, methodSPT("MESSAGE"))}
	})

	msg := sh.originating("MESSAGE", remoteTel, "<"+testMSISDN+">")
	sh.orig.Send(sip.UDP, sh.scscf, msg)

	got, _ := as.RecvRequest()
	as.Send(sip.UDP, sh.scscf, proxied(got, as, false))

	back, f := sh.icscf.RecvRequest()

	sh.sipClock.Advance(2 * DefaultASTimeout)

	reply(t, sh.icscf, back, f, 200, "callee")

	res := responseOn(t, as)
	relayed, to := proxiedResponse(t, res)
	as.Send(sip.UDP, to, relayed)

	if res := final(t, sh.orig); res.StatusCode != 200 {
		t.Fatalf("caller got %q, want the 200", res.StartLine())
	}
}

// TS 24.229 §5.4.3.2: SESSION_TERMINATED ends a MESSAGE the AS does not answer, with a 504 rather than a 408
// (RFC 4320 §4.2).
func TestISCTimeoutTerminated(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		return []cx.InitialFilterCriteria{iscTrigger(as, cx.DefaultHandlingSessionTerminated, methodSPT("MESSAGE"))}
	})

	sh.orig.Send(sip.UDP, sh.scscf, sh.originating("MESSAGE", remoteTel, "<"+testMSISDN+">"))
	as.RecvRequest()

	// The timeout starts once the request is out, which the AS may see first.
	sh.sipClock.Advance(DefaultASTimeout)
	sh.sipClock.Advance(DefaultASTimeout)

	if res := final(t, sh.orig); res.StatusCode != 504 {
		t.Fatalf("caller got %q, want 504", res.StartLine())
	}

	sh.icscf.RecvNone(10 * time.Millisecond)
}

// TS 24.229 §5.4.3.3 step 3 a: a terminating request the AS retargets leaves the iFCs of the served user for the
// I-CSCF.
func TestISCRetargeted(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		term := cx.SessionCaseTerminatingRegistered

		return []cx.InitialFilterCriteria{iscTrigger(as, cx.DefaultHandlingSessionContinued,
			methodSPT("INVITE"), cx.ServicePointTrigger{SessionCase: &term})}
	})

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("INVITE", testTel))

	got, _ := as.RecvRequest()
	if v := got.Header.Get("P-Served-User"); v != "<"+testTel+">;sescase=term;regstate=reg" {
		t.Fatalf("AS got P-Served-User %q, want the callee", v)
	}

	diverted := proxied(got, as, false)
	diverted.URI = sip.URI{Scheme: "tel", User: "+15559990001"}
	as.Send(sip.UDP, sh.scscf, diverted)

	if res, _ := sh.icscf.RecvResponse(); res.StatusCode != 100 {
		t.Fatalf("I-CSCF got %q, want 100", res.StartLine())
	}

	out, _ := sh.icscf.RecvRequest()
	if out.Method != "INVITE" || out.URI.String() != "tel:+15559990001" || !strings.Contains(out.Header.Get("Route"), sh.icscf.Addr().String()) {
		t.Fatalf("I-CSCF got:\n%s\nwant the retargeted INVITE", out)
	}

	sh.term.RecvNone(10 * time.Millisecond)
}

// TS 24.229 §5.4.3.2 step 5 c to e, §5.4.3.3 step 4 d and f: towards an AS, originating and terminating, the
// P-Charging-Vector keeps its icid-value and access-network-charging-info, the AS being in the trust domain, and has
// a type 3 orig-ioi of the S-CSCF's network in place of the one received, without term-ioi nor transit-ioi.
func TestISCChargingVector(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		orig, term := cx.SessionCaseOriginatingRegistered, cx.SessionCaseTerminatingRegistered

		return []cx.InitialFilterCriteria{
			iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("MESSAGE"), cx.ServicePointTrigger{SessionCase: &orig}),
			iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("MESSAGE"), cx.ServicePointTrigger{SessionCase: &term}),
		}
	})

	const received = "icid-value=AB12;orig-ioi=visited.example.net;term-ioi=other.example.net;transit-ioi=transit.example.net;" +
		"access-network-charging-info=x"

	want := "icid-value=AB12;orig-ioi=" + homeDomain + ";access-network-charging-info=x"

	for name, send := range map[string]func() *sip.Request{
		"originating": func() *sip.Request {
			req := sh.originating("MESSAGE", remoteTel, "<"+testMSISDN+">")
			req.Header.Add("P-Charging-Vector", received)
			sh.orig.Send(sip.UDP, sh.scscf, req)

			return req
		},
		"terminating": func() *sip.Request {
			req := sh.terminating("MESSAGE", testTel)
			req.Header.Add("P-Charging-Vector", received)
			sh.icscf.Send(sip.UDP, sh.scscf, req)

			return req
		},
		"without icid": func() *sip.Request {
			req := sh.originating("MESSAGE", remoteTel, "<"+testMSISDN+">")
			req.Header.Add("P-Charging-Vector", "orig-ioi=visited.example.net")
			sh.orig.Send(sip.UDP, sh.scscf, req)

			return req
		},
	} {
		t.Run(name, func(t *testing.T) {
			send()

			got, f := as.RecvRequest()

			v := got.Header.Values("P-Charging-Vector")
			if name == "without icid" {
				if len(v) != 0 {
					t.Fatalf("AS got P-Charging-Vector %q, want none without an icid-value", v)
				}
			} else if !slices.Equal(v, []string{want}) {
				t.Fatalf("AS got P-Charging-Vector %q, want %q", v, want)
			}

			as.Send(sip.UDP, f.Remote, sip.NewResponse(got, 486, ""))
		})
	}
}

// TS 24.229 §5.4.3.2 step 5: the iFCs apply to every initial request and standalone transaction; the S-CSCF
// record-routes a SUBSCRIBE to the AS.
func TestISCMethods(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		spt := func(m string, g int32) cx.ServicePointTrigger {
			return cx.ServicePointTrigger{Method: &m, Groups: []int32{g}}
		}

		handling := cx.DefaultHandlingSessionContinued

		return []cx.InitialFilterCriteria{{
			Trigger: &cx.TriggerPoint{ServicePointTriggers: []cx.ServicePointTrigger{
				spt("PUBLISH", 0), spt("SUBSCRIBE", 1), spt("MESSAGE", 2), spt("REFER", 3),
			}},
			ApplicationServer: cx.ApplicationServer{ServerName: "sip:" + as.Addr().String(), DefaultHandling: &handling},
		}}
	})

	for _, tt := range []struct {
		method string
		rr     bool
	}{
		{"PUBLISH", false},
		{"SUBSCRIBE", true},
		{"MESSAGE", false},
		{"REFER", true},
	} {
		t.Run(tt.method, func(t *testing.T) {
			req := sh.originating(tt.method, "sip:mcptt-cf@"+homeDomain, "<"+testMSISDN+">")
			req.Header.Add("Event", "poc-settings")
			sh.orig.Send(sip.UDP, sh.scscf, req)

			got, f := as.RecvRequest()
			if got.Method != tt.method || got.Header.Has("Record-Route") != tt.rr {
				t.Fatalf("AS got:\n%s\nwant the %s, record-routed: %v", got, tt.method, tt.rr)
			}

			reply(t, as, got, f, 200, "as")

			if res := final(t, sh.orig); res.StatusCode != 200 {
				t.Fatalf("caller got %q, want 200", res.StartLine())
			}
		})
	}

	sh.icscf.RecvNone(10 * time.Millisecond)
}

// TS 24.229 §5.4.3.3 steps 2b and 6: the 2xx of the served user carries the registration token to the AS, but not on
// to the I-CSCF, which another network may be behind.
func TestISCTerminatingToken(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		term := cx.SessionCaseTerminatingRegistered

		return []cx.InitialFilterCriteria{iscTrigger(as, cx.DefaultHandlingSessionContinued,
			methodSPT("MESSAGE"), cx.ServicePointTrigger{SessionCase: &term})}
	})

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", testTel))

	got, _ := as.RecvRequest()
	as.Send(sip.UDP, sh.scscf, proxied(got, as, false))

	toUE, f := sh.term.RecvRequest()
	reply(t, sh.term, toUE, f, 200, "ue")

	res := responseOn(t, as)
	if !strings.Contains(res.Header.Get("Feature-Caps"), registrationToken) {
		t.Fatalf("AS got:\n%s\nwant the registration token", res)
	}

	relayed, to := proxiedResponse(t, res)
	as.Send(sip.UDP, to, relayed)

	if ok := final(t, sh.icscf); ok.StatusCode != 200 || strings.Contains(ok.Header.Get("Feature-Caps"), registrationToken) {
		t.Fatalf("I-CSCF got:\n%s\nwant the 200 without the registration token", ok)
	}
}

// TS 24.229 §5.4.3.2 step 3, §5.4.3.4: an original dialog identifier that names nothing is refused.
func TestISCUnknownODI(t *testing.T) {
	sh, _ := newISCHarness(t, func(*siptest.Socket) []cx.InitialFilterCriteria { return nil })

	req := siptest.NewRequest("MESSAGE", remoteTel, sip.UDP, sh.icscf.Addr())
	req.Header.Prepend("Route", "<sip:"+odiPrefix+"nothing@"+sh.scscf.String()+";lr>")
	sh.icscf.Send(sip.UDP, sh.scscf, req)

	if res := final(t, sh.icscf); res.StatusCode != 403 {
		t.Fatalf("got %q, want 403", res.StartLine())
	}
}

// TS 24.229 §5.4.3.2: once the default handling went on without the AS, what the AS sends back late is refused,
// rather than run the rest of the iFCs a second time.
func TestISCLateReturn(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		return []cx.InitialFilterCriteria{iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("MESSAGE"))}
	})

	sh.orig.Send(sip.UDP, sh.scscf, sh.originating("MESSAGE", remoteTel, "<"+testMSISDN+">"))

	got, _ := as.RecvRequest()

	sh.sipClock.Advance(DefaultASTimeout)
	sh.sipClock.Advance(DefaultASTimeout)

	out, f := sh.icscf.RecvRequest()
	if out.Method != "MESSAGE" {
		t.Fatalf("I-CSCF got %s, want the MESSAGE past the AS", out.Method)
	}

	as.Send(sip.UDP, sh.scscf, proxied(got, as, false))

	if res := responseOn(t, as); res.StatusCode != 403 {
		t.Fatalf("AS got %q, want 403", res.StartLine())
	}

	reply(t, sh.icscf, out, f, 200, "callee")

	if res := final(t, sh.orig); res.StatusCode != 200 {
		t.Fatalf("caller got %q, want the 200", res.StartLine())
	}
}

// TS 24.229 §5.4.3.2 NOTE 8, §5.4.3.4 NOTE: an AS that answered the request sends another with its original dialog
// identifier, as a B2BUA does; it goes on with the iFCs after the AS's.
func TestISCODIReused(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		return []cx.InitialFilterCriteria{iscTrigger(as, cx.DefaultHandlingSessionTerminated, methodSPT("MESSAGE"))}
	})

	sh.orig.Send(sip.UDP, sh.scscf, sh.originating("MESSAGE", remoteTel, "<"+testMSISDN+">"))

	got, f := as.RecvRequest()
	reply(t, as, got, f, 202, "as")

	if res := final(t, sh.orig); res.StatusCode != 202 {
		t.Fatalf("caller got %q, want the AS's 202", res.StartLine())
	}

	later := got.Clone()
	later.Header.PopFirst("Route")
	later.Header.Del("Via")
	later.Header.Add("Via", sip.NewVia(sip.UDP, as.Addr()).String())
	later.Header.Set("Call-ID", sip.NewTag()+"@as")
	as.Send(sip.UDP, sh.scscf, later)

	out, fi := sh.icscf.RecvRequest()
	if out.Method != "MESSAGE" || out.Header.CallID() != later.Header.CallID() {
		t.Fatalf("I-CSCF got:\n%s\nwant the AS's MESSAGE", out)
	}

	reply(t, sh.icscf, out, fi, 200, "callee")

	if res := responseOn(t, as); res.StatusCode != 200 {
		t.Fatalf("AS got %q, want the 200", res.StartLine())
	}
}

// TS 24.229 §5.4.3.2 b: an AS that sent a 1xx, a 100 included, has not failed: its 5xx goes to the served user, as a
// 500 (RFC 3261 §16.7 step 6). Without one, the default handling applies. That the 100 stops the AS's timeout too is
// the proxy's (Options.AnyResponse).
func TestISCResponseAfterTrying(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		return []cx.InitialFilterCriteria{iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("MESSAGE"))}
	})

	sh.orig.Send(sip.UDP, sh.scscf, sh.originating("MESSAGE", remoteTel, "<"+testMSISDN+">"))

	got, f := as.RecvRequest()
	as.Send(sip.UDP, f.Remote, sip.NewResponse(got, 100, ""))
	reply(t, as, got, f, 503, "as")

	if res := final(t, sh.orig); res.StatusCode != 500 {
		t.Fatalf("caller got %q, want the AS's 503 as a 500", res.StartLine())
	}

	again := sh.originating("MESSAGE", remoteTel, "<"+testMSISDN+">")
	sh.orig.Send(sip.UDP, sh.scscf, again)

	got, f = as.RecvRequest()

	reply(t, as, got, f, 503, "as")

	out, fi := sh.icscf.RecvRequest()
	reply(t, sh.icscf, out, fi, 200, "callee")

	if res := final(t, sh.orig); res.StatusCode != 200 {
		t.Fatalf("caller got %q, want the 200 past the AS", res.StartLine())
	}
}

// TS 24.229 §5.4.3.2 step 1 a: the served user of a request from an AS is the identity of P-Served-User, which may
// differ from the one the AS asserts; its iFCs apply.
func TestISCServedUserOtherThanAsserted(t *testing.T) {
	sh, as := newISCHarness(t, func(as *siptest.Socket) []cx.InitialFilterCriteria {
		orig := cx.SessionCaseOriginatingRegistered

		return []cx.InitialFilterCriteria{iscTrigger(as, cx.DefaultHandlingSessionContinued,
			methodSPT("MESSAGE"), cx.ServicePointTrigger{SessionCase: &orig})}
	})

	psi := "sip:mcptt-tpf@" + homeDomain
	sender := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	req := siptest.NewRequest("MESSAGE", remoteTel, sip.UDP, sender.Addr())
	req.Header.Prepend("Route", "<sip:"+sh.scscf.String()+";lr;orig>")
	req.Header.Add("P-Served-User", "<"+testMSISDN+">;sescase=orig;regstate=reg")
	req.Header.Add("P-Asserted-Identity", "<"+psi+">")
	sender.Send(sip.UDP, sh.scscf, req)

	got, f := as.RecvRequest()
	if v := got.Header.Get("P-Served-User"); v != "<"+testMSISDN+">;sescase=orig;regstate=reg" {
		t.Fatalf("AS got P-Served-User %q, want the user of P-Served-User", v)
	}

	if ids := asserted(t, got.Header); len(ids) != 1 || ids[0] != "<"+psi+">" {
		t.Fatalf("AS got P-Asserted-Identity %v, want the AS's", ids)
	}

	reply(t, as, got, f, 200, "as")

	if res := final(t, sender); res.StatusCode != 200 {
		t.Fatalf("sender got %q, want 200", res.StartLine())
	}
}

// TS 24.229 §5.4.1.7 h: the third-party REGISTERs of the registration and the reregistration have the icid-value of
// the REGISTER, and a type 3 orig-ioi of the S-CSCF's network in place of the P-CSCF's, without term-ioi; the
// S-CSCF keeps the term-ioi of the AS's response. That of the deregistration has none.
func TestThirdPartyRegisterCharging(t *testing.T) {
	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	serverName := "sip:" + as.Addr().String()

	h := newHarness(t)
	h.hss.set(func(h *fakeHSS) {
		register := "REGISTER"
		handling := cx.DefaultHandlingSessionContinued

		h.subscriptions[0].ServiceProfiles[0].InitialFilterCriteria = []cx.InitialFilterCriteria{{
			Trigger:           &cx.TriggerPoint{ServicePointTriggers: []cx.ServicePointTrigger{{Groups: []int32{0}, Method: &register}}},
			ApplicationServer: cx.ApplicationServer{ServerName: serverName, DefaultHandling: &handling},
		}}
	})

	u := h.newUE()

	thirdParty := func(want, termIOI string) {
		t.Helper()

		got, f := as.RecvRequest()
		if got.Method != "REGISTER" {
			t.Fatalf("AS got %s, want a third-party REGISTER", got.Method)
		}

		if v := got.Header.Values("P-Charging-Vector"); !slices.Equal(v, []string{want}) && (want != "" || len(v) != 0) {
			t.Fatalf("AS got P-Charging-Vector %q, want %q", v, want)
		}

		res := sip.NewResponse(got, 200, "")
		res.Header.Add("P-Charging-Vector", "icid-value=AB12;orig-ioi="+homeDomain+";term-ioi="+termIOI)
		as.Send(sip.UDP, f.Remote, res)
	}

	stored := func(want string) {
		t.Helper()

		for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
			h.reg.mu.Lock()

			var got string

			for _, peers := range h.reg.peers {
				if p := peers[serverName]; p != nil {
					got = p.termIOI
				}
			}

			h.reg.mu.Unlock()

			if got == want {
				return
			}

			if time.Now().After(deadline) {
				t.Fatalf("stored term-ioi %q, want %q", got, want)
			}
		}
	}

	const received = "icid-value=AB12;icid-generated-at=192.0.2.1;orig-ioi=visited.example.net;term-ioi=other.example.net"

	want := "icid-value=AB12;orig-ioi=" + homeDomain

	u.register(registerOptions{charging: received})
	thirdParty(want, "as.example.net")
	stored("as.example.net")

	u.register(registerOptions{charging: received})
	thirdParty(want, "as2.example.net")
	stored("as2.example.net")

	u.register(registerOptions{charging: received, expires: "0"})
	thirdParty("", "as3.example.net")
}

// RFC 3261 §10.2, TS 24.229 §5.4.1.7: the third-party REGISTERs to an AS for an identity follow the registration
// events, on one Call-ID with the CSeq one more each time, and none goes before the previous one has its final
// response. A deregistration is the last of them: the next registration starts anew.
func TestThirdPartyRegisterOrder(t *testing.T) {
	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	h := newHarness(t)
	h.hss.set(func(h *fakeHSS) {
		register := "REGISTER"
		handling := cx.DefaultHandlingSessionContinued

		h.subscriptions[0].ServiceProfiles[0].InitialFilterCriteria = []cx.InitialFilterCriteria{{
			Trigger:           &cx.TriggerPoint{ServicePointTriggers: []cx.ServicePointTrigger{{Groups: []int32{0}, Method: &register}}},
			ApplicationServer: cx.ApplicationServer{ServerName: "sip:" + as.Addr().String(), DefaultHandling: &handling},
		}}
	})

	u := h.newUE()

	var last *sip.Request

	// next is the next third-party REGISTER, over retransmissions of the last one, nil without one within d.
	next := func(d time.Duration) (*sip.Request, sip.Flow) {
		t.Helper()

		for {
			r, ok := as.RecvWithin(d)
			if !ok {
				return nil, sip.Flow{}
			}

			req, isReq := r.Msg.(*sip.Request)
			if !isReq || req.Method != "REGISTER" {
				t.Fatalf("AS got %q, want a third-party REGISTER", r.Msg.StartLine())
			}

			if last != nil && req.Header.Get("CSeq") == last.Header.Get("CSeq") && req.Header.CallID() == last.Header.CallID() {
				continue
			}

			return req, r.Flow
		}
	}

	want := func(expires string) (*sip.Request, sip.Flow) {
		t.Helper()

		req, f := next(siptest.Timeout)
		if req == nil {
			t.Fatalf("AS got no third-party REGISTER, want one with Expires %s", expires)
		}

		if got := req.Header.Get("Expires"); got != expires {
			t.Fatalf("third-party REGISTER with Expires %s, want %s", got, expires)
		}

		last = req

		return req, f
	}

	u.register(registerOptions{expires: "600"})

	first, f := want("600")

	u.register(registerOptions{expires: "700"})
	u.register(registerOptions{expires: "0"})

	if req, _ := next(100 * time.Millisecond); req != nil {
		t.Fatalf("AS got %q before its response to the previous third-party REGISTER", req.Header.Get("CSeq"))
	}

	as.Send(sip.UDP, f.Remote, sip.NewResponse(first, 200, ""))
	second, f := want("700")
	as.Send(sip.UDP, f.Remote, sip.NewResponse(second, 200, ""))
	third, f := want("0")
	as.Send(sip.UDP, f.Remote, sip.NewResponse(third, 200, ""))

	cseq := func(req *sip.Request) uint32 {
		c, err := req.Header.CSeq()
		if err != nil {
			t.Fatalf("CSeq: %v", err)
		}

		return c.Seq
	}

	for i, req := range []*sip.Request{second, third} {
		if req.Header.CallID() != first.Header.CallID() || cseq(req) != cseq(first)+uint32(i)+1 {
			t.Fatalf("third-party REGISTER %d: Call-ID %s CSeq %d, want %s and %d", i+2, req.Header.CallID(), cseq(req),
				first.Header.CallID(), cseq(first)+uint32(i)+1)
		}
	}

	u.callID = sip.NewTag() + "@127.0.0.1"
	u.register(registerOptions{expires: "600"})

	again, f := want("600")
	as.Send(sip.UDP, f.Remote, sip.NewResponse(again, 200, ""))

	if again.Header.CallID() == first.Header.CallID() || cseq(again) != 1 {
		t.Fatalf("third-party REGISTER after the deregistration: Call-ID %s CSeq %d, want a new Call-ID and 1",
			again.Header.CallID(), cseq(again))
	}
}

// TS 24.229 §5.4.1.1 item 1, §3.1: a UE that restarted registers its contact again, unprotected: an initial
// registration for the ASs, as the first one was, where a refresh is not.
func TestThirdPartyInitialAfterRestart(t *testing.T) {
	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))

	h := newHarness(t)
	h.hss.set(func(h *fakeHSS) {
		register := "REGISTER"
		handling := cx.DefaultHandlingSessionContinued

		h.subscriptions[0].ServiceProfiles[0].InitialFilterCriteria = []cx.InitialFilterCriteria{{
			Trigger: &cx.TriggerPoint{ServicePointTriggers: []cx.ServicePointTrigger{{
				Groups: []int32{0}, Method: &register, RegistrationTypes: []cx.RegistrationType{cx.RegistrationTypeInitial},
			}}},
			ApplicationServer: cx.ApplicationServer{ServerName: "sip:" + as.Addr().String(), DefaultHandling: &handling},
		}}
	})

	u := h.newUE()

	thirdParty := func() {
		t.Helper()

		got, f := as.RecvRequest()
		if got.Method != "REGISTER" {
			t.Fatalf("AS got %s, want a third-party REGISTER", got.Method)
		}

		as.Send(sip.UDP, f.Remote, sip.NewResponse(got, 200, ""))
	}

	nonce := u.challenged(registerOptions{})
	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES)}), 200)
	thirdParty()

	wantStatus(t, u.send(registerOptions{auth: u.protected(nonce, testVector.XRES)}), 200)
	as.RecvNone(50 * time.Millisecond)

	u.callID = sip.NewTag() + "@127.0.0.1"
	u.register(registerOptions{})
	thirdParty()
}

// TS 23.228 §5.12.1 steps 6 and 7, TS 24.229 §5.4.3.3 NOTE 27: the S-CSCF keeps the profile that SAR
// UNREGISTERED_USER downloads, for the canonical identity (TS 29.229 §6.3.2), for the identity's later requests, and
// clears its name at the HSS a registration period after (TS 29.228 §6.1.2.1).
func TestUnregisteredProfileKept(t *testing.T) {
	sh := newSessionHarness(t)
	sh.hss.wantSAR(t, cx.AssignmentRegistration)

	for range 2 {
		sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", "sip:second@"+strings.ToUpper(homeDomain)+";transport=udp"))

		if res := final(t, sh.icscf); res.StatusCode != 480 {
			t.Fatalf("got %q, want 480: no service for the identity unregistered", res.StartLine())
		}
	}

	sar := sh.hss.nextSAR(t)
	if sar.Type != cx.AssignmentUnregisteredUser || sar.PrivateIdentity != "" || !slices.Equal(sar.PublicIdentities, []string{secondIMPU}) {
		t.Fatalf("SAR = %+v, want UNREGISTERED_USER of the canonical identity", sar)
	}

	sh.hss.noCx(t)

	sh.clock.Advance(sh.cfg.MaxExpires)
	sh.clock.Advance(2 * sweepInterval)

	for {
		sar := sh.hss.nextSAR(t)
		if sar.PrivateIdentity != "" {
			continue
		}

		if sar.Type != cx.AssignmentTimeoutDeregistration || !slices.Equal(sar.PublicIdentities, []string{secondIMPU}) {
			t.Fatalf("SAR = %+v, want TIMEOUT_DEREGISTRATION of the identity", sar)
		}

		break
	}
}

// TS 24.229 §5.3.1.3: an HSS that cannot be reached for the profile of an identity makes a 480, as at the I-CSCF.
func TestTerminatingHSSDown(t *testing.T) {
	sh := newSessionHarness(t)
	sh.loop.SetDown(true)

	sh.icscf.Send(sip.UDP, sh.scscf, sh.terminating("MESSAGE", secondIMPU))

	if res := final(t, sh.icscf); res.StatusCode != 480 {
		t.Fatalf("got %q, want 480", res.StartLine())
	}
}

// TS 24.229 §5.4.3.2 step 18: the token goes, the other indicators stay, a ";" in a quoted value included.
func TestWithoutRegistrationToken(t *testing.T) {
	var h sip.Header

	h.Add("Feature-Caps", `*;+g.3gpp.registration-token="<7>";+g.3gpp.trf="<sip:trf.example.com;lr>"`)
	h.Add("Feature-Caps", `*;+g.3gpp.registration-token="<8>"`)

	withoutRegistrationToken(&h)

	if got := h.Values("Feature-Caps"); !slices.Equal(got, []string{`*;+g.3gpp.trf="<sip:trf.example.com;lr>"`}) {
		t.Fatalf("Feature-Caps = %q, want the other indicator only", got)
	}
}
