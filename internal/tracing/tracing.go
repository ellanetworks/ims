// Package tracing exports the IMS's traces over OTLP.
package tracing

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const ServiceName = "ella-ims"

// RoleKey is the attribute of the role of the IMS a span is of: pcscf, icscf, scscf, mcptt or ipsmgw.
const RoleKey = attribute.Key("ella.ims.role")

// Propagator is the only trace context the IMS reads and writes: W3C Trace Context, without baggage.
var Propagator = propagation.TraceContext{}

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
		return nil, fmt.Errorf("create resource: %w", err)
	}

	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Warn("OpenTelemetry error", slog.Any("error", err))
	}))

	return sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	), nil
}

// NewTrace starts the span of work that starts later than what caused it, as a timer fires: the root of a trace of its
// own, linked to the trace of cause, if any (OpenTelemetry links work that starts later than its cause).
func NewTrace(cause context.Context, tracer trace.Tracer, name string) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindInternal)}
	if link := trace.SpanContextFromContext(cause); link.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: link}))
	}

	return tracer.Start(context.Background(), name, opts...)
}
