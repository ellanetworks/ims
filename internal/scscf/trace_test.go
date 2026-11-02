package scscf

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func newTracedHarness(t *testing.T) (*harness, *tracetest.SpanRecorder) {
	t.Helper()

	spans := tracetest.NewSpanRecorder()

	return newHarnessTraced(t, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))), spans
}

// withRegisterAS has the REGISTERs of the MSISDN's profile go to an AS, with the default handling.
func withRegisterAS(h *harness, as *siptest.Socket, handling cx.DefaultHandling) {
	h.hss.set(func(h *fakeHSS) {
		register := "REGISTER"

		h.subscriptions[0].ServiceProfiles[0].InitialFilterCriteria = []cx.InitialFilterCriteria{{
			Trigger:           &cx.TriggerPoint{ServicePointTriggers: []cx.ServicePointTrigger{{Groups: []int32{0}, Method: &register}}},
			ApplicationServer: cx.ApplicationServer{ServerName: "sip:" + as.Addr().String(), DefaultHandling: &handling},
		}}
	})
}

// thirdPartyRegister is the next third-party REGISTER the AS gets, past retransmissions of the one before, last.
func thirdPartyRegister(t *testing.T, as *siptest.Socket, last *sip.Request) (*sip.Request, sip.Flow) {
	t.Helper()

	for {
		r, ok := as.RecvWithin(siptest.Timeout)
		if !ok {
			t.Fatal("AS got no third-party REGISTER")
		}

		req, isReq := r.Msg.(*sip.Request)
		if !isReq || req.Method != "REGISTER" {
			t.Fatalf("AS got %q, want a third-party REGISTER", r.Msg.StartLine())
		}

		if last != nil && req.Header.Get("CSeq") == last.Header.Get("CSeq") {
			continue
		}

		return req, r.Flow
	}
}

func started(spans *tracetest.SpanRecorder, name string, kind trace.SpanKind) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, s := range spans.Started() {
		if s.Name() == name && s.SpanKind() == kind {
			out = append(out, s)
		}
	}

	return out
}

func linked(s sdktrace.ReadOnlySpan, to trace.SpanContext) bool {
	return slices.ContainsFunc(s.Links(), func(l sdktrace.Link) bool { return l.SpanContext.Equal(to) })
}

func spanRole(s sdktrace.ReadOnlySpan) string {
	for _, a := range s.Attributes() {
		if a.Key == tracing.RoleKey {
			return a.Value.AsString()
		}
	}

	return ""
}

// waitSpan waits for the nth span started with the name and kind, from 0.
func waitSpan(t *testing.T, spans *tracetest.SpanRecorder, name string, kind trace.SpanKind, n int) sdktrace.ReadOnlySpan {
	t.Helper()

	var got []sdktrace.ReadOnlySpan

	eventually(t, name+" span", func() bool {
		got = started(spans, name, kind)
		return len(got) > n
	})

	return got[n]
}

// B2, TS 24.229 §5.4.1.7: the third-party REGISTER and the reg event NOTIFY of a registration are in the trace of its
// REGISTER; a third-party REGISTER that waits for the one before has a trace of its own, linked to its REGISTER's.
func TestThirdPartyRegisterAndNotifyInTheTraceOfTheREGISTER(t *testing.T) {
	h, spans := newTracedHarness(t)

	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	withRegisterAS(h, as, cx.DefaultHandlingSessionContinued)

	u := h.newUE()
	u.register(registerOptions{expires: "600"})

	first, f := thirdPartyRegister(t, as, nil)
	as.Send(sip.UDP, f.Remote, sip.NewResponse(first, 200, ""))

	sub := h.ueSubscriber(u)
	wantStatus(t, sub.subscribe("600"), 200)
	sub.recvNotify()

	// The REGISTER of each registration is the second, protected: the first is challenged.
	u.register(registerOptions{expires: "600"})

	register := waitSpan(t, spans, "REGISTER", trace.SpanKindServer, 3)

	sub.recvNotify()

	second, f := thirdPartyRegister(t, as, first)

	for _, s := range []sdktrace.ReadOnlySpan{
		waitSpan(t, spans, "REGISTER", trace.SpanKindClient, 1), waitSpan(t, spans, "NOTIFY", trace.SpanKindClient, 1),
	} {
		if s.SpanContext().TraceID() != register.SpanContext().TraceID() {
			t.Fatalf("%s span in trace %s, want the REGISTER's %s", s.Name(), s.SpanContext().TraceID(),
				register.SpanContext().TraceID())
		}
	}

	// The AS holds its response: the third-party REGISTER of the next registration waits for it.
	u.register(registerOptions{expires: "600"})

	queued := waitSpan(t, spans, "REGISTER", trace.SpanKindServer, 5)

	as.Send(sip.UDP, f.Remote, sip.NewResponse(second, 200, ""))

	third, f := thirdPartyRegister(t, as, second)
	as.Send(sip.UDP, f.Remote, sip.NewResponse(third, 200, ""))

	detached := waitSpan(t, spans, "third-party registration", trace.SpanKindInternal, 0)
	if detached.Parent().IsValid() || !linked(detached, queued.SpanContext()) || spanRole(detached) != tracerRole {
		t.Fatalf("queued third-party registration: parent %v, links %v, role %q; want a root of the S-CSCF linked to %v",
			detached.Parent(), detached.Links(), spanRole(detached), queued.SpanContext())
	}

	if c := waitSpan(t, spans, "REGISTER", trace.SpanKindClient, 2); c.Parent().SpanID() != detached.SpanContext().SpanID() {
		t.Fatalf("queued third-party REGISTER has parent %v, want %v", c.Parent(), detached.SpanContext())
	}
}

// D5: the expiry of a registration is a trace of its own, linked to the REGISTER that last refreshed it, with the
// third-party deregistration in it.
func TestRegistrationExpiryLinksTheREGISTER(t *testing.T) {
	h, spans := newTracedHarness(t)

	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	withRegisterAS(h, as, cx.DefaultHandlingSessionContinued)

	u := h.newUE()
	u.register(registerOptions{expires: "600"})
	h.hss.nextSAR(t)

	register := waitSpan(t, spans, "REGISTER", trace.SpanKindServer, 1)

	req, f := thirdPartyRegister(t, as, nil)
	as.Send(sip.UDP, f.Remote, sip.NewResponse(req, 200, ""))

	h.clock.Advance(600 * time.Second)
	h.hss.wantSAR(t, cx.AssignmentTimeoutDeregistration)

	expiry := waitSpan(t, spans, "registration expiry", trace.SpanKindInternal, 0)
	if expiry.Parent().IsValid() || !linked(expiry, register.SpanContext()) || spanRole(expiry) != tracerRole {
		t.Fatalf("registration expiry: parent %v, links %v, role %q; want a root of the S-CSCF linked to %v",
			expiry.Parent(), expiry.Links(), spanRole(expiry), register.SpanContext())
	}

	if req, _ := thirdPartyRegister(t, as, req); req.Header.Get("Expires") != "0" {
		t.Fatalf("third-party REGISTER with Expires %s, want 0", req.Header.Get("Expires"))
	}

	if c := waitSpan(t, spans, "REGISTER", trace.SpanKindClient, 1); c.SpanContext().TraceID() != expiry.SpanContext().TraceID() {
		t.Fatalf("third-party deregistration in trace %s, want the expiry's %s", c.SpanContext().TraceID(),
			expiry.SpanContext().TraceID())
	}

	eventually(t, "the expiry span to end", func() bool {
		return slices.ContainsFunc(spans.Ended(), func(s sdktrace.ReadOnlySpan) bool { return s.Name() == "registration expiry" })
	})
}

// D5: the expiry of a challenge is a trace of its own, linked to the REGISTER challenged.
func TestChallengeExpiryLinksTheREGISTER(t *testing.T) {
	h, spans := newTracedHarness(t)
	u := h.newUE()

	u.challenged(registerOptions{})

	register := waitSpan(t, spans, "REGISTER", trace.SpanKindServer, 0)

	h.clock.Advance(regAwaitAuth)
	h.hss.wantSAR(t, cx.AssignmentAuthenticationTimeout)

	expiry := waitSpan(t, spans, "challenge expiry", trace.SpanKindInternal, 0)
	if expiry.Parent().IsValid() || !linked(expiry, register.SpanContext()) {
		t.Fatalf("challenge expiry: parent %v, links %v; want a root linked to %v", expiry.Parent(), expiry.Links(),
			register.SpanContext())
	}
}

// D2: what a REGISTER has the S-CSCF do after its response does not end with its transaction, which on a reliable or
// in-memory flow ends with the response: the third-party REGISTERs go on, and the deregistration that the AS's
// failure brings has its Cx request.
func TestWorkAfterTheResponseOutlivesTheTransaction(t *testing.T) {
	h := newHarness(t)
	h.pcscf.endWithResponse.Store(true)

	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	withRegisterAS(h, as, cx.DefaultHandlingSessionTerminated)

	u := h.newUE()
	u.register(registerOptions{expires: "600"})
	h.hss.wantSAR(t, cx.AssignmentRegistration)

	first, f := thirdPartyRegister(t, as, nil)
	as.Send(sip.UDP, f.Remote, sip.NewResponse(first, 200, ""))

	u.register(registerOptions{expires: "600"})
	h.hss.wantSAR(t, cx.AssignmentReRegistration)

	second, f := thirdPartyRegister(t, as, first)
	as.Send(sip.UDP, f.Remote, sip.NewResponse(second, 500, ""))

	h.hss.wantSAR(t, cx.AssignmentAdministrativeDeregistration)
}

// A request back from an AS whose flow carries no trace context, as one outside the trust domain, is a trace of its
// own: the request the S-CSCF sends on links to the one it sent the AS.
func TestRequestBackFromAnASLinksTheRequestSentToIt(t *testing.T) {
	h, spans := newTracedHarness(t)

	as := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	sh := newSessionHarnessOn(t, h, func(h *harness) {
		h.hss.set(func(h *fakeHSS) {
			h.subscriptions[0].ServiceProfiles[0].InitialFilterCriteria = []cx.InitialFilterCriteria{
				iscTrigger(as, cx.DefaultHandlingSessionContinued, methodSPT("INVITE")),
			}
		})
	})

	sh.orig.Send(sip.UDP, sh.scscf, sh.originating("INVITE", remoteTel, "<"+testMSISDN+">"))

	got, _ := as.RecvRequest()
	if got.Header.Has("traceparent") {
		t.Fatalf("AS got trace context %q on a flow that carries none", got.Header.Get("traceparent"))
	}

	sent := waitSpan(t, spans, "INVITE", trace.SpanKindServer, 0)

	as.Send(sip.UDP, sh.scscf, proxied(got, as, true))
	sh.icscf.RecvRequest()

	back := waitSpan(t, spans, "INVITE", trace.SpanKindServer, 1)
	if back.SpanContext().TraceID() == sent.SpanContext().TraceID() {
		t.Fatal("the request back from the AS continues the trace of the one sent to it, over a flow without trace context")
	}

	onward := waitSpan(t, spans, "INVITE", trace.SpanKindClient, 1)
	if onward.SpanContext().TraceID() != back.SpanContext().TraceID() || !linked(onward, sent.SpanContext()) {
		t.Fatalf("INVITE sent on: trace %s, links %v; want the trace %s of the request back, linked to %v",
			onward.SpanContext().TraceID(), onward.Links(), back.SpanContext().TraceID(), sent.SpanContext())
	}
}
