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

const RoleKey = attribute.Key("ella.ims.role")

var Propagator = propagation.TraceContext{}

var errorHandler sync.Once

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

type Detached struct {
	tracer trace.Tracer
	role   attribute.KeyValue
}

func NewDetached(tp trace.TracerProvider, scope, role string) Detached {
	if tp == nil {
		tp = noop.NewTracerProvider()
	}

	return Detached{tracer: tp.Tracer(scope, trace.WithSchemaURL(semconv.SchemaURL)), role: RoleKey.String(role)}
}

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

func End(span trace.Span, err error) {
	if err != nil {
		typ := fmt.Sprintf("%T", err)
		span.SetAttributes(semconv.ErrorTypeKey.String(typ))
		span.SetStatus(codes.Error, typ)
	}

	span.End()
}
