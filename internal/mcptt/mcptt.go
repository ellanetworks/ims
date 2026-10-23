// Package mcptt is the built-in MCPTT application server of the IMS: the participating and controlling functions of
// one MCPTT system (TS 24.379), their settings and runtime state in the IMS's database, their media relay. Through the
// IMS, it is an AS like any other, which its I-CSCF and S-CSCF reach over SIP; in memory, on the network it shares
// with them, by the host name of the AS (Host), which the iFCs and PSIs of the HSS name it by.
package mcptt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/server"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
)

type Config struct {
	config.MCPTT

	// Addresses are the IMS's SIP addresses: the AS has its SIP port on each, and through the IMS takes requests from
	// them only, its I-CSCF's and S-CSCF's.
	Addresses []netip.Addr
	// Network is the in-memory network of the IMS, which the AS serves its addresses on through the IMS; in direct
	// mode, they are sockets.
	Network *transaction.Network
	// HomeDomain is the IMS's, which the host of the AS is under.
	HomeDomain string
	DB         *db.DB
	Logger     *slog.Logger
}

// Host is the host name of the AS of the IMS of homeDomain (identity.Host): the name of the AS in the iFCs, the host
// of its PSIs and of the session identities of its calls (TS 24.379 §4.5). Under the home domain, it is the IMS's
// own: no other server is reached by it.
func Host(homeDomain string) string { return identity.Host(homeDomain) }

// System are the PSIs of the functions of the AS (TS 24.379 Annex H), of its host: fixed, they are what the HSS
// routes to the AS, and what the clients default to.
func System(homeDomain string) settings.System {
	return settings.System{
		ParticipatingPSI: identity.ParticipatingPSI(homeDomain),
		ControllingPSI:   identity.ControllingPSI(homeDomain),
	}
}

// AS is the running MCPTT application server.
type AS struct {
	srv *server.Server
	// kill ends the lifetime of the server, of the DNS queries of its next hops (server.Prepare).
	kill     context.CancelFunc
	settings *settings.Live

	mu     sync.Mutex
	status Status
}

// Status is what the AS runs: its mode; its name and PSIs, which the HSS gives the IMS (Host, System); the addresses
// of its SIP, UDP and TCP, in memory through the IMS; and that of its floor control channels.
type Status struct {
	Mode             string
	Name             string
	ParticipatingPSI string
	ControllingPSI   string
	SIP              []string
	MediaControl     string
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

	if cfg.HomeDomain == "" {
		return nil, errors.New("start MCPTT: no home domain to name it under")
	}

	mode := registrar.ThroughIMS
	if cfg.Mode == config.MCPTTModeDirect {
		mode = registrar.Direct

		logger.Warn("MCPTT in direct mode: the AS is the registrar of its clients, without the IMS, for the lab and tests only")
	}

	live, err := NewSettings(ctx, cfg.DB, cfg.HomeDomain)
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
			Transaction: transaction.Config{Logger: logger, Network: cfg.Network},
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
	as.named(cfg.HomeDomain)

	for _, l := range locals {
		if mode == registrar.Direct {
			if l, err = srv.Agent().Layer().Listen(ctx, l); err != nil {
				return nil, fmt.Errorf("listen for MCPTT: %w", err)
			}
		} else if err := srv.Agent().Layer().Serve(l); err != nil {
			return nil, fmt.Errorf("serve MCPTT in memory: %w", err)
		}

		as.status.SIP = append(as.status.SIP, l.String())
	}

	srv.Resubscribe()

	logger.Info("MCPTT started", slog.String("mode", as.status.Mode), slog.Any("sip", as.status.SIP),
		slog.String("media_control", as.status.MediaControl))

	return as, nil
}

// Settings are the settings of the running AS, which persists and applies a change before it returns.
func (a *AS) Settings() *settings.Live { return a.settings }

func (a *AS) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.status
}

// Servers are the addresses of the SIP of the AS, which its host resolves to: in memory through the IMS.
func (a *AS) Servers() []sip.Server {
	a.mu.Lock()
	defer a.mu.Unlock()

	servers := make([]sip.Server, 0, len(a.status.SIP))

	for _, s := range a.status.SIP {
		if addr, err := netip.ParseAddrPort(s); err == nil {
			servers = append(servers, sip.Server{Transport: sip.UDP, Addr: addr})
		}
	}

	return servers
}

// SetHomeDomain renames the AS and its PSIs, for the new home domain of the IMS.
func (a *AS) SetHomeDomain(ctx context.Context, homeDomain string) error {
	if err := a.settings.UpdateSystem(ctx, System(homeDomain)); err != nil {
		return fmt.Errorf("rename MCPTT: %w", err)
	}

	a.named(homeDomain)

	return nil
}

func (a *AS) named(homeDomain string) {
	sys := System(homeDomain)

	a.mu.Lock()
	defer a.mu.Unlock()

	a.status.Name = "sip:" + Host(homeDomain)
	a.status.ParticipatingPSI, a.status.ControllingPSI = sys.ParticipatingPSI, sys.ControllingPSI
}

// Shutdown stops the AS until ctx is done: its calls end and its subscriptions go. The DNS queries of the BYEs and
// unsubscriptions run until then, and fail at once after it: the lifetime ends with the deadline of the shutdown,
// not at its start, which would leave the calls through a next hop named by a domain name without their BYE.
func (a *AS) Shutdown(ctx context.Context) error {
	defer a.kill()

	stop := context.AfterFunc(ctx, a.kill)
	defer stop()

	return a.srv.Shutdown(ctx)
}

// NewSettings are the settings of the AS as the database has them, persisted to it as they change, and its PSIs
// those of the home domain (System).
func NewSettings(ctx context.Context, d *db.DB, homeDomain string) (*settings.Live, error) {
	initial, err := d.GetMCPTTSettings(ctx)
	if err != nil {
		return nil, err
	}

	initial.System = System(homeDomain)

	live, err := settings.NewLive(store{d}, initial)
	if err != nil {
		return nil, fmt.Errorf("MCPTT settings: %w", err)
	}

	return live, nil
}
