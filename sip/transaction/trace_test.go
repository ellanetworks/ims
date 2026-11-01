package transaction_test

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

var (
	trustedPeer   = netip.MustParseAddrPort("127.0.0.1:6000")
	untrustedPeer = netip.MustParseAddrPort("127.0.0.2:6000")

	roleKey = attribute.Key("test.role")
)

// remoteParent is the trace context a peer sends.
var remoteParent = trace.NewSpanContext(trace.SpanContextConfig{
	TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
	SpanID:     trace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
	TraceFlags: trace.FlagsSampled,
	Remote:     true,
})

const remoteTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

type traceHarness struct {
	t         *testing.T
	clock     *siptest.Clock
	l         *transaction.Layer
	tu        *siptest.TU
	rec       *tracetest.SpanRecorder
	tracer    trace.Tracer
	trusted   *siptest.Peer
	untrusted *siptest.Peer
}

// newTraceHarness serves servedA and servedB, with a peer of the trust domain and one outside it, as a UE.
func newTraceHarness(t *testing.T) *traceHarness {
	t.Helper()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	clock := siptest.NewClock()

	l, tu := siptest.NewLayer(t, transaction.Config{
		Clock:          clock,
		TracerProvider: tp,
		TraceContext: func(f sip.Flow) bool {
			return f.Remote.Addr() == trustedPeer.Addr() || f.Remote == servedA || f.Remote == servedB
		},
		SpanAttributes: func(f sip.Flow) []attribute.KeyValue { return []attribute.KeyValue{roleKey.String(f.Local.String())} },
	})

	for _, a := range []netip.AddrPort{servedA, servedB} {
		if err := l.Serve(a); err != nil {
			t.Fatal(err)
		}
	}

	return &traceHarness{
		t: t, clock: clock, l: l, tu: tu, rec: rec, tracer: tp.Tracer("test"),
		trusted: siptest.NewPeer(t, l, trustedPeer), untrusted: siptest.NewPeer(t, l, untrustedPeer),
	}
}

// ended waits for n spans to end, and returns them.
func (h *traceHarness) ended(n int) []sdktrace.ReadOnlySpan {
	h.t.Helper()

	deadline := time.Now().Add(siptest.Timeout)

	for {
		spans := h.rec.Ended()
		if len(spans) >= n {
			if len(spans) > n {
				h.t.Fatalf("%d spans ended, want %d: %v", len(spans), n, names(spans))
			}

			return spans
		}

		if time.Now().After(deadline) {
			h.t.Fatalf("%d spans ended, want %d: %v", len(spans), n, names(spans))
		}

		time.Sleep(time.Millisecond)
	}
}

func names(spans []sdktrace.ReadOnlySpan) []string {
	var out []string
	for _, s := range spans {
		out = append(out, s.Name()+"/"+s.SpanKind().String())
	}

	return out
}

func attr(s sdktrace.ReadOnlySpan, k attribute.Key) (attribute.Value, bool) {
	for _, a := range s.Attributes() {
		if a.Key == k {
			return a.Value, true
		}
	}

	return attribute.Value{}, false
}

func wantAttr(t *testing.T, s sdktrace.ReadOnlySpan, k attribute.Key, want any) {
	t.Helper()

	v, ok := attr(s, k)
	if !ok {
		t.Fatalf("%s: no %s in %v", s.Name(), k, s.Attributes())
	}

	if got := v.AsInterface(); got != want {
		t.Fatalf("%s: %s = %v, want %v", s.Name(), k, got, want)
	}
}

func wantNoAttr(t *testing.T, s sdktrace.ReadOnlySpan, k attribute.Key) {
	t.Helper()

	if v, ok := attr(s, k); ok {
		t.Fatalf("%s: %s = %v, want none", s.Name(), k, v.AsInterface())
	}
}

func (h *traceHarness) respond(r siptest.ServerRequest, code int) {
	h.t.Helper()

	if err := r.Tx.Respond(sip.NewResponse(r.Req, code, "")); err != nil {
		h.t.Fatal(err)
	}
}

func tracedRequest(method string, from netip.AddrPort, to netip.AddrPort) *sip.Request {
	req := siptest.NewRequest(method, "sip:x@"+to.String(), sip.UDP, from)
	req.Header.Set("traceparent", remoteTraceparent)
	req.Header.Set("tracestate", "vendor=1")
	req.Header.Set("P-Charging-Vector", `icid-value="AyretyU0dm+6O2IrT5tAFrbHLso="`)

	return req
}

func TestServerSpanContinuesTraceOfTrustDomain(t *testing.T) {
	h := newTraceHarness(t)

	h.trusted.Send(sip.UDP, servedA, tracedRequest("INVITE", trustedPeer, servedA))

	got := h.tu.NextRequest()
	if sc := trace.SpanContextFromContext(got.Ctx); sc.TraceID() != remoteParent.TraceID() {
		t.Fatalf("handler trace %s, want %s", sc.TraceID(), remoteParent.TraceID())
	}

	h.respond(got, 180)
	h.respond(got, 486)

	s := h.ended(1)[0]
	if s.Name() != "INVITE" || s.SpanKind() != trace.SpanKindServer {
		t.Fatalf("span %s %s", s.Name(), s.SpanKind())
	}

	if s.Parent().SpanID() != remoteParent.SpanID() || !s.Parent().IsRemote() {
		t.Fatalf("parent %v, want the peer's span", s.Parent())
	}

	if got := s.SpanContext().TraceState().Get("vendor"); got != "1" {
		t.Fatalf("tracestate vendor=%q", got)
	}

	// A 4xx is the client's: not an error of the server.
	if s.Status().Code != codes.Unset {
		t.Fatalf("status %v", s.Status())
	}

	wantAttr(t, s, "sip.request.method", "INVITE")
	wantAttr(t, s, "sip.response.status_code", int64(486))
	wantAttr(t, s, "sip.call_id", got.Req.Header.Get("Call-ID"))
	wantAttr(t, s, "ella.ims.icid", "AyretyU0dm+6O2IrT5tAFrbHLso=")
	wantAttr(t, s, "network.protocol.name", "sip")
	wantAttr(t, s, "network.transport", "udp")
	wantAttr(t, s, "network.peer.address", trustedPeer.Addr().String())
	wantAttr(t, s, "network.peer.port", int64(trustedPeer.Port()))
	wantAttr(t, s, roleKey, servedA.String())

	if ev := s.Events(); len(ev) != 1 || ev[0].Name != "sip.provisional_response" {
		t.Fatalf("events %v", ev)
	}
}

func TestServerSpanOfUntrustedFlowStartsATrace(t *testing.T) {
	h := newTraceHarness(t)

	h.untrusted.Send(sip.UDP, servedA, tracedRequest("REGISTER", untrustedPeer, servedA))

	got := h.tu.NextRequest()
	h.respond(got, 500)

	s := h.ended(1)[0]
	if s.Parent().IsValid() || s.SpanContext().TraceID() == remoteParent.TraceID() {
		t.Fatalf("span continues the UE's trace: parent %v", s.Parent())
	}

	if s.Status().Code != codes.Error {
		t.Fatalf("status %v, want an error for a 5xx", s.Status())
	}

	wantAttr(t, s, "error.type", "500")
	// A UE's address is not recorded.
	wantNoAttr(t, s, "network.peer.address")
}

func TestClientSpanPropagatesWithinTrustDomain(t *testing.T) {
	h := newTraceHarness(t)

	ctx, parent := h.tracer.Start(context.Background(), "parent")

	rs := siptest.NewResponses(t)
	if _, err := h.l.Request(ctx, newFlowRequest("MESSAGE", sip.UDP, servedA, trustedPeer), rs); err != nil {
		t.Fatal(err)
	}

	req, _ := h.trusted.RecvRequest()
	h.trusted.Send(sip.UDP, servedA, sip.NewResponse(req, 503, ""))
	wantCode(t, rs.Next(), 503)
	parent.End()

	spans := h.ended(2)

	i := slices.IndexFunc(spans, func(s sdktrace.ReadOnlySpan) bool { return s.SpanKind() == trace.SpanKindClient })
	s := spans[i]

	if s.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatalf("client span parent %v", s.Parent())
	}

	want := "00-" + s.SpanContext().TraceID().String() + "-" + s.SpanContext().SpanID().String() + "-01"
	if got := req.Header.Get("traceparent"); got != want {
		t.Fatalf("traceparent %q, want %q", got, want)
	}

	if s.Status().Code != codes.Error {
		t.Fatalf("status %v", s.Status())
	}

	wantAttr(t, s, "error.type", "503")
	wantAttr(t, s, "sip.response.status_code", int64(503))
}

func TestClientSpanStripsTraceContextAtTrustBoundary(t *testing.T) {
	h := newTraceHarness(t)

	ctx, parent := h.tracer.Start(context.Background(), "parent")
	defer parent.End()

	// A request forwarded to a UE, which carries the trace context of the previous hop.
	out := tracedRequest("MESSAGE", servedA, untrustedPeer)
	out.Flow = sip.Flow{Transport: sip.UDP, Local: servedA, Remote: untrustedPeer}

	rs := siptest.NewResponses(t)
	if _, err := h.l.Request(ctx, out, rs); err != nil {
		t.Fatal(err)
	}

	req, _ := h.untrusted.RecvRequest()
	if req.Header.Has("traceparent") || req.Header.Has("tracestate") {
		t.Fatalf("trace context sent to a UE: %q %q", req.Header.Get("traceparent"), req.Header.Get("tracestate"))
	}

	h.untrusted.Send(sip.UDP, servedA, sip.NewResponse(req, 200, ""))
	wantCode(t, rs.Next(), 200)

	s := h.ended(1)[0]
	if s.Status().Code != codes.Unset {
		t.Fatalf("status %v", s.Status())
	}

	wantNoAttr(t, s, "network.peer.address")
	wantNoAttr(t, s, "error.type")
}

func TestClientSpanTimesOut(t *testing.T) {
	h := newTraceHarness(t)

	rs := siptest.NewResponses(t)
	if _, err := h.l.Request(context.Background(), newFlowRequest("OPTIONS", sip.UDP, servedA, trustedPeer), rs); err != nil {
		t.Fatal(err)
	}

	h.trusted.RecvRequest()
	h.clock.Advance(64 * t1)

	if err := rs.NextError(); !errors.Is(err, transaction.ErrTimeout) {
		t.Fatalf("error %v, want a timeout", err)
	}

	s := h.ended(1)[0]
	if s.Status().Code != codes.Error {
		t.Fatalf("status %v", s.Status())
	}

	wantAttr(t, s, "error.type", "timeout")
}

func TestInMemoryHopIsOneTrace(t *testing.T) {
	h := newTraceHarness(t)

	ctx, parent := h.tracer.Start(context.Background(), "parent")

	rs := h.request(ctx, "MESSAGE", servedA, servedB)

	got := h.tu.NextRequest()
	h.respond(got, 200)
	wantCode(t, rs.Next(), 200)
	parent.End()

	spans := h.ended(3)
	kind := func(k trace.SpanKind) sdktrace.ReadOnlySpan {
		return spans[slices.IndexFunc(spans, func(s sdktrace.ReadOnlySpan) bool { return s.SpanKind() == k })]
	}

	client, server := kind(trace.SpanKindClient), kind(trace.SpanKindServer)
	if client.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatalf("client span parent %v", client.Parent())
	}

	if server.Parent().SpanID() != client.SpanContext().SpanID() {
		t.Fatalf("server span parent %v, want the client span", server.Parent())
	}

	if sc := trace.SpanContextFromContext(got.Ctx); sc.SpanID() != server.SpanContext().SpanID() {
		t.Fatalf("handler span %s, want the server span", sc.SpanID())
	}

	wantAttr(t, client, roleKey, servedA.String())
	wantAttr(t, server, roleKey, servedB.String())
}

// request sends a request from the layer, in the trace of ctx.
func (h *traceHarness) request(ctx context.Context, method string, from, to netip.AddrPort) *siptest.Responses {
	h.t.Helper()

	rs := siptest.NewResponses(h.t)
	if _, err := h.l.Request(ctx, newFlowRequest(method, sip.UDP, from, to), rs); err != nil {
		h.t.Fatal(err)
	}

	return rs
}

func links(s sdktrace.ReadOnlySpan) []trace.SpanID {
	var out []trace.SpanID
	for _, l := range s.Links() {
		out = append(out, l.SpanContext.SpanID())
	}

	return out
}

func TestAckOf2xxIsASpanLinkedToTheInvite(t *testing.T) {
	h := newTraceHarness(t)

	invite := tracedRequest("INVITE", untrustedPeer, servedA)
	h.untrusted.Send(sip.UDP, servedA, invite)

	got := h.tu.NextRequest()
	h.respond(got, 200)

	ok, _ := h.untrusted.RecvResponse()
	for ok.StatusCode != 200 {
		ok, _ = h.untrusted.RecvResponse()
	}

	inviteSpan := h.ended(1)[0]

	ack := sip.NewRequest("ACK", invite.URI)
	ack.Header = nil

	for _, name := range []string{"Via", "From", "Call-ID"} {
		ack.Header.Add(name, invite.Header.Get(name))
	}

	ack.Header.Add("To", ok.Header.Get("To"))
	ack.Header.Add("CSeq", "1 ACK")
	ack.Header.Add("Content-Length", "0")
	ack.Header.Set("traceparent", remoteTraceparent)
	h.untrusted.Send(sip.UDP, servedA, ack)
	h.tu.NextAck()

	s := h.ended(2)[1]
	if s.Name() != "ACK" || s.SpanKind() != trace.SpanKindServer {
		t.Fatalf("span %s %s", s.Name(), s.SpanKind())
	}

	if s.Parent().IsValid() {
		t.Fatalf("ACK of a UE continues its trace: parent %v", s.Parent())
	}

	if got := links(s); !slices.Equal(got, []trace.SpanID{inviteSpan.SpanContext().SpanID()}) {
		t.Fatalf("ACK links %v, want the INVITE's %s", got, inviteSpan.SpanContext().SpanID())
	}
}

func TestCancelIsASpanLinkedToTheInvite(t *testing.T) {
	h := newTraceHarness(t)

	invite := tracedRequest("INVITE", trustedPeer, servedA)
	h.trusted.Send(sip.UDP, servedA, invite)

	got := h.tu.NextRequest()
	h.respond(got, 180)

	cancel, err := sip.NewCancel(invite)
	if err != nil {
		t.Fatal(err)
	}

	h.trusted.Send(sip.UDP, servedA, cancel)

	c := h.tu.NextCancel()
	h.respond(got, 487)

	spans := h.ended(2)
	byName := func(n string) sdktrace.ReadOnlySpan {
		return spans[slices.IndexFunc(spans, func(s sdktrace.ReadOnlySpan) bool { return s.Name() == n })]
	}

	inviteSpan, cancelSpan := byName("INVITE"), byName("CANCEL")
	if got := links(cancelSpan); !slices.Equal(got, []trace.SpanID{inviteSpan.SpanContext().SpanID()}) {
		t.Fatalf("CANCEL links %v, want the INVITE's", got)
	}

	if sc := trace.SpanContextFromContext(c.Ctx); sc.SpanID() != cancelSpan.SpanContext().SpanID() {
		t.Fatalf("HandleCancel span %s, want the CANCEL's", sc.SpanID())
	}

	wantAttr(t, cancelSpan, "sip.response.status_code", int64(200))
	wantAttr(t, inviteSpan, "sip.response.status_code", int64(487))
}

func TestHandlerContextEndsWithTheTransaction(t *testing.T) {
	h := newTraceHarness(t)

	h.trusted.Send(sip.UDP, servedA, tracedRequest("MESSAGE", trustedPeer, servedA))

	got := h.tu.NextRequest()

	select {
	case <-got.Ctx.Done():
		t.Fatal("the context ended before the transaction")
	default:
	}

	h.respond(got, 200)

	// Timer J (RFC 3261 §17.2.2), zero on a reliable flow, ends the transaction.
	h.clock.Advance(64 * t1)

	select {
	case <-got.Ctx.Done():
	case <-time.After(siptest.Timeout):
		t.Fatal("the context outlived the transaction")
	}

	if err := context.Cause(got.Ctx); !errors.Is(err, transaction.ErrTerminated) {
		t.Fatalf("cause %v, want ErrTerminated", err)
	}
}

func TestHandlerContextEndsWithACancel(t *testing.T) {
	h := newTraceHarness(t)

	invite := tracedRequest("INVITE", trustedPeer, servedA)
	h.trusted.Send(sip.UDP, servedA, invite)

	got := h.tu.NextRequest()
	h.respond(got, 180)

	cancel, err := sip.NewCancel(invite)
	if err != nil {
		t.Fatal(err)
	}

	h.trusted.Send(sip.UDP, servedA, cancel)
	h.tu.NextCancel()

	select {
	case <-got.Ctx.Done():
	case <-time.After(siptest.Timeout):
		t.Fatal("the INVITE's context did not end with its CANCEL")
	}

	if err := context.Cause(got.Ctx); !errors.Is(err, transaction.ErrCanceled) {
		t.Fatalf("cause %v, want ErrCanceled", err)
	}
}

type ctxKey struct{}

// capture is a client handler that keeps the context of its calls.
type capture struct{ ctxs chan context.Context }

func (c capture) HandleResponse(ctx context.Context, _ *sip.Response) { c.ctxs <- ctx }

func (c capture) HandleError(ctx context.Context, _ error) { c.ctxs <- ctx }

func TestClientHandlerContextKeepsTheCallersValues(t *testing.T) {
	h := newTraceHarness(t)

	parent, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "caller"))
	c := capture{ctxs: make(chan context.Context, 4)}

	if _, err := h.l.Request(parent, newFlowRequest("OPTIONS", sip.UDP, servedA, trustedPeer), c); err != nil {
		t.Fatal(err)
	}

	cancel()

	req, _ := h.trusted.RecvRequest()
	h.trusted.Send(sip.UDP, servedA, sip.NewResponse(req, 200, ""))

	ctx := <-c.ctxs
	if ctx.Value(ctxKey{}) != "caller" {
		t.Fatal("the caller's values are lost")
	}

	if ctx.Err() != nil {
		t.Fatal("the response's context ended with the caller's, not the layer's")
	}
}

func TestUnknownMethodIsOther(t *testing.T) {
	h := newTraceHarness(t)

	h.trusted.Send(sip.UDP, servedA, tracedRequest("XYZZY", trustedPeer, servedA))

	got := h.tu.NextRequest()
	h.respond(got, 501)

	s := h.ended(1)[0]
	if s.Name() != "_OTHER" {
		t.Fatalf("span %q, want _OTHER", s.Name())
	}

	wantAttr(t, s, "sip.request.method", "_OTHER")
	wantAttr(t, s, "sip.request.method_original", "XYZZY")
}

func TestErrorStatusDoesNotQuoteTheRequest(t *testing.T) {
	h := newTraceHarness(t)

	rs := siptest.NewResponses(t)
	if _, err := h.l.Request(context.Background(), newFlowRequest("OPTIONS", sip.UDP, servedA, trustedPeer), rs); err != nil {
		t.Fatal(err)
	}

	h.trusted.RecvRequest()
	h.clock.Advance(64 * t1)

	if err := rs.NextError(); !errors.Is(err, transaction.ErrTimeout) {
		t.Fatalf("error %v", err)
	}

	if got := h.ended(1)[0].Status().Description; got != "timeout" {
		t.Fatalf("status description %q, want the error's type", got)
	}
}

func TestChallengeIsNotAnError(t *testing.T) {
	for _, code := range []int{401, 407} {
		h := newTraceHarness(t)

		rs := h.request(context.Background(), "REGISTER", servedA, trustedPeer)

		req, _ := h.trusted.RecvRequest()
		h.trusted.Send(sip.UDP, servedA, sip.NewResponse(req, code, ""))
		wantCode(t, rs.Next(), code)

		s := h.ended(1)[0]
		if s.Status().Code != codes.Unset {
			t.Fatalf("%d: status %v", code, s.Status())
		}

		wantNoAttr(t, s, "error.type")
		wantAttr(t, s, "sip.response.status_code", int64(code))
	}
}

func TestOwnCancelIsLinkedAndNotAnError(t *testing.T) {
	h := newTraceHarness(t)

	rs := siptest.NewResponses(t)

	tx, err := h.l.Request(context.Background(), newFlowRequest("INVITE", sip.UDP, servedA, trustedPeer), rs)
	if err != nil {
		t.Fatal(err)
	}

	invite, _ := h.trusted.RecvRequest()
	h.trusted.Send(sip.UDP, servedA, sip.NewResponse(invite, 180, ""))
	wantCode(t, rs.Next(), 180)

	cause, causeSpan := h.tracer.Start(context.Background(), "hang up")
	causeSpan.End()

	if err := tx.Cancel(cause); err != nil {
		t.Fatal(err)
	}

	cancel, _ := h.trusted.RecvRequest()
	if cancel.Method != "CANCEL" {
		t.Fatalf("got %s", cancel.Method)
	}

	h.trusted.Send(sip.UDP, servedA, sip.NewResponse(cancel, 200, ""))
	h.trusted.Send(sip.UDP, servedA, sip.NewResponse(invite, 487, ""))
	wantCode(t, rs.Next(), 487)

	spans := h.ended(3)
	byName := func(n string) sdktrace.ReadOnlySpan {
		return spans[slices.IndexFunc(spans, func(s sdktrace.ReadOnlySpan) bool { return s.Name() == n })]
	}

	inviteSpan, cancelSpan := byName("INVITE"), byName("CANCEL")
	if cancelSpan.Parent().SpanID() != inviteSpan.SpanContext().SpanID() {
		t.Fatalf("CANCEL parent %v, want the INVITE's span", cancelSpan.Parent())
	}

	if got := links(cancelSpan); !slices.Equal(got, []trace.SpanID{causeSpan.SpanContext().SpanID()}) {
		t.Fatalf("CANCEL links %v, want its cause", got)
	}

	if inviteSpan.Status().Code != codes.Unset {
		t.Fatalf("the 487 of our own CANCEL is an error: %v", inviteSpan.Status())
	}
}

func TestResponsesCarryNoTraceContext(t *testing.T) {
	h := newTraceHarness(t)

	h.untrusted.Send(sip.UDP, servedA, tracedRequest("MESSAGE", untrustedPeer, servedA))

	got := h.tu.NextRequest()

	// A response relayed from the core, which carries its trace context.
	res := sip.NewResponse(got.Req, 200, "")
	res.Header.Set("traceparent", remoteTraceparent)
	res.Header.Set("tracestate", "vendor=1")

	if err := got.Tx.Respond(res); err != nil {
		t.Fatal(err)
	}

	sent, _ := h.untrusted.RecvResponse()
	if sent.Header.Has("traceparent") || sent.Header.Has("tracestate") {
		t.Fatalf("trace context sent to a UE: %q", sent.Header.Get("traceparent"))
	}
}

func TestLayerWithoutProviderTracesNothing(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	global := otel.GetTracerProvider()

	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(global) })

	l, tu := siptest.NewLayer(t, transaction.Config{Clock: siptest.NewClock()})
	if err := l.Serve(servedA); err != nil {
		t.Fatal(err)
	}

	peer := siptest.NewPeer(t, l, trustedPeer)
	peer.Send(sip.UDP, servedA, tracedRequest("MESSAGE", trustedPeer, servedA))

	got := tu.NextRequest()
	if err := got.Tx.Respond(sip.NewResponse(got.Req, 200, "")); err != nil {
		t.Fatal(err)
	}

	if spans := rec.Ended(); len(spans) != 0 {
		t.Fatalf("the global provider traced %v", names(spans))
	}
}
