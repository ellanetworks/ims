package server

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type role struct {
	name    string
	handler transaction.Handler

	filter       func(m sip.Message) error
	responseFlow func(req *sip.Request, res *sip.Response) (sip.Flow, bool, error)
	accept       func(local, remote netip.AddrPort) bool
	connections  int
	tlsClient    func(f sip.Flow) (transport.TLSOptions, error)

	traceContext   func(f sip.Flow) bool
	spanAttributes func(req *sip.Request, kind trace.SpanKind) []attribute.KeyValue
	untraced       func(req *sip.Request) bool
	done           func(ctx context.Context, req *sip.Request, res *sip.Response, elapsed time.Duration)
}

type dispatcher struct {
	log *slog.Logger

	mu        sync.RWMutex
	listeners map[netip.AddrPort]string
	roles     map[string]*role
}

func newDispatcher(logger *slog.Logger) *dispatcher {
	return &dispatcher{log: logger, listeners: make(map[netip.AddrPort]string), roles: make(map[string]*role)}
}

func (d *dispatcher) bind(local netip.AddrPort, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.listeners[unmap(local)] = name
}

func (d *dispatcher) set(r *role) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.roles[r.name] = r
}

func (d *dispatcher) at(local netip.AddrPort) *role {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.roles[d.listeners[unmap(local)]]
}

func (d *dispatcher) role(f sip.Flow) *role {
	return d.at(f.Local)
}

func (d *dispatcher) handler(f sip.Flow) transaction.Handler {
	if r := d.role(f); r != nil {
		return r.handler
	}

	return nil
}

func (d *dispatcher) config(c transaction.Config) transaction.Config {
	c.Handler = d
	c.Filter = d.filter
	c.ResponseFlow = d.responseFlow
	c.TraceContext = d.traceContext
	c.SpanAttributes = d.spanAttributes
	c.SpanLinks = d.spanLinks
	c.Untraced = d.untraced
	c.OnServerDone = d.serverDone
	c.Transport.Accept = d.accept
	c.Transport.MaxConnectionsPerAddress = d.connections
	c.Transport.TLSClientOptions = d.tlsClient

	return c
}

func (d *dispatcher) filter(m sip.Message) error {
	if r := d.role(m.Env().Flow); r != nil && r.filter != nil {
		return r.filter(m)
	}

	return nil
}

func (d *dispatcher) responseFlow(req *sip.Request, res *sip.Response) (sip.Flow, bool, error) {
	if r := d.role(req.Flow); r != nil && r.responseFlow != nil {
		return r.responseFlow(req, res)
	}

	return sip.Flow{}, false, nil
}

func (d *dispatcher) accept(local, remote netip.AddrPort) bool {
	r := d.at(local)

	switch {
	case r == nil:
		return false
	case r.accept == nil:
		return true
	}

	return r.accept(local, remote)
}

func (d *dispatcher) connections(local netip.AddrPort) int {
	if r := d.at(local); r != nil {
		return r.connections
	}

	return 0
}

func (d *dispatcher) tlsClient(f sip.Flow) (transport.TLSOptions, error) {
	if r := d.role(f); r != nil && r.tlsClient != nil {
		return r.tlsClient(f)
	}

	return transport.TLSOptions{}, transport.ErrNoTLSConfig
}

func (d *dispatcher) traceContext(f sip.Flow) bool {
	r := d.role(f)

	return r != nil && r.traceContext != nil && r.traceContext(f)
}

func (d *dispatcher) spanAttributes(req *sip.Request, kind trace.SpanKind) []attribute.KeyValue {
	r := d.role(req.Flow)

	switch {
	case r == nil:
		return nil
	case r.spanAttributes != nil:
		return r.spanAttributes(req, kind)
	}

	return []attribute.KeyValue{tracing.RoleKey.String(r.name)}
}

func (d *dispatcher) spanLinks(req *sip.Request) []trace.SpanContext {
	if l, ok := d.handler(req.Flow).(interface {
		SpanLinks(req *sip.Request) []trace.SpanContext
	}); ok {
		return l.SpanLinks(req)
	}

	return nil
}

func (d *dispatcher) untraced(req *sip.Request) bool {
	r := d.role(req.Flow)

	return r != nil && r.untraced != nil && r.untraced(req)
}

func (d *dispatcher) serverDone(ctx context.Context, req *sip.Request, res *sip.Response, elapsed time.Duration) {
	r := d.role(req.Flow)

	d.logTransaction(ctx, r, req, res, elapsed)

	if r != nil && r.done != nil {
		r.done(ctx, req, res, elapsed)
	}
}

func (d *dispatcher) logTransaction(ctx context.Context, r *role, req *sip.Request, res *sip.Response, elapsed time.Duration) {
	level := slog.LevelDebug

	switch req.Method {
	case "INVITE", "BYE", "CANCEL", "UPDATE", "MESSAGE":
		level = slog.LevelInfo
	}

	if !d.log.Enabled(ctx, level) {
		return
	}

	name := ""
	if r != nil {
		name = r.name
	}

	attrs := []slog.Attr{
		slog.String("role", name),
		slog.String("method", req.Method),
		slog.String("call_id", req.Header.CallID()),
	}

	if from, err := req.Header.From(); err == nil {
		attrs = append(attrs, slog.String("from", from.URI.String()))
	}

	if to, err := req.Header.To(); err == nil {
		attrs = append(attrs, slog.String("to", to.URI.String()))
	}

	attrs = append(attrs, slog.String("remote", req.Flow.Remote.String()), slog.Duration("duration", elapsed))

	if res == nil {
		d.log.LogAttrs(ctx, level, "SIP transaction ended without a final response", attrs...)
		return
	}

	attrs = append(attrs, slog.Int("status", res.StatusCode), slog.String("reason", res.Reason))
	d.log.LogAttrs(ctx, level, "SIP transaction", attrs...)
}

func (d *dispatcher) HandleRequest(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	h := d.handler(req.Flow)
	if h == nil {
		res := sip.NewResponse(req, 503, "")
		res.Header.Set("Retry-After", "1")

		if err := tx.Respond(res); err != nil {
			d.log.DebugContext(ctx, "SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}

		return
	}

	h.HandleRequest(ctx, tx, req)
}

func (d *dispatcher) HandleCancel(ctx context.Context, tx *transaction.ServerTransaction, cancel *sip.Request) {
	if h := d.handler(tx.Request().Flow); h != nil {
		h.HandleCancel(ctx, tx, cancel)
	}
}

func (d *dispatcher) HandleAck(ctx context.Context, ack *sip.Request) {
	if h := d.handler(ack.Flow); h != nil {
		h.HandleAck(ctx, ack)
	}
}

func (d *dispatcher) HandleTransactionError(ctx context.Context, tx *transaction.ServerTransaction, err error) {
	if h := d.handler(tx.Request().Flow); h != nil {
		h.HandleTransactionError(ctx, tx, err)
	}
}

func unmap(a netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
}
