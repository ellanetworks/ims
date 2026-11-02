package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsCountRequestsByRouteAndCode(t *testing.T) {
	store := openStore(t)
	cfg := testConfig(t, store)
	cfg.Requests = api.NewRequestMetrics()

	registry := prometheus.NewRegistry()
	registry.MustRegister(cfg.Requests.Collectors()...)
	cfg.Metrics = registry

	h := api.NewHandler(cfg)
	token := initialize(t, h)

	if code := serve(h, request(http.MethodGet, "/api/v1/users/me", "", token)).Code; code != http.StatusOK {
		t.Fatalf("GET /api/v1/users/me: %d", code)
	}

	if code := serve(h, request(http.MethodGet, "/api/v1/users/me", "", "")).Code; code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/users/me without token: %d", code)
	}

	if code := serve(h, request(http.MethodGet, "/api/v1/users/nobody@example.com", "", token)).Code; code != http.StatusNotFound {
		t.Fatalf("GET missing user: %d", code)
	}

	serve(h, request(http.MethodGet, "/api/v1/pprof/cmdline", "", token))

	rec := serve(h, request(http.MethodGet, "/api/v1/metrics", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics: %d", rec.Code)
	}

	body := rec.Body.String()

	for _, s := range []string{
		`ellaims_api_request_duration_seconds_count{code="201",method="post",route="/api/v1/init"} 1`,
		`ellaims_api_request_duration_seconds_count{code="200",method="get",route="/api/v1/users/me"} 1`,
		`ellaims_api_request_duration_seconds_count{code="401",method="get",route="/api/v1/users/me"} 1`,
		`ellaims_api_request_duration_seconds_count{code="404",method="get",route="/api/v1/users/{email}"} 1`,
	} {
		if !strings.Contains(body, "\n"+s+"\n") {
			t.Errorf("metrics do not have %q", s)
		}
	}

	if strings.Contains(body, `route="/api/v1/pprof/`) {
		t.Error("metrics have a pprof route")
	}
}
