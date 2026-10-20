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
