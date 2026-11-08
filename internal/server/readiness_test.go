package server

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/ellanetworks/ims/internal/api"
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

	var (
		stopping bool
		pingErr  error
	)

	r := &readiness{
		ping: func(context.Context) error { return pingErr }, stopping: func() bool { return stopping },
		metrics: newReadinessMetrics(), logger: slog.New(slog.DiscardHandler),
	}

	want := []api.Check{{Name: CheckShutdown, OK: true}, {Name: CheckDatabase, OK: true}}
	if got := r.checks(ctx); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ready: checks = %+v, want %+v", got, want)
	}

	stopping, pingErr = true, errors.New("database is locked")

	want = []api.Check{
		{Name: CheckShutdown, Detail: "the IMS is shutting down"}, {Name: CheckDatabase, Detail: "database is locked"},
	}
	if got := r.checks(ctx); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("not ready: checks = %+v, want %+v", got, want)
	}
}

func TestReadinessLogsTransitionsOnly(t *testing.T) {
	ctx := context.Background()
	logs := &countingHandler{}
	r := &readiness{metrics: newReadinessMetrics(), logger: slog.New(logs)}

	ok := []api.Check{{Name: CheckShutdown, OK: true}, {Name: CheckDatabase, OK: true}}
	failing := func(n int) []api.Check {
		return []api.Check{{Name: CheckShutdown, OK: true}, {Name: CheckDatabase, Detail: string(rune('a' + n))}}
	}

	r.observe(ctx, ok)
	r.observe(ctx, ok)

	for n := range 5 {
		r.observe(ctx, failing(n))
	}

	if v := testutil.ToFloat64(r.metrics.checks.WithLabelValues(CheckDatabase)); v != 0 {
		t.Fatalf("ellaims_ready_check{check=database} = %v", v)
	}

	r.observe(ctx, ok)

	if got := logs.count("not ready"); got != 1 {
		t.Fatalf("logged not ready %d times, want 1", got)
	}

	if got := logs.count("ready"); got != 1 {
		t.Fatalf("logged ready %d times, want 1", got)
	}

	for _, check := range []string{CheckShutdown, CheckDatabase} {
		if v := testutil.ToFloat64(r.metrics.checks.WithLabelValues(check)); v != 1 {
			t.Fatalf("ellaims_ready_check{check=%s} = %v", check, v)
		}
	}
}
