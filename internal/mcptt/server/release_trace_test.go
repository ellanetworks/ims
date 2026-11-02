package server_test

import (
	"testing"
	"time"

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

func TestReleaseTraces(t *testing.T) {
	for _, tc := range []struct {
		name, group, span string
		run               func(w *world)
	}{
		{"T4", fire, "group call inactivity", func(w *world) {
			w.call(fire)
			w.n.Advance(31 * time.Second)
		}},
		{"TNG3", short, "group call maximum duration", func(w *world) {
			_, cb, _ := w.call(short)
			w.talk(cb, 50*time.Second)
			cb.ReleasePTT()
			w.n.Advance(11 * time.Second)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tracetest.NewSpanRecorder()
			w := newTracedWorld(t, nil, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))

			tc.run(w)

			for _, name := range []string{"alice", "bob", "carol"} {
				w.users[name].expect(t, "ended")
			}

			w.n.Quiesce()

			invites := spansOf(rec, "INVITE", oteltrace.SpanKindServer)
			released := spansOf(rec, tc.span, oteltrace.SpanKindInternal)

			if len(invites) != 1 || len(released) != 1 {
				t.Fatalf("%d INVITEs served, %d %q spans", len(invites), len(released), tc.span)
			}

			detachedFrom(t, released[0], invites[0])

			byes := 0

			for _, b := range spansOf(rec, "BYE", oteltrace.SpanKindClient) {
				if b.Parent().SpanID() == released[0].SpanContext().SpanID() {
					byes++
				}
			}

			if byes != 3 {
				t.Errorf("%d BYEs of the release, want one a member", byes)
			}
		})
	}
}
