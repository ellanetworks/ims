package mcptt

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

var loopback = netip.MustParseAddr("127.0.0.1")

// freePort is a UDP port of loopback free a moment ago.
func freePort(t testing.TB) int {
	t.Helper()

	c, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(loopback, 0)))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = c.Close() }()

	return c.LocalAddr().(*net.UDPAddr).Port
}

// openDB is a new database of the IMS, closed at the end of the test.
func openDB(t testing.TB, dir string) *db.DB {
	t.Helper()

	d, err := db.Open(context.Background(), filepath.Join(dir, "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	return d
}

// directConfig is the AS in direct mode on loopback, of free ports, its settings in d.
func directConfig(t *testing.T, d *db.DB) Config {
	t.Helper()

	return Config{
		MCPTT: config.MCPTT{
			Mode: config.MCPTTModeDirect, Port: freePort(t),
			Media: config.MCPTTMedia{Address: loopback, MinPort: 20000, MaxPort: 20999, ControlPort: freePort(t)},
		},
		Addresses: []netip.Addr{loopback},
		DB:        d,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// startExample starts the AS of cfg, provisioned with the example settings if its database has none.
func startExample(t *testing.T, cfg Config) *AS {
	t.Helper()

	as, err := Start(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	if len(as.Settings().Get().Users) > 0 {
		return as
	}

	if err := provision(t.Context(), as.Settings(), exampleSettings()); err != nil {
		t.Fatal(err)
	}

	return as
}

// The AS of the example settings, in direct mode on loopback, registers a client over UDP, and stops.
func TestStartRegister(t *testing.T) {
	cfg := directConfig(t, openDB(t, t.TempDir()))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := startExample(t, cfg)

	server := netip.AddrPortFrom(loopback, uint16(cfg.Port))
	if sips := as.Status().SIP; len(sips) != 1 || sips[0] != server.String() {
		t.Fatalf("listening on %v", sips)
	}

	local := netip.AddrPortFrom(loopback, uint16(freePort(t)))

	agent, err := ua.New(ua.Config{
		Transaction: transaction.Config{Logger: logger},
		Logger:      logger,
		Prepare: func(req *sip.Request) error {
			req.Flow = sip.Flow{Transport: sip.UDP, Local: local, Remote: server}
			req.Header.Prepend("Via", sip.NewVia(sip.UDP, local).String())

			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := agent.Layer().Listen(context.Background(), local); err != nil {
		t.Fatal(err)
	}

	id, impu := mustURI(t, "sip:alice@mcptt.example.com"), mustURI(t, "sip:alice@ims.example.com")

	c, err := client.New(client.Config{
		Agent: agent, IMPU: impu, MCPTTID: id, ClientID: "urn:uuid:00000000-0000-1000-8000-000000000001",
		Contact:   client.FixedContact(sip.Address{URI: sip.URI{Scheme: "sip", Host: local.Addr().String(), Port: local.Port()}}),
		Registrar: mustURI(t, "sip:mcptt.example.com"), ParticipatingPSI: as.Settings().Current().ParticipatingPSI(),
		Media: media.UDP{}, Addr: loopback, MinPort: 40000, MaxPort: 40003,
		Floor: floor.ParticipantTimers{T100: time.Second, T101: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}

	codes := make(chan int, 1)

	if err := c.Register(time.Hour, func(res *sip.Response, err error) {
		if err != nil {
			t.Error(err)

			codes <- 0

			return
		}

		codes <- res.StatusCode
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-codes:
		if code != 200 {
			t.Errorf("REGISTER: %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Error("no response to the REGISTER")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := agent.Shutdown(ctx); err != nil {
		t.Error(err)
	}

	if err := as.Shutdown(ctx); err != nil {
		t.Error(err)
	}
}

// Without SIP addresses, the AS does not start.
func TestStartNoSIP(t *testing.T) {
	cfg := directConfig(t, openDB(t, t.TempDir()))
	cfg.Addresses = nil

	if _, err := Start(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "no SIP address") {
		t.Errorf("Start: %v", err)
	}
}

// The settings changed are those of the AS after a restart; settings in the database that are not valid, which
// only a change of the database outside the API makes, stop the start.
func TestRestart(t *testing.T) {
	d := openDB(t, t.TempDir())
	cfg := directConfig(t, d)
	as := startExample(t, cfg)

	if _, err := as.Settings().CreateUser(t.Context(), settings.User{
		MCPTTUserID: "sip:dave@mcptt.example.com", IMPUs: []string{"sip:dave@ims.example.com"},
	}); err != nil {
		t.Fatal(err)
	}

	want := as.Settings().Get()

	if err := as.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	as = startExample(t, cfg)

	if got := as.Settings().Get(); !reflect.DeepEqual(got, want) || len(got.Users) != 4 {
		t.Errorf("settings after a restart\n%+v\nwant\n%+v", got, want)
	}

	if as.Settings().Current().User(mustURI(t, "sip:dave@mcptt.example.com")) == nil {
		t.Error("dave not served")
	}

	if err := d.UpdateMCPTTServiceConfiguration(t.Context(), settings.ServiceConfiguration{Domain: "mcptt.example.com"}); err != nil {
		t.Fatal(err)
	}

	if err := as.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := Start(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "T7-floor-idle: is required") {
		t.Errorf("start of invalid settings: %v", err)
	}
}

func mustURI(t testing.TB, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}
