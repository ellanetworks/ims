package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/prometheus/client_golang/prometheus/testutil/promlint"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func TestMetrics(t *testing.T) {
	srv := &Server{Config: testConfig(t).seeded(t), Logger: slog.New(slog.DiscardHandler), IPsec: ipsectest.NewKernel()}

	ctx := context.Background()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(ctx)

	body := scrape(t, "http://"+srv.APIAddr().String()+"/api/v1/metrics")

	problems, err := promlint.New(bytes.NewReader(body)).Lint()
	if err != nil {
		t.Fatalf("lint: %v", err)
	}

	for _, p := range problems {
		t.Errorf("lint: %s: %s", p.Metric, p.Text)
	}

	parser := expfmt.NewTextParser(model.UTF8Validation)

	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, name := range []string{
		"go_goroutines",
		"process_start_time_seconds",
		"ellaims_build_info",
		"ellaims_database_query_duration_seconds",
		"ellaims_database_query_errors_total",
		"ellaims_database_storage_bytes",
		"go_sql_wait_duration_seconds_total",
	} {
		if _, ok := families[name]; !ok {
			t.Errorf("%s is missing", name)
		}
	}

	// The IMS has read its settings at start, so the writer pool has timed a call.
	for _, m := range families["ellaims_database_query_duration_seconds"].GetMetric() {
		if m.GetLabel()[0].GetValue() == "write" && m.GetHistogram().GetSampleCount() == 0 {
			t.Error("no database call on the write pool was timed")
		}
	}

	if n := len(families["ellaims_database_storage_bytes"].GetMetric()); n != 2 {
		t.Errorf("ellaims_database_storage_bytes has %d series, want 2", n)
	}
}

func scrape(t *testing.T, url string) []byte {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return body
}
