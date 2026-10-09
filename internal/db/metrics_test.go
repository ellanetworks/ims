package db

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsCountFailuresOnly(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if _, err := d.GetSettings(ctx); err != nil {
		t.Fatal(err)
	}

	// A record that is not there is an answer, not a failure.
	if err := d.DeletePeer(ctx, "missing"); err == nil {
		t.Fatal("DeletePeer of a missing peer succeeded")
	}

	if _, err := d.GetCallRecord(ctx, 1); err == nil {
		t.Fatal("GetCallRecord of a missing record succeeded")
	}

	if n := testutil.CollectAndCount(d.metrics.duration); n != 2 {
		t.Fatalf("duration has %d series, want 2", n)
	}

	for pool, want := range map[string]float64{poolWrite: 0, poolRead: 0} {
		if got := testutil.ToFloat64(d.metrics.errors.WithLabelValues(pool)); got != want {
			t.Errorf("errors{pool=%q} = %v, want %v", pool, got, want)
		}
	}

	// A call on a closed database fails.
	if err := d.conn.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := d.GetSettings(ctx); err == nil {
		t.Fatal("GetSettings on a closed database succeeded")
	}

	if got := testutil.ToFloat64(d.metrics.errors.WithLabelValues(poolWrite)); got != 1 {
		t.Errorf("errors{pool=%q} = %v, want 1", poolWrite, got)
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

// TestEveryCallIsObserved checks that each exported method of DB that calls the database starts with
// defer d.observe(...)(), so that a new one cannot be left out of the metrics.
func TestEveryCallIsObserved(t *testing.T) {
	unobserved := map[string]bool{"Close": true, "Collectors": true}

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
				t.Errorf("%s: DB.%s does not start with defer d.observe(...)()", fset.Position(fn.Pos()), fn.Name.Name)
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
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return false
	}

	d, ok := fn.Body.List[0].(*ast.DeferStmt)
	if !ok {
		return false
	}

	call, ok := d.Call.Fun.(*ast.CallExpr)
	if !ok {
		return false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)

	return ok && sel.Sel.Name == "observe"
}
