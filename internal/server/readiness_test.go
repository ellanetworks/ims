package server

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ellanetworks/ims/internal/cluster"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type countingHandler struct {
	mu      sync.Mutex
	records []string
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.records = append(h.records, r.Message)

	return nil
}

func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *countingHandler) WithGroup(string) slog.Handler { return h }

func (h *countingHandler) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	n := 0

	for _, r := range h.records {
		if r == msg {
			n++
		}
	}

	return n
}

func TestReadinessChecks(t *testing.T) {
	ctx := context.Background()

	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	node, err := cluster.Open(ctx, cluster.Config{Dir: filepath.Join(t.TempDir(), "raft")}, d)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = node.Close() })

	var stopping bool

	r := &readiness{
		node: node, stopping: func() bool { return stopping }, metrics: newReadinessMetrics(),
		logger: slog.New(slog.DiscardHandler),
	}

	check := func(name string) (cluster.Check, bool) {
		for _, c := range r.checks(ctx, false) {
			if c.Name == name {
				return c, true
			}
		}

		return cluster.Check{}, false
	}

	if c, ok := check(CheckShutdown); !ok || !c.OK {
		t.Fatalf("shutdown check = %+v, %t; want it passing", c, ok)
	}

	if _, ok := check(cluster.CheckDatabase); !ok {
		t.Fatal("no database check")
	}

	stopping = true

	if c, _ := check(CheckShutdown); c.OK || c.Detail != "the IMS is shutting down" {
		t.Fatalf("shutdown check while stopping = %+v", c)
	}
}

func TestReadinessLogsTransitionsOnly(t *testing.T) {
	ctx := context.Background()
	logs := &countingHandler{}
	r := &readiness{metrics: newReadinessMetrics(), logger: slog.New(logs)}

	ok := []cluster.Check{{Name: CheckShutdown, OK: true}, {Name: cluster.CheckDatabase, OK: true}}
	failing := func(n int) []cluster.Check {
		return []cluster.Check{{Name: CheckShutdown, OK: true}, {Name: cluster.CheckDatabase, Detail: string(rune('a' + n))}}
	}

	r.observe(ctx, ok)
	r.observe(ctx, ok)

	for n := range 5 {
		r.observe(ctx, failing(n))
	}

	if v := testutil.ToFloat64(r.metrics.checks.WithLabelValues(cluster.CheckDatabase)); v != 0 {
		t.Fatalf("ellaims_ready_check{check=database} = %v", v)
	}

	r.observe(ctx, ok)

	if got := logs.count("not ready"); got != 1 {
		t.Fatalf("logged not ready %d times, want 1", got)
	}

	if got := logs.count("ready"); got != 1 {
		t.Fatalf("logged ready %d times, want 1", got)
	}

	for _, check := range []string{CheckShutdown, cluster.CheckDatabase} {
		if v := testutil.ToFloat64(r.metrics.checks.WithLabelValues(check)); v != 1 {
			t.Fatalf("ellaims_ready_check{check=%s} = %v", check, v)
		}
	}
}
