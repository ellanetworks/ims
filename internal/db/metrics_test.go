package db

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

func TestMetricsCountFailuresOnly(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if _, err := d.GetSettings(ctx); err != nil {
		t.Fatal(err)
	}

	if err := d.DeletePeer(ctx, "missing"); err == nil {
		t.Fatal("DeletePeer of a missing peer succeeded")
	}

	if _, err := d.GetCallRecord(ctx, 1); err == nil {
		t.Fatal("GetCallRecord of a missing record succeeded")
	}

	if n := testutil.CollectAndCount(d.metrics.duration); n != 3 {
		t.Fatalf("duration has %d series, want 3", n)
	}

	for pool, want := range map[string]float64{poolWrite: 0, poolRead: 0, poolLocal: 0} {
		if got := testutil.ToFloat64(d.metrics.errors.WithLabelValues(pool)); got != want {
			t.Errorf("errors{pool=%q} = %v, want %v", pool, got, want)
		}
	}

	if err := d.reader().Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := d.GetSettings(ctx); err == nil {
		t.Fatal("GetSettings on a closed database succeeded")
	}

	if got := testutil.ToFloat64(d.metrics.errors.WithLabelValues(poolRead)); got != 1 {
		t.Errorf("errors{pool=%q} = %v, want 1", poolRead, got)
	}
}

func TestMetricsLint(t *testing.T) {
	d := openTestDB(t)

	for _, c := range d.Collectors() {
		problems, err := testutil.CollectAndLint(c)
		if err != nil {
			t.Fatal(err)
		}

		for _, p := range problems {
			t.Errorf("lint: %s: %s", p.Metric, p.Text)
		}
	}
}

func TestEveryCallIsObserved(t *testing.T) {
	unobserved := map[string]bool{
		"Close": true, "Collectors": true, "Ping": true, "Apply": true, "ApplyEntries": true, "AppliedIndex": true,
		"WaitApplied": true, "OnEvents": true, "SetProposer": true, "RecordConfiguration": true, "MakeDurable": true,
		"ResetPosition": true, "JWTSecret": true, "OIDCCookieKey": true, "LeaderTime": true,
		"Schema": true, "RegisterMember": true, "Secrets": true,
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()

	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || unobserved[fn.Name.Name] || !onDB(fn) {
				continue
			}

			if !startsObserved(fn) {
				t.Errorf("%s: DB.%s does not start with d.observe(ctx, %q, ...) and defer done(&err)",
					fset.Position(fn.Pos()), fn.Name.Name, fn.Name.Name)
			}
		}
	}
}

func onDB(fn *ast.FuncDecl) bool {
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}

	id, ok := star.X.(*ast.Ident)

	return ok && id.Name == "DB"
}

func startsObserved(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) < 2 {
		return false
	}

	assign, ok := fn.Body.List[0].(*ast.AssignStmt)
	if !ok || len(assign.Lhs) != 2 || len(assign.Rhs) != 1 {
		return false
	}

	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 3 {
		return false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "observe" {
		return false
	}

	name, ok := call.Args[1].(*ast.BasicLit)
	if !ok || name.Kind != token.STRING || name.Value != strconv.Quote(fn.Name.Name) {
		return false
	}

	done, ok := assign.Lhs[1].(*ast.Ident)
	if !ok {
		return false
	}

	d, ok := fn.Body.List[1].(*ast.DeferStmt)
	if !ok {
		return false
	}

	callee, ok := d.Call.Fun.(*ast.Ident)

	return ok && callee.Name == done.Name
}

func TestSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "ims.db"), WithTracerProvider(tp))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	if _, err := d.GetSettings(context.Background()); err != nil {
		t.Fatal(err)
	}

	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("a call outside a trace started %d spans, want 0", n)
	}

	ctx, parent := tp.Tracer("test").Start(context.Background(), "parent")

	if _, err := d.GetSettings(ctx); err != nil {
		t.Fatal(err)
	}

	if err := d.DeletePeer(ctx, "missing"); err == nil {
		t.Fatal("DeletePeer of a missing peer succeeded")
	}

	if err := d.reader().Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := d.GetSettings(ctx); err == nil {
		t.Fatal("GetSettings on a closed database succeeded")
	}

	parent.End()

	var got []sdktrace.ReadOnlySpan

	for _, s := range rec.Ended() {
		if s.Name() != "parent" {
			got = append(got, s)
		}
	}

	want := []struct {
		name   string
		pool   string
		status codes.Code
	}{
		{"db.GetSettings", poolRead, codes.Unset},
		{"db.DeletePeer", poolWrite, codes.Unset},
		{"db.GetSettings", poolRead, codes.Error},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d spans, want %d", len(got), len(want))
	}

	for i, w := range want {
		s := got[i]

		if s.Name() != w.name || s.SpanKind() != trace.SpanKindInternal || s.Status().Code != w.status {
			t.Errorf("span %d = %s %s %s, want %s internal %s", i, s.Name(), s.SpanKind(), s.Status().Code, w.name, w.status)
		}

		if s.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Errorf("span %d is not a child of the caller's span", i)
		}

		attrs := attribute.NewSet(s.Attributes()...)

		if v, _ := attrs.Value(tracing.DBPoolKey); v.AsString() != w.pool {
			t.Errorf("span %d pool = %q, want %q", i, v.AsString(), w.pool)
		}

		if _, ok := attrs.Value(semconv.ErrorTypeKey); ok != (w.status == codes.Error) {
			t.Errorf("span %d has error.type %v, want %v", i, ok, w.status == codes.Error)
		}
	}
}

func TestDurationExemplar(t *testing.T) {
	tp := sdktrace.NewTracerProvider()

	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "ims.db"), WithTracerProvider(tp))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	ctx, span := tp.Tracer("test").Start(context.Background(), "parent")
	defer span.End()

	if _, err := d.GetSettings(ctx); err != nil {
		t.Fatal(err)
	}

	var m dto.Metric
	if err := d.metrics.duration.WithLabelValues(poolRead).(prometheus.Metric).Write(&m); err != nil {
		t.Fatal(err)
	}

	exemplars := m.GetHistogram().GetExemplars()
	for _, b := range m.GetHistogram().GetBucket() {
		if e := b.GetExemplar(); e != nil {
			exemplars = append(exemplars, e)
		}
	}

	for _, e := range exemplars {
		for _, l := range e.GetLabel() {
			if l.GetName() == "trace_id" && l.GetValue() == span.SpanContext().TraceID().String() {
				return
			}
		}
	}

	t.Fatal("no exemplar with the caller's trace ID")
}
