package registrar

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type saves struct {
	mu   sync.Mutex
	ctxs []context.Context
	errs []error
}

func (s *saves) SaveRegistrations(ctx context.Context, _ []Record, _ []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ctxs, s.errs = append(s.ctxs, ctx), append(s.errs, ctx.Err())

	return nil
}

type loggedOff struct {
	reconfigured

	mu   sync.Mutex
	ctxs []context.Context
}

func (l *loggedOff) LogOff(ctx context.Context, _ time.Time, _ sip.URI, _ string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.ctxs = append(l.ctxs, ctx)
}

func TestExpiryTrace(t *testing.T) {
	n := siptest.NewNet(t)
	addr := n.Addr()

	a, err := ua.New(ua.Config{
		Transaction: n.Config(transaction.Config{}),
		Contact:     sip.Address{URI: sip.URI{Scheme: "sip", Host: addr.Addr().String(), Port: addr.Port()}},
		Prepare:     func(context.Context, *sip.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { n.Await(func() { _ = a.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1) })

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	store, offs := &saves{}, &loggedOff{}
	st := a.NewStrand()

	g := New(Config{
		Mode: ThroughIMS, Store: store, StrandOf: func(string) *ua.Strand { return st }, Affiliations: offs,
		Clock: a.Layer().Clock(), TracerProvider: tp, Logger: slog.New(slog.DiscardHandler),
	})

	now := g.cfg.Clock.Now()
	alice, impu := uri(t, "sip:alice@mcptt.example.com"), uri(t, "sip:a@ims.example.com")

	var ch change

	g.mu.Lock()
	g.bind(&binding{
		key: bindingKey{settings.Key(impu), "c1"}, user: settings.Key(alice),
		Binding: Binding{MCPTTID: alice, ClientID: "c1", IMPU: impu, IMPUs: []sip.URI{impu}, Expires: now.Add(time.Minute)},
	}, &ch)
	g.mu.Unlock()

	ctx, end := context.WithCancel(context.Background())
	ctx, register := tp.Tracer("test").Start(ctx, "REGISTER")

	end()
	register.End()

	st.Post(func() { g.apply(ctx, st, now, &ch) })
	n.Quiesce()
	n.Advance(time.Minute)
	n.Quiesce()

	store.mu.Lock()
	ctxs, errs := store.ctxs, store.errs
	store.mu.Unlock()

	if len(ctxs) != 2 {
		t.Fatalf("%d writes, want the REGISTER's and the expiry's", len(ctxs))
	}

	for i, err := range errs {
		if err != nil {
			t.Fatalf("write %d cancelled: %v", i, err)
		}
	}

	if sc := trace.SpanContextFromContext(ctxs[0]); sc.SpanID() != register.SpanContext().SpanID() {
		t.Fatalf("the REGISTER's write is of span %v, want %v", sc, register.SpanContext())
	}

	var expiry sdktrace.ReadOnlySpan

	for _, s := range rec.Ended() {
		if s.Name() == expiryName {
			expiry = s
		}
	}

	if expiry == nil {
		t.Fatal("no expiry span")
	}

	if expiry.Parent().IsValid() || expiry.SpanContext().TraceID() == register.SpanContext().TraceID() {
		t.Fatalf("the expiry is in the REGISTER's trace: parent %v", expiry.Parent())
	}

	if l := expiry.Links(); len(l) != 1 || l[0].SpanContext.SpanID() != register.SpanContext().SpanID() {
		t.Fatalf("the expiry links %v, want the REGISTER", l)
	}

	if !hasRole(expiry) {
		t.Fatalf("the expiry has no role: %v", expiry.Attributes())
	}

	if sc := trace.SpanContextFromContext(ctxs[1]); sc.SpanID() != expiry.SpanContext().SpanID() {
		t.Fatalf("the expiry's write is of span %v, want the expiry's %v", sc, expiry.SpanContext())
	}

	offs.mu.Lock()
	off := offs.ctxs
	offs.mu.Unlock()

	if len(off) != 1 || trace.SpanContextFromContext(off[0]).SpanID() != expiry.SpanContext().SpanID() || off[0].Err() != nil {
		t.Fatalf("log-offs %v, want one of the expiry, live", off)
	}

	g.Close()

	if off[0].Err() == nil {
		t.Fatal("the expiry's work outlives the registrar")
	}
}

func hasRole(s sdktrace.ReadOnlySpan) bool {
	for _, kv := range s.Attributes() {
		if kv.Key == tracing.RoleKey && kv.Value.AsString() == "mcptt" {
			return true
		}
	}

	return false
}
