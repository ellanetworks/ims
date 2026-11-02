package server

import (
	"context"
	"crypto/tls"
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
	"github.com/ellanetworks/ims/internal/as"
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsmgw"
	"github.com/ellanetworks/ims/internal/mcptt"
	"github.com/ellanetworks/ims/internal/pcscf"
	"github.com/ellanetworks/ims/internal/scscf"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/ui"
	"github.com/ellanetworks/ims/version"
	"go.opentelemetry.io/otel/trace"
)

var ErrAlreadyStarted = errors.New("server: already started")

const (
	tracerName = "github.com/ellanetworks/ims/internal/server"
	roleIMS    = "ims"
)

const (
	ShutdownTimeout = mcpttStopTime + pcscf.DefaultPolicyTimeout + diameterDisconnectTime + recordsFlushTime +
		tracesFlushTime + time.Second

	mcpttStopTime = 5 * time.Second

	// RFC 6733 section 5.4
	diameterDisconnectTime = 2 * time.Second
	recordsFlushTime       = 2 * time.Second
	tracesFlushTime        = 2 * time.Second
)

type Server struct {
	Config config.Config
	Logger *slog.Logger

	IPsec pcscf.Kernel

	DiameterHandshakeTimeout time.Duration

	MinExpires       time.Duration
	ReauthInterval   time.Duration
	ReauthExpires    time.Duration
	NoAnswerTimeout  time.Duration
	MediaLossTimeout time.Duration
	GroupNoAnswer    time.Duration
	ASTimeout        time.Duration
	PruneInterval    time.Duration
	BcryptCost       int
	TracerProvider   trace.TracerProvider

	tracerProvider  trace.TracerProvider
	shutdownTracing func(context.Context) error
	detached        tracing.Detached

	database    *db.DB
	draining    atomic.Bool
	records     *callrecords.Recorder
	metrics     *metrics
	stopPrune   context.CancelFunc
	pruneDone   chan struct{}
	settings    *settings.Live
	apiServer   *http.Server
	apiListener net.Listener

	mcptt           atomic.Pointer[mcptt.AS]
	ases            builtinASs
	mcpttHomeDomain string

	ipsmgw           atomic.Pointer[ipsmgw.AS]
	ipsmgwHomeDomain string

	core       atomic.Pointer[core]
	stopFollow context.CancelFunc
	followDone chan struct{}

	sipFailed chan struct{}
}

type core struct {
	settings    settings.Settings
	node        *diameter.Node
	diameterLns []diameter.Listener
	realms      *realms
	policy      *policyFunction
	sip         *sipServer

	failed *atomic.Bool
}

func (s *Server) Start(ctx context.Context) error {
	if s.apiServer != nil {
		return ErrAlreadyStarted
	}

	if s.Logger == nil {
		s.Logger = slog.Default()
	}

	cfg := s.Config

	tlsConfig, err := apiTLS(cfg.API.TLS)
	if err != nil {
		return err
	}

	if err := s.startTracing(ctx); err != nil {
		return err
	}

	s.detached = tracing.NewDetached(s.tracerProvider, tracerName, roleIMS)

	database, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		s.stopTracing(ctx)
		return err
	}

	initial, err := database.GetSettings(ctx)
	if err != nil {
		_ = database.Close()

		s.stopTracing(ctx)

		return err
	}

	jwtSecret, err := database.JWTSecret(ctx)
	if err != nil {
		_ = database.Close()

		s.stopTracing(ctx)

		return err
	}

	live := settings.NewLive(database, initial, func(ctx context.Context, st settings.Settings) error {
		return checkSettings(ctx, cfg, st)
	})

	closed, err := database.CloseOpenCallRecords(ctx, nil)
	if err != nil {
		_ = database.Close()

		s.stopTracing(ctx)

		return err
	}

	if closed > 0 {
		s.Logger.InfoContext(ctx, "closed the call records of calls lost when the IMS stopped", slog.Int64("records", closed))
	}

	var apiLC net.ListenConfig

	apiLn, err := apiLC.Listen(ctx, "tcp", netip.AddrPortFrom(cfg.API.Address, uint16(cfg.API.Port)).String())
	if err != nil {
		_ = database.Close()

		s.stopTracing(ctx)

		return fmt.Errorf("listen for the API: %w", err)
	}

	s.database = database
	s.settings = live
	s.sipFailed = make(chan struct{}, 1)
	s.records = callrecords.New(callrecords.Config{Store: database, Logger: s.Logger})
	s.metrics = newMetrics(database, s.records, func() ([]settings.Peer, []diameter.PeerStatus) {
		return live.Get().Peers, coreView{s}.Peers()
	})

	s.ases = builtinASs{network: transaction.NewNetwork(), resolver: &as.Resolver{DNS: &sip.Resolver{}}}

	if cfg.MCPTT != nil {
		if err := s.startMCPTT(ctx, initial.Operator.HomeDomain()); err != nil {
			_ = s.records.Close(ctx)
			_ = apiLn.Close()
			_ = database.Close()

			s.stopTracing(ctx)

			return err
		}
	}

	if err := s.startIPSMGW(ctx, initial.Operator.HomeDomain()); err != nil {
		s.stopMCPTT(ctx)

		_ = s.records.Close(ctx)
		_ = apiLn.Close()
		_ = database.Close()

		s.stopTracing(ctx)

		return err
	}

	c, err := s.startCore(ctx, initial)
	if err != nil {
		s.stopIPSMGW(ctx)
		s.stopMCPTT(ctx)

		_ = s.records.Close(ctx)
		_ = apiLn.Close()
		_ = database.Close()

		s.stopTracing(ctx)

		return err
	}

	s.core.Store(c)
	s.resubscribeMCPTT(ctx)
	s.resubscribeIPSMGW(ctx)

	view := coreView{s}

	apiCfg := api.Config{
		Settings:       live,
		Diameter:       view,
		SIP:            view,
		Registrations:  view,
		Policy:         view,
		CallRecords:    database,
		Frontend:       ui.FS(),
		Ready:          func(ctx context.Context) error { return s.ready(ctx, database) },
		Metrics:        s.metrics.registry,
		Auth:           database,
		JWTSecret:      jwtSecret,
		BcryptCost:     s.BcryptCost,
		SecureCookie:   tlsConfig != nil,
		TracerProvider: s.tracerProvider,
		Now:            time.Now,
		Logger:         s.Logger,
	}

	if cfg.MCPTT != nil {
		apiCfg.MCPTT = mcpttView{s}
		apiCfg.MCPTTState = mcpttView{s}
		apiCfg.MCPTTStatus = mcpttView{s}.Status
	}

	s.apiListener = apiLn
	s.apiServer = &http.Server{
		Handler:           api.NewHandler(apiCfg),
		ErrorLog:          slog.NewLogLogger(s.Logger.Handler(), slog.LevelWarn),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       time.Minute,
		WriteTimeout:      time.Minute,
		IdleTimeout:       2 * time.Minute,
		TLSConfig:         tlsConfig,
	}

	if tlsConfig != nil {
		go func() { _ = s.apiServer.ServeTLS(apiLn, "", "") }()
	} else {
		go func() { _ = s.apiServer.Serve(apiLn) }()
	}

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
	attrs := []any{
		slog.String("version", v.Version), slog.String("revision", v.Revision), slog.String("api", apiLn.Addr().String()),
		slog.Bool("tls", tlsConfig != nil),
	}
	s.Logger.InfoContext(ctx, "ims started", append(attrs, c.attrs()...)...)

	return nil
}

func (s *Server) startCore(ctx context.Context, st settings.Settings) (*core, error) {
	cfg := s.Config

	diameterLns, err := listenDiameter(ctx, cfg.Diameter, st)
	if err != nil {
		return nil, err
	}

	cxh := newCxHandler(s.Logger)
	rxh := newRxHandler(s.Logger)

	node, err := newDiameterNode(cfg.Diameter, st, s.DiameterHandshakeTimeout, cxh, rxh, ipsmgwHandler{s.ipsmgw.Load},
		s.tracerProvider, s.Logger)
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

	rlm := newRealms(st)

	pf, err := newPolicyFunction(ctx, cfg, st, node, rlm, s.metrics, s.tracerProvider, s.Logger)
	if err != nil {
		_ = node.Shutdown(ctx)
		return nil, err
	}

	failed := new(atomic.Bool)

	sipFailed := func(netip.AddrPort, error) {
		failed.Store(true)

		select {
		case s.sipFailed <- struct{}{}:
		default:
		}
	}

	sipServer, err := startSIP(ctx, cfg, st, s.numbering, s.timers(), node, rlm, cxh, rxh, pf, s.database, s.records,
		s.metrics, s.IPsec, sipFailed, s.ases, s.tracerProvider, s.Logger)
	if err != nil {
		_ = pf.close(ctx)
		_ = node.Shutdown(ctx)

		return nil, fmt.Errorf("start SIP: %w", err)
	}

	pf.serve(s.Logger)

	return &core{settings: st, node: node, diameterLns: diameterLns, realms: rlm, policy: pf, sip: sipServer, failed: failed}, nil
}

func (s *Server) numbering() scscf.Numbering {
	n := s.settings.Get().Operator.Numbering

	return scscf.Numbering{
		CountryCode:         n.CountryCode,
		NationalPrefix:      n.NationalPrefix,
		InternationalPrefix: n.InternationalPrefix,
	}
}

func (s *Server) follow(ctx context.Context) {
	defer close(s.followDone)

	backoff := minRetry
	number := s.settings.Get().IPSMGW.Number

	var causes []trace.SpanContext

	for {
		changed := s.settings.Changed()

		st, taken := s.settings.Take()
		causes = append(causes, taken...)
		causes = causes[max(0, len(causes)-maxCauses):]

		var retry <-chan time.Time

		if ctx.Err() == nil {
			s.apply(ctx, st, number != st.IPSMGW.Number, causes)
			number = st.IPSMGW.Number

			if s.core.Load() == nil {
				retry = time.After(backoff)
				backoff = min(2*backoff, maxRetry)
			} else {
				backoff = minRetry
				causes = nil
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-retry:
		case <-s.sipFailed:
		}
	}
}

func (s *Server) apply(ctx context.Context, st settings.Settings, numberChanged bool, causes []trace.SpanContext) {
	current := s.coreSettings()
	c := s.core.Load()
	failed := c != nil && c.failed.Load()

	if !numberChanged && !failed && current.SameCore(st) {
		return
	}

	name := "apply settings"
	if failed {
		name = "restart core"
	}

	ctx, span := s.detached.Start(ctx, name, causes...)

	var err error

	defer func() { tracing.End(span, err) }()

	if numberChanged {
		s.ipsmgwNumberChanged(ctx)
	}

	switch {
	case failed:
		err = s.restartCore(ctx, st, "for a failed SIP listener")
	case current.SameCore(st):
	case c != nil && current.SameNode(st):
		err = s.reconfigureNode(ctx, st)
	default:
		err = s.restartCore(ctx, st, "for the new settings")
	}
}

const maxCauses = 32

const (
	minRetry = time.Second
	maxRetry = 30 * time.Second
)

func (s *Server) reconfigureNode(ctx context.Context, st settings.Settings) error {
	c := s.core.Load()

	err := c.node.SetPeers(diameterPeers(st, c.settings))
	if err == nil {
		c.realms.set(st)
		err = c.node.SetPeers(diameterPeers(st))
	}

	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to reconfigure the Diameter peers", slog.Any("error", err))
		return s.restartCore(ctx, st, "for the new settings")
	}

	next := *c
	next.settings = st
	s.core.Store(&next)

	s.Logger.InfoContext(ctx, "reconfigured the Diameter peers", slog.Int("diameter_peers", len(st.Peers)),
		slog.String("cx_realm", st.Realm(settings.ApplicationCx)), slog.String("rx_realm", st.Realm(settings.ApplicationRx)))

	return nil
}

func (s *Server) coreSettings() settings.Settings {
	if c := s.core.Load(); c != nil {
		return c.settings
	}

	return settings.Settings{}
}

func (s *Server) restartCore(ctx context.Context, st settings.Settings, why string) error {
	s.Logger.InfoContext(ctx, "restarting "+why,
		slog.String("home_domain", st.Operator.HomeDomain()), slog.Int("diameter_peers", len(st.Peers)),
		slog.String("policy", string(st.Policy.Interface)))

	renamed := s.mcptt.Load() != nil && s.mcpttHomeDomain != st.Operator.HomeDomain()
	if renamed {
		stopCtx, cancel := context.WithTimeout(ctx, mcpttStopTime)
		s.stopMCPTT(stopCtx)
		cancel()
	}

	renamedIPSMGW := s.ipsmgw.Load() != nil && s.ipsmgwHomeDomain != st.Operator.HomeDomain()
	if renamedIPSMGW {
		stopCtx, cancel := context.WithTimeout(ctx, mcpttStopTime)
		s.stopIPSMGW(stopCtx)
		cancel()
	}

	if c := s.core.Swap(nil); c != nil {
		stopCtx, cancel := context.WithTimeout(ctx, ShutdownTimeout)
		c.shutdown(stopCtx, s.Logger)
		cancel()
	}

	s.records.CloseOpen()

	if renamed {
		if err := s.startMCPTT(ctx, st.Operator.HomeDomain()); err != nil {
			s.Logger.ErrorContext(ctx, "failed to restart MCPTT for the new home domain", slog.Any("error", err))
		}
	}

	if renamedIPSMGW {
		if err := s.startIPSMGW(ctx, st.Operator.HomeDomain()); err != nil {
			s.Logger.ErrorContext(ctx, "failed to restart the IP-SM-GW for the new home domain", slog.Any("error", err))
		}
	}

	c, err := s.startCore(ctx, st)
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to restart "+why, slog.Any("error", err))
		return err
	}

	s.core.Store(c)
	s.resubscribeMCPTT(ctx)
	s.resubscribeIPSMGW(ctx)
	s.Logger.InfoContext(ctx, "restarted "+why, c.attrs()...)

	return nil
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
	sipCtx, cancel := before(ctx, diameterDisconnectTime)
	defer cancel()

	if err := c.sip.Close(sipCtx); err != nil {
		logger.WarnContext(ctx, "failed to stop SIP cleanly", slog.Any("error", err))
	}

	if err := c.policy.close(sipCtx); err != nil {
		logger.WarnContext(ctx, "failed to stop the N5 notification server cleanly", slog.Any("error", err))
	}

	if err := c.node.ShutdownWithCause(ctx, diameter.DisconnectCauseRebooting); err != nil {
		logger.WarnContext(ctx, "failed to stop Diameter cleanly", slog.Any("error", err))
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

	s.Logger.InfoContext(ctx, "ims stopping")

	s.draining.Store(true)

	if err := s.apiServer.Shutdown(ctx); err != nil {
		s.Logger.WarnContext(ctx, "failed to stop the API cleanly", slog.Any("error", err))
	}

	s.stopFollow()
	<-s.followDone

	mcpttCtx, cancel := before(ctx, ShutdownTimeout-mcpttStopTime)
	s.stopMCPTT(mcpttCtx)
	s.stopIPSMGW(mcpttCtx)
	cancel()

	if c := s.core.Swap(nil); c != nil {
		coreCtx, cancel := before(ctx, recordsFlushTime+tracesFlushTime)
		c.shutdown(coreCtx, s.Logger)
		cancel()
	}

	recordsCtx, cancel := before(ctx, tracesFlushTime)
	if err := s.records.Close(recordsCtx); err != nil {
		s.Logger.WarnContext(ctx, "failed to save the call records", slog.Any("error", err))
	}

	cancel()

	s.stopPrune()
	<-s.pruneDone

	if err := s.database.Close(); err != nil {
		s.Logger.WarnContext(ctx, "failed to close the database", slog.Any("error", err))
	}

	s.stopTracing(ctx)
}

func (s *Server) startTracing(ctx context.Context) error {
	s.shutdownTracing = func(context.Context) error { return nil }

	switch {
	case s.TracerProvider != nil:
		s.tracerProvider = s.TracerProvider
		return nil
	case !s.Config.Telemetry.Enabled:
		return nil
	}

	tp, err := tracing.NewProvider(ctx, s.Config.Telemetry.OTLPEndpoint, version.Get().Version, s.Logger)
	if err != nil {
		return fmt.Errorf("start tracing: %w", err)
	}

	s.tracerProvider, s.shutdownTracing = tp, tp.Shutdown

	return nil
}

func (s *Server) stopTracing(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tracesFlushTime)
	defer cancel()

	if err := s.shutdownTracing(ctx); err != nil {
		s.Logger.WarnContext(ctx, "failed to export the last spans", slog.Any("error", err))
	}
}

func (s *Server) ready(ctx context.Context, database *db.DB) error {
	if s.draining.Load() {
		return errors.New("shutting down")
	}

	return database.Ping(ctx)
}

func before(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}

	return context.WithDeadline(ctx, deadline.Add(-d))
}

func (s *Server) timers() timers {
	return timers{
		minExpires:       s.MinExpires,
		reauthInterval:   s.ReauthInterval,
		reauthExpires:    s.ReauthExpires,
		noAnswer:         s.NoAnswerTimeout,
		mediaLossTimeout: s.MediaLossTimeout,
		groupNoAnswer:    s.GroupNoAnswer,
		asTimeout:        s.ASTimeout,
	}
}

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

func apiTLS(c *config.APITLS) (*tls.Config, error) {
	if c == nil {
		return nil, nil //nolint:nilnil
	}

	cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return nil, fmt.Errorf("load the API certificate: %w", err)
	}

	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}
