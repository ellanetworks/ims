// Package tracing exports the IMS's traces over OTLP.
package tracing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const ServiceName = "ella-ims"

// RoleKey is the attribute of the role of the IMS a span is of: pcscf, icscf, scscf, mcptt or ipsmgw; ims for the
// work of the IMS as a whole, as applying its settings.
const RoleKey = attribute.Key("ella.ims.role")

// Propagator is the only trace context the IMS reads and writes: W3C Trace Context, without baggage.
var Propagator = propagation.TraceContext{}

var errorHandler sync.Once

// NewProvider returns a provider that exports to an OTLP/gRPC collector at endpoint, a host:port. It samples every
// trace that a caller has not decided against.
func NewProvider(ctx context.Context, endpoint, version string, logger *slog.Logger) (*sdktrace.TracerProvider, error) {
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(semconv.ServiceName(ServiceName), semconv.ServiceVersion(version)),
	)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create resource: %w", err), exp.Shutdown(ctx))
	}

	// The handler is global to the process: set once, with the logger of the first provider.
	errorHandler.Do(func() {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			logger.Warn("OpenTelemetry error", slog.Any("error", err))
		}))
	})

	return sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	), nil
}

// Detached starts the spans of the work of a role that starts later than what caused it, as a timer fires or a queue
// drains: each is the root of a trace of its own, linked to its causes, as OpenTelemetry links work that starts later
// than its cause, or a batch to what it carries.
type Detached struct {
	tracer trace.Tracer
	role   attribute.KeyValue
}

// NewDetached traces the detached work of role with tp, under the instrumentation scope scope; with none if tp is nil.
func NewDetached(tp trace.TracerProvider, scope, role string) Detached {
	if tp == nil {
		tp = noop.NewTracerProvider()
	}

	return Detached{tracer: tp.Tracer(scope, trace.WithSchemaURL(semconv.SchemaURL)), role: RoleKey.String(role)}
}

// Start starts the span named name of work bound to life, the lifetime of what does it, and linked to the valid span
// contexts of causes.
func (d Detached) Start(life context.Context, name string, causes ...trace.SpanContext) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(d.role),
	}

	for _, c := range causes {
		if c.IsValid() {
			opts = append(opts, trace.WithLinks(trace.Link{SpanContext: c}))
		}
	}

	return d.tracer.Start(life, name, opts...)
}

// End ends span, with the error err if not nil: its Go type as error.type, as for the IMS's other spans, and the Error
// status, whose description is the type, the message staying in the logs.
func End(span trace.Span, err error) {
	if err != nil {
		typ := fmt.Sprintf("%T", err)
		span.SetAttributes(semconv.ErrorTypeKey.String(typ))
		span.SetStatus(codes.Error, typ)
	}

	span.End()
}
