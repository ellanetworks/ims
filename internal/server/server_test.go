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
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/settings"
)

// testIMS is the configuration file of a test IMS, with the settings its database starts with.
type testIMS struct {
	config.Config

	Peers  []settings.Peer
	Policy settings.Policy
}

func testConfig(t *testing.T) testIMS {
	t.Helper()

	return testIMS{
		Config: config.Config{
			DB:       config.DB{Path: filepath.Join(t.TempDir(), "ims.db")},
			API:      config.API{Address: netip.MustParseAddr("127.0.0.1"), Port: 0},
			SIP:      config.SIP{Addresses: []netip.Addr{loopback, loopback6}},
			ICSCF:    config.ICSCF{Port: 5070},
			SCSCF:    config.SCSCF{Port: 5080},
			Diameter: config.Diameter{Address: loopback},
		},
		Peers: []settings.Peer{{
			ID: "hss", Host: "hss.ims.mnc001.mcc001.3gppnetwork.org", Realm: imsRealm,
			Address: loopback, Port: unusedPort(t), Transport: settings.TransportTCP,
			Applications: []settings.Application{settings.ApplicationCx, settings.ApplicationRx},
		}},
	}
}

// seeded seeds the database, and returns the configuration file of the IMS to start on it.
func (c testIMS) seeded(t *testing.T) config.Config {
	t.Helper()

	c.seed(t)

	return c.Config
}

// seed writes the peers and the policy into the database, before the IMS starts on it.
func (c testIMS) seed(t *testing.T) {
	t.Helper()

	d, err := db.Open(t.Context(), c.DB.Path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	for _, p := range c.Peers {
		if err := d.CreatePeer(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}

	if c.Policy.Interface != "" {
		if err := d.UpdatePolicy(t.Context(), c.Policy); err != nil {
			t.Fatal(err)
		}
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
	srv := &Server{Config: testConfig(t).seeded(t), Logger: slog.New(slog.DiscardHandler), IPsec: ipsectest.NewKernel()}

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

	srv := &Server{Config: testConfig(t).seeded(t), Logger: slog.New(slog.DiscardHandler)}

	err := srv.Start(t.Context())
	if err == nil {
		srv.Shutdown(context.Background())
		t.Fatal("Start succeeded without CAP_NET_ADMIN")
	}

	if !strings.Contains(err.Error(), "CAP_NET_ADMIN") {
		t.Fatalf("Start = %v, want an error naming CAP_NET_ADMIN", err)
	}
}
