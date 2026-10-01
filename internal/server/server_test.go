package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()

	return config.Config{
		DB:          config.DB{Path: filepath.Join(t.TempDir(), "ims.db")},
		CallHistory: config.CallHistory{Retention: 24 * time.Hour},
		API:         config.API{Address: netip.MustParseAddr("127.0.0.1"), Port: 0},
		IMS:         config.IMS{MCC: "001", MNC: "01", HomeDomain: imsRealm},
		SIP:         config.SIP{Addresses: []netip.Addr{loopback, loopback6}, Port: 0},
		Diameter: diameterConfig(
			config.DiameterPeer{
				ID: "hss", Host: "hss.ims.mnc001.mcc001.3gppnetwork.org", Realm: imsRealm,
				Address: loopback, Port: unusedPort(t), Transport: config.TransportTCP,
				Applications: []config.Application{config.ApplicationCx, config.ApplicationRx},
			},
		),
	}
}

// unusedPort returns a loopback TCP port nothing listens on, so the IMS keeps
// failing to reach the peer.
func unusedPort(t *testing.T) int {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	return port
}

func TestServerStartShutdown(t *testing.T) {
	srv := &Server{Config: testConfig(t), Logger: slog.New(slog.DiscardHandler)}

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

func TestServerPurgesCallHistoryAtStart(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)

	database, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	now := time.Now()

	for _, endedAt := range []time.Time{now.Add(-25 * time.Hour), now.Add(-time.Hour)} {
		if _, err := database.CreateCall(ctx, db.Call{
			CallID: "call", Caller: "tel:+15551230001", Callee: "tel:+15551230002",
			StartedAt: endedAt, EndedAt: endedAt, Outcome: db.OutcomeBusy, SIPStatus: 486, EndedBy: db.PartyCallee,
		}); err != nil {
			t.Fatalf("CreateCall: %v", err)
		}
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	srv := &Server{Config: cfg, Logger: slog.New(slog.DiscardHandler)}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	defer srv.Shutdown(ctx)

	calls, total, err := srv.database.ListCalls(ctx, db.CallFilter{}, 1, 10)
	if err != nil {
		t.Fatalf("ListCalls: %v", err)
	}

	if total != 1 || !calls[0].EndedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("calls after start = %+v", calls)
	}
}
