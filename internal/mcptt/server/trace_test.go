package server

import (
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func spansOf(rec *tracetest.SpanRecorder, name string, kind oteltrace.SpanKind) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, s := range rec.Ended() {
		if s.Name() == name && s.SpanKind() == kind {
			out = append(out, s)
		}
	}

	return out
}

func detachedFrom(t *testing.T, s, cause sdktrace.ReadOnlySpan) {
	t.Helper()

	if s.Parent().IsValid() || s.SpanContext().TraceID() == cause.SpanContext().TraceID() {
		t.Fatalf("%s is in the trace of %s: parent %v", s.Name(), cause.Name(), s.Parent())
	}

	if l := s.Links(); len(l) != 1 || l[0].SpanContext.SpanID() != cause.SpanContext().SpanID() {
		t.Fatalf("%s links %v, want %s %v", s.Name(), l, cause.Name(), cause.SpanContext())
	}

	for _, kv := range s.Attributes() {
		if kv.Key == tracing.RoleKey && kv.Value.AsString() == "mcptt" {
			return
		}
	}

	t.Fatalf("%s has no role: %v", s.Name(), s.Attributes())
}

func TestUnreadableDocumentResubscribeTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	h := newTracedHarness(t, registrar.ThroughIMS, nil, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	s := h.scscf
	s.regEvent = true
	ue := h.client(aliceIM)

	if res := s.send(s.register(aliceIM, "t1", 600, ue.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("alice: %d", res.StatusCode)
	}

	s.pump()
	subscribes := s.subscribes

	s.state = "terminated;reason=deactivated"
	s.notify(aliceIM, 1, "full", `<registration aor="`+aliceIM+`" id="r1" state="gone"/>`)
	s.state = ""

	if res := s.notified[len(s.notified)-1]; res.StatusCode != 400 {
		t.Fatalf("NOTIFY of an unreadable document: %d", res.StatusCode)
	}

	h.n.Advance(time.Second)
	s.pump()

	if s.subscribes != subscribes+1 {
		t.Fatalf("%d SUBSCRIBEs, want one more than %d", s.subscribes, subscribes)
	}

	h.n.Quiesce()

	notifies := spansOf(rec, "NOTIFY", oteltrace.SpanKindServer)
	again := spansOf(rec, "reg event resubscribe", oteltrace.SpanKindInternal)

	if len(notifies) != 3 || len(again) != 1 {
		t.Fatalf("%d NOTIFYs served, %d resubscribes", len(notifies), len(again))
	}

	slices.SortFunc(notifies, func(a, b sdktrace.ReadOnlySpan) int { return a.StartTime().Compare(b.StartTime()) })

	detachedFrom(t, again[0], notifies[1])

	subs := spansOf(rec, "SUBSCRIBE", oteltrace.SpanKindClient)
	slices.SortFunc(subs, func(a, b sdktrace.ReadOnlySpan) int { return a.StartTime().Compare(b.StartTime()) })

	if len(subs) != 2 || subs[1].Parent().SpanID() != again[0].SpanContext().SpanID() {
		t.Fatalf("the SUBSCRIBE again is not of the resubscribe: %d SUBSCRIBEs", len(subs))
	}
}

func TestPublishWriteTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	st := newMemState()
	h := newTracedHarness(t, registrar.ThroughIMS, st, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	s := h.scscf
	ue := h.client(aliceIM)

	if res := s.send(s.register(aliceIM, "t1", 600, ue.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("alice: %d", res.StatusCode)
	}

	st.mu.Lock()
	before := len(st.affWrites)
	st.mu.Unlock()

	if res := s.send(s.forward(ue.publish(alice, client1, 4294967295, fire, police), aliceIM)); res.StatusCode != 200 {
		t.Fatalf("PUBLISH: %d", res.StatusCode)
	}

	h.n.Quiesce()

	publishes := spansOf(rec, "PUBLISH", oteltrace.SpanKindServer)
	if len(publishes) != 1 {
		t.Fatalf("%d PUBLISHes served", len(publishes))
	}

	st.mu.Lock()
	writes := st.affWrites[before:]
	st.mu.Unlock()

	if len(writes) != 1 {
		t.Fatalf("%d writes of the PUBLISH", len(writes))
	}

	if w := writes[0]; w.span.SpanID() != publishes[0].SpanContext().SpanID() || w.err != nil || !w.deadline {
		t.Fatalf("write of span %v, error %v, deadline %v; want of the PUBLISH %v, live, bounded", w.span, w.err,
			w.deadline, publishes[0].SpanContext())
	}
}
