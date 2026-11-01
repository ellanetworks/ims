package ue

import (
	"context"
	"testing"

	"github.com/ellanetworks/ims/internal/uetest"
	"github.com/ellanetworks/ims/sip"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestRegisterIsPartOfTheCallersTrace checks that the REGISTERs of Register are children of its caller's span, and
// that the UE sends the P-CSCF no trace context.
func TestRegisterIsPartOfTheCallersTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

	n := newNetwork(t)
	u, _ := n.newUE(Config{Plain: true, TracerProvider: tp})

	_, span := tp.Tracer("test").Start(t.Context(), "register")

	done := uetest.Start(func(ctx context.Context) error {
		return u.Register(trace.ContextWithSpan(ctx, span))
	})

	req, f := n.Recv(n.PCSCF)
	n.Challenge(req, f, n.PCSCF, false)

	req, f = n.Recv(n.PCSCF)
	if tp := req.Header.Get("Traceparent"); tp != "" {
		t.Errorf("REGISTER carries traceparent %q to the P-CSCF", tp)
	}

	res := sip.NewResponse(req, 200, "")
	res.Header.Add("Contact", req.Header.Get("Contact")+";expires=3600")
	res.Header.Add("P-Associated-URI", n.Associated)
	n.Reply(n.PCSCF, f, res)

	if err := uetest.Wait(t, done); err != nil {
		t.Fatal(err)
	}

	span.End()

	var registers int

	for _, s := range rec.Ended() {
		if s.SpanKind() != trace.SpanKindClient {
			continue
		}

		registers++

		if s.Parent().SpanID() != span.SpanContext().SpanID() || s.SpanContext().TraceID() != span.SpanContext().TraceID() {
			t.Errorf("%s span has parent %v, want the caller's %v", s.Name(), s.Parent(), span.SpanContext())
		}
	}

	if registers != 2 {
		t.Fatalf("%d client spans, want the 2 REGISTERs", registers)
	}
}
