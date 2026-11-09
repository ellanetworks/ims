package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/cluster"
)

func TestLivez(t *testing.T) {
	cfg := testConfig(t, openStore(t))
	cfg.Ready = func(context.Context, bool) []cluster.Check {
		return []cluster.Check{{Name: "database", Detail: "database is locked"}}
	}

	if rec := serve(api.NewHandler(cfg), request(http.MethodGet, "/api/v1/livez", "", "")); rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rec.Code)
	}
}

func readyz(t *testing.T, h http.Handler) (int, api.Readiness) {
	t.Helper()

	rec := serve(h, request(http.MethodGet, "/api/v1/readyz", "", ""))

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}

	var body struct {
		Result api.Readiness `json:"result"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}

	return rec.Code, body.Result
}

func TestReadyz(t *testing.T) {
	var checks []cluster.Check

	cfg := testConfig(t, openStore(t))
	cfg.Ready = func(context.Context, bool) []cluster.Check { return checks }
	h := api.NewHandler(cfg)

	checks = []cluster.Check{{Name: "shutdown", OK: true}, {Name: "database", OK: true}}

	code, res := readyz(t, h)
	if code != http.StatusOK || !res.Ready || len(res.Checks) != 2 {
		t.Fatalf("ready: code = %d, result = %+v", code, res)
	}

	checks = []cluster.Check{{Name: "shutdown", OK: true}, {Name: "database", Detail: "database is locked"}}

	code, res = readyz(t, h)
	if code != http.StatusServiceUnavailable || res.Ready {
		t.Fatalf("not ready: code = %d, result = %+v", code, res)
	}

	if len(res.Checks) != 2 || res.Checks[1] != checks[1] {
		t.Fatalf("not ready: checks = %+v, want %+v", res.Checks, checks)
	}
}

func TestReadyzWithoutChecks(t *testing.T) {
	cfg := testConfig(t, openStore(t))
	cfg.Ready = nil

	code, res := readyz(t, api.NewHandler(cfg))
	if code != http.StatusOK || !res.Ready || res.Checks == nil {
		t.Fatalf("code = %d, result = %+v", code, res)
	}
}
