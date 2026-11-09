package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/api/apitest"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/settings"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/crypto/bcrypt"
)

type testIMS struct {
	config.Config

	Peers          []settings.Peer
	Routes         []settings.Route
	SMSRoutes      []settings.SMSRoute
	Policy         settings.Policy
	TracerProvider trace.TracerProvider
}

func testConfig(t *testing.T) testIMS {
	t.Helper()

	return testIMS{
		Config: config.Config{
			DB:       config.DB{Path: filepath.Join(t.TempDir(), "ims.db")},
			API:      config.API{Address: netip.MustParseAddr("127.0.0.1"), Port: 0},
			SIP:      config.SIP{Addresses: []netip.Addr{loopback, loopback6}},
			Diameter: config.Diameter{Address: loopback, HostLabel: settings.DefaultDiameterLabel},
		},
		Peers: []settings.Peer{{
			ID: "hss", Host: "hss.ims.mnc001.mcc001.3gppnetwork.org",
			Address: loopback.String(), Port: unusedPort(t), Transport: settings.TransportTCP,
			Applications: []settings.Application{settings.ApplicationCx, settings.ApplicationRx},
		}},
	}
}

func (c testIMS) seeded(t *testing.T) config.Config {
	t.Helper()

	c.seed(t)

	return c.Config
}

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

	for _, r := range c.Routes {
		if err := d.UpdateRoute(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}

	for _, r := range c.SMSRoutes {
		if err := d.CreateSMSRoute(t.Context(), r); err != nil {
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

	for range 100 {
		pc, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}

		port := pc.LocalAddr().(*net.UDPAddr).Port
		free := freeOn(t.Context(), &lc, "tcp", "127.0.0.1", port) && freeOn(t.Context(), &lc, "udp", "::1", port) &&
			freeOn(t.Context(), &lc, "tcp", "::1", port)

		_ = pc.Close()

		if free {
			return port
		}
	}

	t.Fatal("no port free for UDP and TCP")

	return 0
}

func freeOn(ctx context.Context, lc *net.ListenConfig, network, host string, port int) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	var (
		c   io.Closer
		err error
	)

	if network == "udp" {
		c, err = lc.ListenPacket(ctx, network, addr)
	} else {
		c, err = lc.Listen(ctx, network, addr)
	}

	if err != nil {
		return false
	}

	_ = c.Close()

	return true
}

func TestServerStartShutdown(t *testing.T) {
	srv := &Server{Config: testConfig(t).seeded(t), Logger: slog.New(slog.DiscardHandler), BcryptCost: bcrypt.MinCost, IPsec: ipsectest.NewKernel()}

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

	resp, err := apitest.Client(srv.APIAddr().String()).Do(req)
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	for path, want := range map[string]int{"/api/v1/livez": http.StatusNoContent, "/api/v1/readyz": http.StatusOK} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+srv.APIAddr().String()+path, nil)
		if err != nil {
			t.Fatal(err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}

		_ = resp.Body.Close()

		if resp.StatusCode != want {
			t.Fatalf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}

	srv.draining.Store(true)

	if checks := srv.readiness.checks(ctx, false); checks[0].Name != CheckShutdown || checks[0].OK {
		t.Fatalf("checks while shutting down = %+v", checks)
	}
}

func TestStartNeedsIPsec(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can install SAs")
	}

	srv := &Server{Config: testConfig(t).seeded(t), Logger: slog.New(slog.DiscardHandler), BcryptCost: bcrypt.MinCost}

	err := srv.Start(t.Context())
	if err == nil {
		srv.Shutdown(context.Background())
		t.Fatal("Start succeeded without CAP_NET_ADMIN")
	}

	if !strings.Contains(err.Error(), "CAP_NET_ADMIN") {
		t.Fatalf("Start = %v, want an error naming CAP_NET_ADMIN", err)
	}
}
