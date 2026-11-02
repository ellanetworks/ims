package transaction

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/lifetime"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const tracerName = "github.com/ellanetworks/ims/sip/transaction"

const (
	methodKey         = attribute.Key("sip.request.method")
	methodOriginalKey = attribute.Key("sip.request.method_original")
	statusKey         = attribute.Key("sip.response.status_code")
	callIDKey         = attribute.Key("sip.call_id")
	icidKey           = attribute.Key("ella.ims.icid")

	provisionalEvent    = "sip.provisional_response"
	retransmissionEvent = "sip.retransmission"
)

const (
	traceparent = "traceparent"
	tracestate  = "tracestate"
)

var propagator = propagation.TraceContext{}

const otherMethod = "_OTHER"

var knownMethods = map[string]bool{
	"INVITE": true, "ACK": true, "BYE": true, "CANCEL": true, "OPTIONS": true, "REGISTER": true, "PRACK": true,
	"SUBSCRIBE": true, "NOTIFY": true, "PUBLISH": true, "INFO": true, "REFER": true, "MESSAGE": true, "UPDATE": true,
}

func spanName(method string) string {
	if knownMethods[method] {
		return method
	}

	return otherMethod
}

func providerOf(tp trace.TracerProvider) trace.TracerProvider {
	if tp == nil {
		return noop.NewTracerProvider()
	}

	return tp
}

func (l *Layer) Lifetime() context.Context {
	return l.ctx
}

func (l *Layer) TracerProvider() trace.TracerProvider {
	return l.tp
}

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

func (l *Layer) traces(f sip.Flow) bool {
	return l.traceContext != nil && l.traceContext(f)
}

func (l *Layer) propagate(ctx context.Context, req *sip.Request) {
	req.Header.Del(traceparent)
	req.Header.Del(tracestate)

	if l.traces(req.Flow) {
		propagator.Inject(ctx, headerCarrier{&req.Header})
	}
}

func (l *Layer) untraced(m sip.Message) sip.Message {
	h := &m.Env().Header
	if !h.Has(traceparent) && !h.Has(tracestate) {
		return m
	}

	if req, ok := m.(*sip.Request); ok && l.traces(req.Flow) {
		return m
	}

	switch v := m.(type) {
	case *sip.Request:
		m = v.Clone()
	case *sip.Response:
		m = v.Clone()
	default:
		return m
	}

	h = &m.Env().Header
	h.Del(traceparent)
	h.Del(tracestate)

	return m
}

type linksKey struct{}

func WithLinks(ctx context.Context, scs ...trace.SpanContext) context.Context {
	var links []trace.Link

	for _, sc := range scs {
		if sc.IsValid() {
			links = append(links, trace.Link{SpanContext: sc})
		}
	}

	if len(links) == 0 {
		return ctx
	}

	return context.WithValue(ctx, linksKey{}, links)
}

func linksOf(ctx context.Context) []trace.Link {
	links, _ := ctx.Value(linksKey{}).([]trace.Link)
	return links
}

func (l *Layer) parent(req *sip.Request) (context.Context, []trace.SpanContext) {
	ctx := l.remote(req)
	if l.spanLinks == nil || trace.SpanContextFromContext(ctx).IsValid() {
		return ctx, nil
	}

	return ctx, l.spanLinks(req)
}

func validLinks(scs []trace.SpanContext) []trace.Link {
	var links []trace.Link

	for _, sc := range scs {
		if sc.IsValid() {
			links = append(links, trace.Link{SpanContext: sc})
		}
	}

	return links
}

func (l *Layer) remote(req *sip.Request) context.Context {
	if !l.traces(req.Flow) {
		return context.Background()
	}

	return propagator.Extract(context.Background(), headerCarrier{&req.Header})
}

func (l *Layer) requestAttributes(req *sip.Request) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		semconv.NetworkProtocolName("sip"),
		methodKey.String(spanName(req.Method)),
	}

	if spanName(req.Method) == otherMethod {
		attrs = append(attrs, methodOriginalKey.String(req.Method))
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

func (l *Layer) scope(ctx context.Context) context.Context {
	return lifetime.With(l.ctx, ctx)
}

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

func (s *span) end(res *sip.Response, err error, failed func(code int) bool) {
	if s.Span == nil || s.ended {
		return
	}

	s.ended = true

	switch {
	case res != nil:
		s.SetAttributes(statusKey.Int(res.StatusCode))

		if failed(res.StatusCode) {
			s.SetAttributes(semconv.ErrorTypeKey.String(strconv.Itoa(res.StatusCode)))
			s.SetStatus(codes.Error, "")
		}
	case err != nil:
		s.SetAttributes(semconv.ErrorTypeKey.String(errorType(err)))
		s.SetStatus(codes.Error, errorType(err))
	}

	s.End()
}

func serverFailed(code int) bool { return code >= 500 }

// RFC 3261 section 22.2
// RFC 3261 section 22.3
// RFC 3261 section 9.1
func clientFailed(cancelled bool) func(code int) bool {
	return func(code int) bool {
		switch code {
		case 401, 407:
			return false
		case 487:
			return !cancelled
		}

		return code >= 400
	}
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

// RFC 3261 section 17.1.1.3
func (l *Layer) handleAck(ack *sip.Request, invite trace.SpanContext) {
	parent, links := l.parent(ack)
	if invite.IsValid() && !slices.ContainsFunc(links, invite.Equal) {
		links = append(links, invite)
	}

	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(l.requestAttributes(ack)...),
		trace.WithLinks(validLinks(links)...),
	}

	_, s := l.tracer.Start(parent, spanName(ack.Method), opts...)
	defer s.End()

	ctx, cancel := context.WithCancel(lifetime.With(l.ctx, trace.ContextWithSpan(context.Background(), s)))
	defer cancel()

	l.h.HandleAck(ctx, ack)
}
