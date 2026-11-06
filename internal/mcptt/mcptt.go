package mcptt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/ellanetworks/ims/internal/as"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/controlling"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/server"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/mcptt/identity"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/ua"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	config.MCPTT

	Addresses      []netip.Addr
	Network        *transaction.Network
	HomeDomain     string
	DB             *db.DB
	Logger         *slog.Logger
	TracerProvider trace.TracerProvider
}

func Host(homeDomain string) string { return identity.Host(homeDomain) }

// TS 24.379 annex H
func System(homeDomain string) settings.System {
	return settings.System{
		ParticipatingPSI: identity.ParticipatingPSI(homeDomain),
		ControllingPSI:   identity.ControllingPSI(homeDomain),
	}
}

type AS struct {
	srv      *server.Server
	kill     context.CancelFunc
	settings *settings.Live

	mu     sync.Mutex
	status Status
}

type Status struct {
	Mode             string
	Name             string
	ParticipatingPSI string
	ControllingPSI   string
	SIP              []string
	MediaControl     string
}

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

		logger.WarnContext(ctx, "MCPTT in direct mode: the AS is the registrar of its clients, without the IMS, for the lab and tests only")
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
	role := []attribute.KeyValue{tracing.RoleKey.String("mcptt")}

	srv, err := server.New(server.Config{
		UA: ua.Config{
			Transaction: transaction.Config{
				Logger: logger, Network: cfg.Network, TracerProvider: cfg.TracerProvider,
				SpanAttributes: func(*sip.Request, trace.SpanKind) []attribute.KeyValue { return role },
			},
			Prepare: as.Prepare(life, locals, &sip.Resolver{}),
			Contact: sip.Address{URI: sip.URI{Scheme: "sip", Host: sip.FormatHost(locals[0].Addr()), Port: locals[0].Port()}},
			Logger:  logger,
		},
		Source:          live,
		Mode:            mode,
		TrustedNetworks: trusted,
		HomeDomain:      cfg.HomeDomain,
		Media:           relay,
		State:           store{d: cfg.DB, home: cfg.HomeDomain},
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

		srv.Restore(ctx, regs, affs)
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

	logger.InfoContext(ctx, "MCPTT started", slog.String("mode", as.status.Mode), slog.Any("sip", as.status.SIP),
		slog.String("media_control", as.status.MediaControl))

	return as, nil
}

func (a *AS) Clients() []registrar.Binding { return a.srv.Registrar().Clients() }

func (a *AS) Affiliations() []affiliation.Affiliations {
	return a.srv.Participating().Snapshot(a.srv.Now())
}

func (a *AS) Calls(ctx context.Context) ([]controlling.Call, error) { return a.srv.Calls().Calls(ctx) }

func (a *AS) Call(ctx context.Context, id string) (controlling.Call, bool, error) {
	return a.srv.Calls().Call(ctx, id)
}

func (a *AS) Settings() *settings.Live { return a.settings }

func (a *AS) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.status
}

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

func (a *AS) Resubscribe(ctx context.Context) { a.srv.Resubscribe(ctx) }

func (a *AS) named(homeDomain string) {
	sys := System(homeDomain)

	a.mu.Lock()
	defer a.mu.Unlock()

	a.status.Name = "sip:" + Host(homeDomain)
	a.status.ParticipatingPSI, a.status.ControllingPSI = sys.ParticipatingPSI, sys.ControllingPSI
}

func (a *AS) Shutdown(ctx context.Context) error {
	defer a.kill()

	stop := context.AfterFunc(ctx, a.kill)
	defer stop()

	return a.srv.Shutdown(ctx)
}

func NewSettings(ctx context.Context, d *db.DB, homeDomain string) (*settings.Live, error) {
	initial, err := d.GetMCPTTSettings(ctx)
	if err != nil {
		return nil, err
	}

	initial.System = System(homeDomain)

	if initial.ServiceConfiguration.Domain == "" {
		initial.ServiceConfiguration.Domain = homeDomain
	}

	live, err := settings.NewLive(store{d: d, home: homeDomain}, initial)
	if err != nil {
		return nil, fmt.Errorf("MCPTT settings: %w", err)
	}

	return live, nil
}
