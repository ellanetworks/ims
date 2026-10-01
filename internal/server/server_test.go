package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"testing"

	"github.com/ellanetworks/ims/internal/config"
)

func TestServerStartShutdown(t *testing.T) {
	srv := &Server{
		Config: config.Config{API: config.API{Address: netip.MustParseAddr("127.0.0.1"), Port: 0}},
		Logger: slog.New(slog.DiscardHandler),
	}

	ctx := context.Background()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(ctx)

	if err := srv.Start(ctx); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Start = %v, want %v", err, ErrAlreadyStarted)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+srv.APIAddr().String()+"/api/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
