package tracing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/ellanetworks/ims/internal/tracing"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestDetachedIsARootLinkedToItsCauses(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	tracer := tp.Tracer("test")

	_, a := tracer.Start(context.Background(), "a")
	_, b := tracer.Start(context.Background(), "b")

	a.End()
	b.End()

	life, stop := context.WithCancel(context.Background())
	parent, p := tracer.Start(life, "parent")

	ctx, s := tracing.NewDetached(tp, "scope", "pcscf").Start(parent, "work", a.SpanContext(), trace.SpanContext{}, b.SpanContext())
	tracing.End(s, errors.New("failed"))
	p.End()

	var got sdktrace.ReadOnlySpan

	for _, e := range rec.Ended() {
		if e.Name() == "work" {
			got = e
		}
	}

	if got.Parent().IsValid() {
		t.Fatalf("parent %v, want a root", got.Parent())
	}

	if l := got.Links(); len(l) != 2 || l[0].SpanContext.SpanID() != a.SpanContext().SpanID() ||
		l[1].SpanContext.SpanID() != b.SpanContext().SpanID() {
		t.Fatalf("links %v", l)
	}

	if got.Status().Code != codes.Error || got.Status().Description != "*errors.errorString" {
		t.Fatalf("status %v", got.Status())
	}

	found := false
	for _, kv := range got.Attributes() {
		found = found || kv.Key == tracing.RoleKey && kv.Value.AsString() == "pcscf"
	}

	if !found {
		t.Fatalf("no role in %v", got.Attributes())
	}

	stop()

	if ctx.Err() == nil {
		t.Fatal("the work did not end with its lifetime")
	}
}

func TestLogHandlerAddsTheTrace(t *testing.T) {
	tp := sdktrace.NewTracerProvider()

	ctx, s := tp.Tracer("test").Start(context.Background(), "op")
	defer s.End()

	var buf bytes.Buffer

	log := slog.New(tracing.LogHandler(slog.NewJSONHandler(&buf, nil))).With("k", "v")
	log.InfoContext(ctx, "traced")
	log.Info("untraced")

	dec := json.NewDecoder(&buf)

	var traced, untraced map[string]any
	if err := dec.Decode(&traced); err != nil {
		t.Fatal(err)
	}

	if err := dec.Decode(&untraced); err != nil {
		t.Fatal(err)
	}

	if traced["trace_id"] != s.SpanContext().TraceID().String() || traced["span_id"] != s.SpanContext().SpanID().String() {
		t.Fatalf("traced record %v", traced)
	}

	if _, ok := untraced["trace_id"]; ok {
		t.Fatalf("untraced record %v", untraced)
	}
}
