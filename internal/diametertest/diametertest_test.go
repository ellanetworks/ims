package diametertest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type key struct{}

// TestLoopServesAsANode checks that Loop's handler gets what a node's does: a server span continuing the caller's
// trace, none of the caller's values, and a context the caller giving up does not cancel.
func TestLoopServesAsANode(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

	served := make(chan context.Context, 1)
	release := make(chan struct{})

	l := &Loop{
		Local: diameter.Identity{OriginHost: "ims.example.org", OriginRealm: "example.org"},
		Handler: func(ctx context.Context, req *diameter.Message) *diameter.Message {
			served <- ctx

			<-release

			return nil
		},
		TracerProvider: tp,
	}
	t.Cleanup(l.Close)

	caller, span := tp.Tracer("test").Start(context.WithValue(t.Context(), key{}, "caller"), "client")
	defer span.End()

	caller, cancel := context.WithTimeout(caller, 50*time.Millisecond)
	defer cancel()

	if _, err := l.Send(caller, &diameter.Message{ApplicationID: 16777216, CommandCode: 301}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send = %v, want the caller's deadline", err)
	}

	ctx := <-served

	if ctx.Err() != nil {
		t.Errorf("handler context ended with the caller's: %v", ctx.Err())
	}

	if ctx.Value(key{}) != nil {
		t.Error("handler context carries the caller's values")
	}

	if sc := trace.SpanContextFromContext(ctx); sc.TraceID() != span.SpanContext().TraceID() || sc.SpanID() == span.SpanContext().SpanID() {
		t.Errorf("handler span %v, want a child of the caller's %v", sc, span.SpanContext())
	}

	close(release)
	l.Close()

	ended := rec.Ended()
	if len(ended) != 1 || ended[0].SpanKind() != trace.SpanKindServer || ended[0].Parent().SpanID() != span.SpanContext().SpanID() {
		t.Fatalf("spans %v, want one server span, child of the caller's", ended)
	}
}
