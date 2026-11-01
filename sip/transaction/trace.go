package transaction

import (
	"context"
	"errors"
	"strconv"

	"github.com/ellanetworks/ims/sip"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/ellanetworks/ims/sip/transaction"

// The attributes of SIP spans. OpenTelemetry has no SIP conventions: these are the names of OpenSIPS's.
const (
	methodKey = attribute.Key("sip.request.method")
	statusKey = attribute.Key("sip.response.status_code")
	callIDKey = attribute.Key("sip.call_id")
	// The ICID of P-Charging-Vector (RFC 7315 §5.6), which joins a trace to its charging and call records.
	icidKey = attribute.Key("ella.ims.icid")

	provisionalEvent    = "sip.provisional_response"
	retransmissionEvent = "sip.retransmission"
)

// The header fields of W3C Trace Context, which SIP carries as it does HTTP.
const (
	traceparent = "traceparent"
	tracestate  = "tracestate"
)

var propagator = propagation.TraceContext{}

func providerOf(tp trace.TracerProvider) trace.TracerProvider {
	if tp == nil {
		return otel.GetTracerProvider()
	}

	return tp
}

// TracerProvider traces the layer, for the agents on it to trace their own work with the same one.
func (l *Layer) TracerProvider() trace.TracerProvider {
	return l.tp
}

// headerCarrier carries the trace context of a SIP message in its header fields.
type headerCarrier struct{ h *sip.Header }

func (c headerCarrier) Get(key string) string { return c.h.Get(key) }

func (c headerCarrier) Set(key, value string) { c.h.Set(key, value) }

func (c headerCarrier) Keys() []string {
	var keys []string

	for _, k := range []string{traceparent, tracestate} {
		if c.h.Has(k) {
			keys = append(keys, k)
		}
	}

	return keys
}

// traces reports whether flow f carries trace context, which only flows within the trust domain do.
func (l *Layer) traces(f sip.Flow) bool {
	return l.traceContext != nil && l.traceContext(f)
}

// propagate gives req the trace context of ctx when its flow carries trace context, and none otherwise: a proxy copies
// the trace context of the request it forwards (RFC 3261 §16.6), which is that of the previous hop.
func (l *Layer) propagate(ctx context.Context, req *sip.Request) {
	req.Header.Del(traceparent)
	req.Header.Del(tracestate)

	if l.traces(req.Flow) {
		propagator.Inject(ctx, headerCarrier{&req.Header})
	}
}

// remote is the context of the trace that req continues: the trace context it carries, when its flow carries trace
// context.
func (l *Layer) remote(req *sip.Request) context.Context {
	if !l.traces(req.Flow) {
		return context.Background()
	}

	return propagator.Extract(context.Background(), headerCarrier{&req.Header})
}

// requestAttributes are the attributes of the span of req: those of its flow, and the address of its peer if of the
// trust domain, never that of a UE.
func (l *Layer) requestAttributes(req *sip.Request) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		semconv.NetworkProtocolName("sip"),
		methodKey.String(req.Method),
	}

	switch req.Flow.Transport {
	case sip.UDP:
		attrs = append(attrs, semconv.NetworkTransportUDP)
	case sip.TCP:
		attrs = append(attrs, semconv.NetworkTransportTCP)
	}

	if id := req.Header.Get("Call-ID"); id != "" {
		attrs = append(attrs, callIDKey.String(id))
	}

	if id := req.Header.ICID(); id != "" {
		attrs = append(attrs, icidKey.String(id))
	}

	if l.traces(req.Flow) && req.Flow.Remote.IsValid() {
		attrs = append(attrs, semconv.NetworkPeerAddress(req.Flow.Remote.Addr().Unmap().String()),
			semconv.NetworkPeerPort(int(req.Flow.Remote.Port())))
	}

	if l.attributes != nil {
		attrs = append(attrs, l.attributes(req.Flow)...)
	}

	return attrs
}

// scope is the context of the work that ctx causes on the layer: the trace of ctx, ended only once the layer closes.
// The work a request causes outlives its transaction, and is part of its trace.
func (l *Layer) scope(ctx context.Context) context.Context {
	return trace.ContextWithSpan(l.ctx, trace.SpanFromContext(ctx))
}

// span is the span of a transaction, which ends once: at its first final response, or when the transaction ends
// without one.
type span struct {
	trace.Span

	ended bool
}

func (s *span) recording() bool {
	return s.Span != nil && !s.ended && s.IsRecording()
}

func (s *span) event(name string, attrs ...attribute.KeyValue) {
	if s.recording() {
		s.AddEvent(name, trace.WithAttributes(attrs...))
	}
}

// end ends the span with the final response res, or with err when there is none. A response from failFrom is an error:
// 500 for a server span, whose 4xx are the client's, and 400 for a client span.
func (s *span) end(res *sip.Response, err error, failFrom int) {
	if s.Span == nil || s.ended {
		return
	}

	s.ended = true

	switch {
	case res != nil:
		s.SetAttributes(statusKey.Int(res.StatusCode))

		if res.StatusCode >= failFrom {
			s.SetAttributes(semconv.ErrorTypeKey.String(strconv.Itoa(res.StatusCode)))
			s.SetStatus(codes.Error, "")
		}
	case err != nil:
		s.SetAttributes(semconv.ErrorTypeKey.String(errorType(err)))
		s.SetStatus(codes.Error, err.Error())
	}

	s.End()
}

func errorType(err error) string {
	switch {
	case errors.Is(err, ErrTimeout):
		return "timeout"
	case errors.Is(err, ErrTransport):
		return "transport"
	case errors.Is(err, ErrDropped):
		return "dropped"
	case errors.Is(err, ErrClosed):
		return "closed"
	}

	return semconv.ErrorTypeOther.Value.AsString()
}

// handleAck passes the ACK of a 2xx, which has no transaction (RFC 3261 §17.1.1.3), to the handler, in a span of its
// own, linked to the INVITE's.
func (l *Layer) handleAck(ack *sip.Request, invite trace.SpanContext) {
	var opts []trace.SpanStartOption
	if invite.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: invite}))
	}

	opts = append(opts, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(l.requestAttributes(ack)...))

	_, s := l.tracer.Start(l.remote(ack), ack.Method, opts...)
	defer s.End()

	l.h.HandleAck(l.scope(trace.ContextWithSpan(context.Background(), s)), ack)
}
