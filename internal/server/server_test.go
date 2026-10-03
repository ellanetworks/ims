package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()

	return config.Config{
		DB:  config.DB{Path: filepath.Join(t.TempDir(), "ims.db")},
		API: config.API{Address: netip.MustParseAddr("127.0.0.1"), Port: 0},
		IMS: config.IMS{MCC: "001", MNC: "01", HomeDomain: imsRealm},
		SIP: config.SIP{Addresses: []netip.Addr{loopback, loopback6}},
		Diameter: diameterConfig(
			config.DiameterPeer{
				ID: "hss", Host: "hss.ims.mnc001.mcc001.3gppnetwork.org", Realm: imsRealm,
				Address: loopback, Port: unusedPort(t), Transport: config.TransportTCP,
				Applications: []config.Application{config.ApplicationCx, config.ApplicationRx},
			},
		),
	}
}

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
	srv := &Server{Config: testConfig(t), Logger: slog.New(slog.DiscardHandler), IPsec: ipsectest.NewKernel()}

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

func TestStartNeedsIPsec(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can install SAs")
	}

	srv := &Server{Config: testConfig(t), Logger: slog.New(slog.DiscardHandler)}

	err := srv.Start(t.Context())
	if err == nil {
		srv.Shutdown(context.Background())
		t.Fatal("Start succeeded without CAP_NET_ADMIN")
	}

	if !strings.Contains(err.Error(), "CAP_NET_ADMIN") {
		t.Fatalf("Start = %v, want an error naming CAP_NET_ADMIN", err)
	}
}
