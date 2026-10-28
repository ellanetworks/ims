package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ellanetworks/ims/internal/api"
)

func TestLivez(t *testing.T) {
	cfg := testConfig(t, openStore(t))
	cfg.Ready = func(context.Context) error { return errors.New("database is locked") }

	// Without a token, and while not ready.
	if rec := serve(api.NewHandler(cfg), request(http.MethodGet, "/api/v1/livez", "", "")); rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	var notReady error

	cfg := testConfig(t, openStore(t))
	cfg.Ready = func(context.Context) error { return notReady }
	h := api.NewHandler(cfg)

	// Without a token, and before the first user exists.
	if rec := serve(h, request(http.MethodGet, "/api/v1/readyz", "", "")); rec.Code != http.StatusNoContent {
		t.Fatalf("ready: code = %d", rec.Code)
	}

	notReady = errors.New("database is locked")

	rec := serve(h, request(http.MethodGet, "/api/v1/readyz", "", ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("not ready: code = %d", rec.Code)
	}

	if got, want := rec.Body.String(), `{"error":"Not ready"}`; got != want {
		t.Fatalf("not ready: body = %s, want %s", got, want)
	}
}
