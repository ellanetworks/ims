package ibcf

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	resolutionEvent = "sip.resolution"
	slowResolution  = time.Second
)

var roleAttr = tracing.RoleKey.String(role)

func (b *IBCF) spanAttributes(req *sip.Request, kind trace.SpanKind) []attribute.KeyValue {
	f := req.Flow
	if !b.isExternal(f.Local) || !f.Remote.IsValid() {
		return []attribute.KeyValue{roleAttr}
	}

	attrs := []attribute.KeyValue{
		roleAttr,
		semconv.NetworkPeerAddress(f.Remote.Addr().Unmap().String()),
		semconv.NetworkPeerPort(int(f.Remote.Port())),
	}

	l := b.legs.received(req)
	if kind == trace.SpanKindClient {
		l = b.legs.sending(req)
	}

	if id := b.spanTrunk(l, f); id != "" {
		attrs = append(attrs, tracing.TrunkIDKey.String(id))
	}

	if kind == trace.SpanKindClient {
		attrs = append(attrs, semconv.ServerAddress(serverAddress(l, f)))
	}

	return attrs
}

func (b *IBCF) spanTrunk(l *leg, f sip.Flow) string {
	if l != nil && l.trunk {
		t, _ := l.call.owner()
		return t.ID
	}

	if t, ok := b.trunkFor(f); ok {
		return t.ID
	}

	if p, ok := b.peers.get(f.Remote); ok {
		return p.trunk
	}

	return ""
}

func serverAddress(l *leg, f sip.Flow) string {
	if l != nil {
		if h, ok := l.serverName(f.Remote); ok {
			return h
		}
	}

	return f.Remote.Addr().Unmap().String()
}

func (b *IBCF) untraced(req *sip.Request) bool {
	return req.Method == "OPTIONS" && !inDialog(req) && b.isExternal(req.Flow.Local)
}

// RFC 3263 section 4
func (b *IBCF) resolve(ctx context.Context, u sip.URI, transports []sip.Transport) ([]sip.Server, error) {
	start := time.Now()

	servers, err := b.cfg.Resolve(ctx, u, transports)

	if err != nil || time.Since(start) > slowResolution {
		attrs := []attribute.KeyValue{semconv.ServerAddress(strings.Trim(u.Host, "[]"))}
		if err != nil {
			attrs = append(attrs, semconv.ErrorTypeKey.String(resolutionError(err)))
		}

		trace.SpanFromContext(ctx).AddEvent(resolutionEvent, trace.WithAttributes(attrs...))
	}

	return servers, err
}

func resolutionError(err error) string {
	var dns *net.DNSError

	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &dns) && dns.IsTimeout:
		return "timeout"
	case errors.As(err, &dns) && dns.IsNotFound:
		return "not_found"
	}

	return semconv.ErrorTypeOther.Value.AsString()
}
