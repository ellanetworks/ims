package tracing

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestNewProvider checks that the spans name the IMS and its version, and that the OTEL_* environment variables do
// not apply: the configuration file is the only one.
func TestNewProvider(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "ims-west")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")

	// No collector listens on port 1, so that the test exports nothing.
	tp, err := NewProvider(t.Context(), "127.0.0.1:1", "v1.2.3", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	rec := tracetest.NewSpanRecorder()
	tp.RegisterSpanProcessor(rec)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		_ = tp.Shutdown(ctx)
	})

	_, span := tp.Tracer("test").Start(t.Context(), "span")
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}

	res := attribute.NewSet(spans[0].Resource().Attributes()...)
	service, _ := res.Value("service.name")
	version, _ := res.Value("service.version")

	if service.AsString() != ServiceName || version.AsString() != "v1.2.3" {
		t.Fatalf("service %q version %q, want %q v1.2.3", service.AsString(), version.AsString(), ServiceName)
	}
}
