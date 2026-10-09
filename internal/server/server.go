package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/pcscf"
	"github.com/ellanetworks/ims/internal/scscf"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/ui"
	"github.com/ellanetworks/ims/version"
)

var ErrAlreadyStarted = errors.New("server: already started")

type Server struct {
	Config config.Config
	Logger *slog.Logger

	IPsec pcscf.Kernel

	DiameterHandshakeTimeout time.Duration

	// These change the S-CSCF's and P-CSCF's timers in tests. Zero keeps their defaults; ReauthInterval and
	// NoAnswerTimeout are off by default.
	MinExpires       time.Duration
	ReauthInterval   time.Duration
	ReauthExpires    time.Duration
	NoAnswerTimeout  time.Duration
	MediaLossTimeout time.Duration
	GroupNoAnswer    time.Duration
	// PruneInterval changes how often call records are pruned in tests. Zero keeps the default.
	PruneInterval time.Duration

	database *db.DB
	// records outlives the cores, so that it closes the records of the calls a restart loses.
	records     *callrecords.Recorder
	stopPrune   context.CancelFunc
	pruneDone   chan struct{}
	settings    *settings.Live
	apiServer   *http.Server
	apiListener net.Listener

	// The core is replaced when the operator's identity changes.
	core       atomic.Pointer[core]
	stopFollow context.CancelFunc
	followDone chan struct{}
}

// core is the part of the IMS that its settings build, apart from what it reads when it uses it: Diameter, the
// policy function and SIP.
type core struct {
	settings    settings.Settings
	node        *diameter.Node
	diameterLns []diameter.Listener
	policy      *policyFunction
	sip         *sipServer
}

func (s *Server) Start(ctx context.Context) error {
	if s.apiServer != nil {
		return ErrAlreadyStarted
	}

	if s.Logger == nil {
		s.Logger = slog.Default()
	}

	cfg := s.Config

	database, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		return err
	}

	initial, err := database.GetSettings(ctx)
	if err != nil {
		_ = database.Close()
		return err
	}

	live := settings.NewLive(database, initial, func(st settings.Settings) error {
		return checkSettings(context.Background(), cfg, st)
	})

	// The calls in progress when the IMS last stopped were lost with it.
	closed, err := database.CloseOpenCallRecords(ctx, nil)
	if err != nil {
		_ = database.Close()
		return err
	}

	if closed > 0 {
		s.Logger.Info("closed the call records of calls lost when the IMS stopped", slog.Int64("records", closed))
	}

	var apiLC net.ListenConfig

	apiLn, err := apiLC.Listen(ctx, "tcp", netip.AddrPortFrom(cfg.API.Address, uint16(cfg.API.Port)).String())
	if err != nil {
		_ = database.Close()
		return fmt.Errorf("listen for the API: %w", err)
	}

	s.database = database
	s.settings = live
	s.records = callrecords.New(callrecords.Config{Store: database, Logger: s.Logger})

	c, err := s.startCore(ctx, initial)
	if err != nil {
		s.records.Close()
		_ = apiLn.Close()
		_ = database.Close()

		return err
	}

	s.core.Store(c)

	view := coreView{s}

	s.apiListener = apiLn
	s.apiServer = &http.Server{
		Handler: api.NewHandler(api.Config{
			Settings:      live,
			Diameter:      view,
			SIP:           view,
			Registrations: view,
			Policy:        view,
			CallRecords:   database,
			Frontend:      ui.FS(),
			Metrics:       newMetrics(database),
			Logger:        s.Logger,
		}),
		ErrorLog:          slog.NewLogLogger(s.Logger.Handler(), slog.LevelWarn),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       time.Minute,
		WriteTimeout:      time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	go func() { _ = s.apiServer.Serve(apiLn) }()

	followCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.stopFollow, s.followDone = stop, make(chan struct{})

	go s.follow(followCtx)

	pruneCtx, stopPrune := context.WithCancel(context.WithoutCancel(ctx))
	s.stopPrune, s.pruneDone = stopPrune, make(chan struct{})

	go func() {
		defer close(s.pruneDone)

		callrecords.Prune(pruneCtx, callrecords.PruneConfig{
			Store: database, Retention: func() time.Duration { return live.Get().CallRecords.Retention() },
			Interval: s.PruneInterval, Logger: s.Logger,
		})
	}()

	v := version.Get()
	attrs := []any{slog.String("version", v.Version), slog.String("revision", v.Revision), slog.String("api", apiLn.Addr().String())}
	s.Logger.Info("ims started", append(attrs, c.attrs()...)...)

	return nil
}

// startCore starts Diameter, the policy function and SIP from their settings.
func (s *Server) startCore(ctx context.Context, st settings.Settings) (*core, error) {
	cfg := s.Config

	diameterLns, err := listenDiameter(ctx, cfg.Diameter, st.Peers)
	if err != nil {
		return nil, err
	}

	rtr := newRTRHandler(s.Logger)
	rxh := newRxHandler(s.Logger)

	node, err := newDiameterNode(cfg.Diameter, st, s.DiameterHandshakeTimeout, rtr, rxh, s.Logger)
	if err != nil {
		closeListeners(diameterLns)
		return nil, fmt.Errorf("start Diameter: %w", err)
	}

	for _, ln := range diameterLns {
		go func() {
			if err := node.Serve(ln); !errors.Is(err, diameter.ErrClosed) && !errors.Is(err, net.ErrClosed) {
				s.Logger.Warn("Diameter listener stopped", slog.String("address", ln.Addr().String()), slog.Any("error", err))
			}
		}()
	}

	pf, err := newPolicyFunction(ctx, cfg, st, node, s.Logger)
	if err != nil {
		_ = node.Shutdown(ctx)
		return nil, err
	}

	sipServer, err := startSIP(ctx, cfg, st, s.numbering, s.timers(), node, rtr, rxh, pf, s.database, s.records, s.IPsec,
		s.Logger)
	if err != nil {
		_ = pf.close(ctx)
		_ = node.Shutdown(ctx)

		return nil, fmt.Errorf("start SIP: %w", err)
	}

	pf.serve(s.Logger)

	return &core{settings: st, node: node, diameterLns: diameterLns, policy: pf, sip: sipServer}, nil
}

func (s *Server) numbering() scscf.Numbering {
	n := s.settings.Get().Operator.Numbering

	return scscf.Numbering{
		CountryCode:         n.CountryCode,
		NationalPrefix:      n.NationalPrefix,
		InternationalPrefix: n.InternationalPrefix,
	}
}

// follow replaces the core whenever the settings change what it is built from: the IMS's identity, its Diameter
// peers or its policy function. Other settings are read when used, and need no restart.
func (s *Server) follow(ctx context.Context) {
	defer close(s.followDone)

	backoff := minRetry

	for {
		changed := s.settings.Changed()

		var retry <-chan time.Time

		if st := s.settings.Get(); ctx.Err() == nil {
			current := s.coreSettings()

			switch {
			case current.SameCore(st):
			case s.core.Load() != nil && current.MovedPeers(st):
				s.movePeers(st)
			default:
				s.restartCore(ctx, st)
			}

			// A core that failed to build is retried, since what it failed on may come back, like a peer's
			// certificate files or the SCTP module.
			if s.core.Load() == nil {
				retry = time.After(backoff)
				backoff = min(2*backoff, maxRetry)
			} else {
				backoff = minRetry
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-retry:
		}
	}
}

const (
	minRetry = time.Second
	maxRetry = 30 * time.Second
)

// movePeers points the running Diameter node at peers' new addresses, without a restart.
func (s *Server) movePeers(st settings.Settings) {
	c := s.core.Load()

	if err := c.node.SetPeers(diameterPeers(st.Peers)); err != nil {
		s.Logger.Error("failed to move the Diameter peers", slog.Any("error", err))
		return
	}

	next := *c
	next.settings = st
	s.core.Store(&next)

	s.Logger.Info("moved the Diameter peers", slog.Int("diameter_peers", len(st.Peers)))
}

// coreSettings are the settings the current core was built from. Without a core, as after it failed to restart,
// they are none, so that the next change tries again.
func (s *Server) coreSettings() settings.Settings {
	if c := s.core.Load(); c != nil {
		return c.settings
	}

	return settings.Settings{}
}

// restartCore replaces the core with one built from st. Peers and UEs reconnect to it; registrations and security
// associations are restored from the database.
func (s *Server) restartCore(ctx context.Context, st settings.Settings) {
	s.Logger.Info("restarting for the new settings",
		slog.String("home_domain", st.Operator.HomeDomain()), slog.Int("diameter_peers", len(st.Peers)),
		slog.String("policy", string(st.Policy.Interface)))

	if c := s.core.Swap(nil); c != nil {
		c.shutdown(ctx, s.Logger)
	}

	// The calls in progress were lost with the core.
	s.records.CloseOpen()

	c, err := s.startCore(ctx, st)
	if err != nil {
		s.Logger.Error("failed to restart for the new settings", slog.Any("error", err))
		return
	}

	s.core.Store(c)
	s.Logger.Info("restarted for the new settings", c.attrs()...)
}

func (c *core) attrs() []any {
	sipAttrs := make([]string, 0, len(c.sip.Listeners()))
	for _, l := range c.sip.Listeners() {
		sipAttrs = append(sipAttrs, l.Role+" "+l.Address.String())
	}

	diameterAttrs := make([]string, 0, len(c.diameterLns))
	for _, ln := range c.diameterLns {
		diameterAttrs = append(diameterAttrs, ln.Addr().Network()+" "+ln.Addr().String())
	}

	attrs := []any{
		slog.String("home_domain", c.settings.Operator.HomeDomain()), slog.Any("sip", sipAttrs),
		slog.Any("diameter", diameterAttrs),
	}
	if a := c.policy.address(); a != "" {
		attrs = append(attrs, slog.String("n5_notify", a))
	}

	return attrs
}

func (c *core) shutdown(ctx context.Context, logger *slog.Logger) {
	if err := c.sip.Close(); err != nil {
		logger.Warn("failed to stop SIP cleanly", slog.Any("error", err))
	}

	if err := c.policy.close(ctx); err != nil {
		logger.Warn("failed to stop the N5 notification server cleanly", slog.Any("error", err))
	}

	if err := c.node.ShutdownWithCause(ctx, diameter.DisconnectCauseRebooting); err != nil {
		logger.Warn("failed to stop Diameter cleanly", slog.Any("error", err))
	}
}

func (s *Server) APIAddr() net.Addr {
	if s.apiListener == nil {
		return nil
	}

	return s.apiListener.Addr()
}

func (s *Server) Shutdown(ctx context.Context) {
	if s.apiServer == nil {
		return
	}

	s.Logger.Info("ims stopping")

	if err := s.apiServer.Shutdown(ctx); err != nil {
		s.Logger.Warn("failed to stop the API cleanly", slog.Any("error", err))
	}

	s.stopFollow()
	<-s.followDone

	if c := s.core.Swap(nil); c != nil {
		c.shutdown(ctx, s.Logger)
	}

	// The records of the calls in progress are saved as they are, and closed when the IMS starts again.
	s.records.Close()

	s.stopPrune()
	<-s.pruneDone

	if err := s.database.Close(); err != nil {
		s.Logger.Warn("failed to close the database", slog.Any("error", err))
	}
}

func (s *Server) timers() timers {
	return timers{
		minExpires:       s.MinExpires,
		reauthInterval:   s.ReauthInterval,
		reauthExpires:    s.ReauthExpires,
		noAnswer:         s.NoAnswerTimeout,
		mediaLossTimeout: s.MediaLossTimeout,
		groupNoAnswer:    s.GroupNoAnswer,
	}
}

// coreView serves the API from the current core. While the core restarts, there is none: the IMS reports no peers,
// listeners or policy function, and has no registrations.
type coreView struct {
	s *Server
}

func (v coreView) Identity() diameter.Identity {
	if c := v.s.core.Load(); c != nil {
		return c.node.Identity()
	}

	return diameter.Identity{}
}

func (v coreView) Peers() []diameter.PeerStatus {
	if c := v.s.core.Load(); c != nil {
		return c.node.Peers()
	}

	return nil
}

func (v coreView) Listeners() []api.SIPEndpoint {
	if c := v.s.core.Load(); c != nil {
		return c.sip.Listeners()
	}

	return nil
}

func (v coreView) Reauthenticate(ctx context.Context, impi string) error {
	if c := v.s.core.Load(); c != nil {
		return c.sip.Reauthenticate(ctx, impi)
	}

	return api.ErrUnavailable
}

func (v coreView) PolicyStatus() api.PolicyStatus {
	if c := v.s.core.Load(); c != nil {
		return c.policy.PolicyStatus()
	}

	return api.PolicyStatus{}
}
