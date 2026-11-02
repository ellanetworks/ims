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

// The attributes of SIP spans. OpenTelemetry has no SIP conventions: these are the names of OpenSIPS's.
const (
	methodKey         = attribute.Key("sip.request.method")
	methodOriginalKey = attribute.Key("sip.request.method_original")
	statusKey         = attribute.Key("sip.response.status_code")
	callIDKey         = attribute.Key("sip.call_id")
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

// otherMethod names the spans of methods the stack does not know, as HTTP semconv does: a span name must be of low
// cardinality, and a peer can send any token as a method.
const otherMethod = "_OTHER"

// knownMethods are the methods of RFC 3261 and of the extensions the stack implements.
var knownMethods = map[string]bool{
	"INVITE": true, "ACK": true, "BYE": true, "CANCEL": true, "OPTIONS": true, "REGISTER": true, "PRACK": true,
	"SUBSCRIBE": true, "NOTIFY": true, "PUBLISH": true, "INFO": true, "REFER": true, "MESSAGE": true, "UPDATE": true,
}

// spanName is the name of the span of a request of method.
func spanName(method string) string {
	if knownMethods[method] {
		return method
	}

	return otherMethod
}

// providerOf is tp, or a provider that traces nothing: a layer traces only with the provider it is given.
func providerOf(tp trace.TracerProvider) trace.TracerProvider {
	if tp == nil {
		return noop.NewTracerProvider()
	}

	return tp
}

// Lifetime is the context of the layer's lifetime, done once it closes: the lifetime of the work the layer's users
// start themselves (lifetime.With).
func (l *Layer) Lifetime() context.Context {
	return l.ctx
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

// untraced is m without trace context when it must carry none: a response, which W3C Trace Context does not carry, or
// a request on a flow that carries no trace context, as one a proxy relays statelessly to a UE. No trace context of
// the IMS leaves the trust domain, whatever path a message takes.
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

// linksKey is the key of the span contexts a client span links to, besides its parent.
type linksKey struct{}

// WithLinks is ctx, for the client transactions whose spans link to the valid span contexts of scs besides their
// parent: work that a request continues, other than its parent, such as a request that came back from an AS outside
// the trust domain and carries no trace context of its own.
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

// parent is the context of the trace that req continues (remote), and the span contexts its span links to if it
// starts a trace of its own (Config.SpanLinks).
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

// scope is the context of the work that ctx causes on the layer: the values and trace of ctx, ended only once the
// layer closes. The work a request causes outlives its transaction, and is part of its trace.
func (l *Layer) scope(ctx context.Context) context.Context {
	return lifetime.With(l.ctx, ctx)
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

// end ends the span with the final response res, or with err when there is none. failed tells the responses that are
// errors: serverFailed for a server span, clientFailed for a client span.
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
		// The error itself, which may quote the request, stays out of the trace: its type tells what failed.
		s.SetStatus(codes.Error, errorType(err))
	}

	s.End()
}

// serverFailed tells the final responses of a server transaction that are its errors: the 5xx. A 4xx is the client's.
func serverFailed(code int) bool { return code >= 500 }

// clientFailed tells the final responses to a client transaction that are its errors: from 400, but for the
// challenges of digest authentication, a step of the exchange (RFC 3261 §22.2, §22.3), and the 487 of an INVITE the
// client cancelled itself (§9.1).
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

// handleAck passes the ACK of a 2xx, which has no transaction (RFC 3261 §17.1.1.3), to the handler, in a span of its
// own, linked to the INVITE's.
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

	// The context ends once the handler returns, as an HTTP request's does: work that outlives it binds itself to a
	// lifetime of its own (lifetime.With).
	ctx, cancel := context.WithCancel(lifetime.With(l.ctx, trace.ContextWithSpan(context.Background(), s)))
	defer cancel()

	l.h.HandleAck(ctx, ack)
}
