package db

import (
	"context"
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
