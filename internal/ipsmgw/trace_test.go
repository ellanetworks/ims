package ipsmgw

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func served(spans *tracetest.SpanRecorder, method string) []trace.SpanContext {
	var out []trace.SpanContext

	for _, s := range spans.Started() {
		if s.Name() == method && s.SpanKind() == trace.SpanKindServer {
			out = append(out, s.SpanContext())
		}
	}

	return out
}

func detachedSpans(t *testing.T, spans *tracetest.SpanRecorder, name string, causes ...trace.SpanContext) []sdktrace.ReadOnlySpan {
	t.Helper()

	var got []sdktrace.ReadOnlySpan

	deadline := time.Now().Add(5 * time.Second)

	for {
		got = got[:0]

		for _, s := range spans.Ended() {
			if s.Name() == name {
				got = append(got, s)
			}
		}

		if len(got) >= len(causes) {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("%d %q spans, want %d", len(got), name, len(causes))
		}

		time.Sleep(time.Millisecond)
	}

	for i, s := range got[:len(causes)] {
		role := false

		for _, a := range s.Attributes() {
			role = role || a.Key == tracing.RoleKey && a.Value.AsString() == "ipsmgw"
		}

		links := s.Links()
		if s.Parent().IsValid() || !role || len(links) != 1 || !links[0].SpanContext.Equal(causes[i]) ||
			s.SpanContext().TraceID() == causes[i].TraceID() {
			t.Fatalf("%q span %d: parent %v, role %v, links %+v; want a root of the role linked to %v",
				name, i, s.Parent(), role, links, causes[i])
		}
	}

	return got
}

func TestHSSUpdateLinkedToItsCause(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	w := newWorldTraced(t, newFakeStore(), sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	impu := "sip:olivia@" + home

	w.register(impu, 600, "application/3gpp-ims+xml", serviceInfo)
	sub := w.subscribed()

	w.notify(sub, "pending;expires=600", doc(0, impu, true))
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})

	if res := w.register(impu, 0, "", ""); res.StatusCode != 200 {
		t.Fatalf("deregistration = %d", res.StatusCode)
	}

	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"}, update{msisdn: "15551230001"})

	notified, registers := served(spans, "NOTIFY"), served(spans, "REGISTER")
	if len(notified) != 1 || len(registers) != 2 {
		t.Fatalf("%d NOTIFY and %d REGISTER spans", len(notified), len(registers))
	}

	detachedSpans(t, spans, "ip-sm-gw hss update", notified[0], registers[1])
}

func TestDetachedWorkLinkedToItsCause(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	w := newWorldTraced(t, newFakeStore(), sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	impu := "sip:peggy@" + home

	w.register(impu, 60, "application/3gpp-ims+xml", serviceInfo)
	sub := w.subscribed()

	w.notify(sub, "terminated;reason=deactivated", doc(0, impu, true))
	w.subscribed()

	resub := detachedSpans(t, spans, "reg event resubscribe", served(spans, "NOTIFY")[0])[0]

	var subscribe sdktrace.ReadWriteSpan

	for _, s := range spans.Started() {
		if s.Name() == "SUBSCRIBE" && s.SpanKind() == trace.SpanKindClient && s.Parent().SpanID() == resub.SpanContext().SpanID() {
			subscribe = s
		}
	}

	if subscribe == nil {
		t.Fatalf("no SUBSCRIBE in the trace of the resubscription")
	}

	w.n.Advance(61 * time.Second)
	detachedSpans(t, spans, "registration expiry", served(spans, "REGISTER")[0])
}

func TestWritesBoundedOutsideTheLock(t *testing.T) {
	store := newFakeStore()
	w := newWorld(t, store)

	var (
		mu       sync.Mutex
		writes   int
		failures []string
	)

	store.onPut = func(ctx context.Context) {
		fail := func(why string) {
			mu.Lock()
			defer mu.Unlock()

			failures = append(failures, why)
		}

		if d, ok := ctx.Deadline(); !ok || time.Until(d) > saveTimeout {
			fail("no deadline of saveTimeout")
		}

		if ctx.Err() != nil {
			fail(fmt.Sprintf("context ended: %v", context.Cause(ctx)))
		}

		free := false

		for range 1000 {
			if w.a.reg.mu.TryLock() {
				w.a.reg.mu.Unlock()

				free = true

				break
			}

			time.Sleep(time.Millisecond)
		}

		if !free {
			fail("written under the registrar's lock")
		}

		mu.Lock()
		writes++
		mu.Unlock()
	}

	impu := "sip:quentin@" + home

	w.register(impu, 600, "application/3gpp-ims+xml", serviceInfo)
	sub := w.subscribed()
	w.notify(sub, "active;expires=600", doc(0, impu, true))
	w.hss.waitUpdates(t, update{msisdn: "15551230001", number: "15550000100"})
	w.n.Quiesce()

	mu.Lock()
	defer mu.Unlock()

	if writes < 2 || len(failures) > 0 {
		t.Fatalf("%d writes, failures %v", writes, failures)
	}
}
