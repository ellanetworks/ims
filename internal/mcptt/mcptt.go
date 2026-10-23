// Package mcptt is the built-in MCPTT application server of the IMS: the participating and controlling functions of
// one MCPTT system (TS 24.379), their settings and runtime state in the IMS's database, their media relay. Through the
// IMS, it is an AS like any other: its I-CSCF and S-CSCF reach it over SIP, on a port of its own.
package mcptt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/server"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

type Config struct {
	config.MCPTT

	// Addresses are the IMS's SIP addresses: the AS listens on each, and through the IMS takes requests from them
	// only, its I-CSCF's and S-CSCF's.
	Addresses []netip.Addr
	DB        *db.DB
	Logger    *slog.Logger
}

// AS is the running MCPTT application server.
type AS struct {
	srv *server.Server
	// kill ends the lifetime of the server, of the DNS queries of its next hops (server.Prepare).
	kill     context.CancelFunc
	settings *settings.Live
	status   Status
}

// Status is what the AS runs: its mode, the addresses its SIP listens on, UDP and TCP, and that of its floor control
// channels.
type Status struct {
	Mode         string
	SIP          []string
	MediaControl string
}

// Start starts the AS of cfg: its settings of the database, its media relay, its SIP. Through the IMS, it restores the
// registrations and affiliations it had when it stopped; in direct mode, its clients register again.
func Start(ctx context.Context, cfg Config) (_ *AS, err error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	logger = logger.With(slog.String("role", "mcptt"))

	if len(cfg.Addresses) == 0 {
		return nil, errors.New("start MCPTT: no SIP address to listen on")
	}

	mode := registrar.ThroughIMS
	if cfg.Mode == config.MCPTTModeDirect {
		mode = registrar.Direct

		logger.Warn("MCPTT in direct mode: the AS is the registrar of its clients, without the IMS, for the lab and tests only")
	}

	live, err := NewSettings(ctx, cfg.DB)
	if err != nil {
		return nil, err
	}

	locals := make([]netip.AddrPort, len(cfg.Addresses))
	trusted := make([]netip.Prefix, len(cfg.Addresses))

	for i, a := range cfg.Addresses {
		locals[i] = netip.AddrPortFrom(a, uint16(cfg.Port))
		trusted[i] = netip.PrefixFrom(a, a.BitLen())
	}

	m := cfg.Media

	relay, err := media.New(media.Config{
		Network: media.UDP{}, Addr: m.Address, Advertise: m.Advertise,
		MinPort: uint16(m.MinPort), MaxPort: uint16(m.MaxPort), ControlPort: uint16(m.ControlPort),
		Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("start the MCPTT media relay: %w", err)
	}

	life, kill := context.WithCancel(context.Background())

	srv, err := server.New(server.Config{
		UA: ua.Config{
			Transaction: transaction.Config{Logger: logger},
			Prepare:     server.Prepare(life, locals, &sip.Resolver{}),
			Contact:     sip.Address{URI: sip.URI{Scheme: "sip", Host: sip.FormatHost(locals[0].Addr()), Port: locals[0].Port()}},
			Logger:      logger,
		},
		Source:          live,
		Mode:            mode,
		TrustedNetworks: trusted,
		Media:           relay,
		State:           store{cfg.DB},
	})
	if err != nil {
		kill()
		return nil, errors.Join(err, relay.Close())
	}

	as := &AS{srv: srv, kill: kill, settings: live}

	defer func() {
		if err != nil {
			err = errors.Join(err, as.Shutdown(ctx))
		}
	}()

	// A change of the settings over the API takes effect on the registrations and affiliations at once.
	live.OnChange(srv.Revoke)

	if mode == registrar.Direct {
		if err := cfg.DB.ClearMCPTTState(ctx); err != nil {
			return nil, err
		}
	} else {
		regs, err := cfg.DB.MCPTTRegistrations(ctx)
		if err != nil {
			return nil, err
		}

		affs, err := cfg.DB.MCPTTAffiliations(ctx)
		if err != nil {
			return nil, err
		}

		srv.Restore(regs, affs)
	}

	as.status = Status{
		Mode:         string(cfg.Mode),
		MediaControl: netip.AddrPortFrom(m.Address, uint16(m.ControlPort)).String(),
	}

	for _, l := range locals {
		bound, err := srv.Agent().Layer().Listen(ctx, l)
		if err != nil {
			return nil, fmt.Errorf("listen for MCPTT: %w", err)
		}

		as.status.SIP = append(as.status.SIP, bound.String())
	}

	srv.Resubscribe()

	logger.Info("MCPTT started", slog.String("mode", as.status.Mode), slog.Any("sip", as.status.SIP),
		slog.String("media_control", as.status.MediaControl))

	return as, nil
}

// Settings are the settings of the running AS, which persists and applies a change before it returns.
func (a *AS) Settings() *settings.Live { return a.settings }

func (a *AS) Status() Status { return a.status }

// Shutdown stops the AS until ctx is done: its calls end and its subscriptions go. The DNS queries of the BYEs and
// unsubscriptions run until then, and fail at once after it: the lifetime ends with the deadline of the shutdown,
// not at its start, which would leave the calls through a next hop named by a domain name without their BYE.
func (a *AS) Shutdown(ctx context.Context) error {
	defer a.kill()

	stop := context.AfterFunc(ctx, a.kill)
	defer stop()

	return a.srv.Shutdown(ctx)
}

// NewSettings are the settings of the AS as the database has them, persisted to it as they change.
func NewSettings(ctx context.Context, d *db.DB) (*settings.Live, error) {
	initial, err := d.GetMCPTTSettings(ctx)
	if err != nil {
		return nil, err
	}

	live, err := settings.NewLive(store{d}, initial)
	if err != nil {
		return nil, fmt.Errorf("MCPTT settings: %w", err)
	}

	return live, nil
}
