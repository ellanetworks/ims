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
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/ui"
	"github.com/ellanetworks/ims/version"
)

var ErrAlreadyStarted = errors.New("server: already started")

const (
	// ShutdownTimeout is the time stopping the IMS takes at most when nothing fails: for the MCPTT AS to end its
	// calls, for the P-CSCF to end its policy sessions, then for Diameter to disconnect from its peers and for the call records to be saved.
	ShutdownTimeout = mcpttStopTime + pcscf.DefaultPolicyTimeout + diameterDisconnectTime + recordsFlushTime + time.Second

	// mcpttStopTime is what stopping the IMS leaves the MCPTT AS to end its calls and subscriptions, before the core.
	mcpttStopTime = 5 * time.Second

	// diameterDisconnectTime is what stopping the core leaves Diameter to disconnect from its peers (RFC 6733
	// §5.4), after SIP, whose policy sessions end over Diameter.
	diameterDisconnectTime = 2 * time.Second
	// recordsFlushTime is what stopping the IMS leaves the call records to be saved, after the core.
	recordsFlushTime = 2 * time.Second
)

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
	ASTimeout        time.Duration
	// PruneInterval changes how often call records are pruned in tests. Zero keeps the default.
	PruneInterval time.Duration
	// BcryptCost is the cost of the API's password hashes, lowered in tests: bcrypt's default when zero.
	BcryptCost int

	database *db.DB
	// records outlives the cores, so that it closes the records of the calls a restart loses.
	records     *callrecords.Recorder
	metrics     *metrics
	stopPrune   context.CancelFunc
	pruneDone   chan struct{}
	settings    *settings.Live
	apiServer   *http.Server
	apiListener net.Listener

	// mcptt is the built-in MCPTT AS, if configured. It outlives the cores: a restart of the core leaves its calls
	// and registrations alone, but one for a new home domain, which renames it, restarts it. ases is where the cores
	// find it.
	mcptt atomic.Pointer[mcptt.AS]
	ases  builtinASs
	// mcpttHomeDomain is the home domain the MCPTT AS is named under: changed by follow only.
	mcpttHomeDomain string

	// ipsmgw is the built-in IP-SM-GW, which the iFCs of the HSS route to or not. Like the MCPTT AS, it outlives the
	// cores but one for a new home domain. ipsmgwHomeDomain is the home domain it is named under.
	ipsmgw           atomic.Pointer[ipsmgw.AS]
	ipsmgwHomeDomain string

	// The core is replaced when the operator's identity changes.
	core       atomic.Pointer[core]
	stopFollow context.CancelFunc
	followDone chan struct{}

	// sipFailed wakes follow when a SIP listener of the core failed, for it to restart the core.
	sipFailed chan struct{}
}

// core is the part of the IMS that its settings build, apart from what it reads when it uses it: Diameter, the
// policy function and SIP.
type core struct {
	settings    settings.Settings
	node        *diameter.Node
	diameterLns []diameter.Listener
	realms      *realms
	policy      *policyFunction
	sip         *sipServer

	// failed is set once a SIP listener of the core failed for good: the core is restarted.
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

	database, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		return err
	}

	initial, err := database.GetSettings(ctx)
	if err != nil {
		_ = database.Close()
		return err
	}

	jwtSecret, err := database.JWTSecret(ctx)
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

			return err
		}
	}

	if err := s.startIPSMGW(ctx, initial.Operator.HomeDomain()); err != nil {
		s.stopMCPTT(ctx)

		_ = s.records.Close(ctx)
		_ = apiLn.Close()
		_ = database.Close()

		return err
	}

	c, err := s.startCore(ctx, initial)
	if err != nil {
		s.stopIPSMGW(ctx)
		s.stopMCPTT(ctx)

		_ = s.records.Close(ctx)
		_ = apiLn.Close()
		_ = database.Close()

		return err
	}

	s.core.Store(c)
	s.resubscribeMCPTT()
	s.resubscribeIPSMGW()

	view := coreView{s}

	apiCfg := api.Config{
		Settings:      live,
		Diameter:      view,
		SIP:           view,
		Registrations: view,
		Policy:        view,
		CallRecords:   database,
		Frontend:      ui.FS(),
		Metrics:       s.metrics.registry,
		Auth:          database,
		JWTSecret:     jwtSecret,
		BcryptCost:    s.BcryptCost,
		// The cookie is Secure, and HSTS on, only over TLS: a browser would not send a Secure cookie over HTTP.
		SecureCookie: tlsConfig != nil,
		Now:          time.Now,
		Logger:       s.Logger,
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
	s.Logger.Info("ims started", append(attrs, c.attrs()...)...)

	return nil
}

// startCore starts Diameter, the policy function and SIP from their settings.
func (s *Server) startCore(ctx context.Context, st settings.Settings) (*core, error) {
	cfg := s.Config

	diameterLns, err := listenDiameter(ctx, cfg.Diameter, st)
	if err != nil {
		return nil, err
	}

	cxh := newCxHandler(s.Logger)
	rxh := newRxHandler(s.Logger)

	node, err := newDiameterNode(cfg.Diameter, st, s.DiameterHandshakeTimeout, cxh, rxh, ipsmgwHandler{s.ipsmgw.Load},
		s.Logger)
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

	pf, err := newPolicyFunction(ctx, cfg, st, node, rlm, s.metrics, s.Logger)
	if err != nil {
		_ = node.Shutdown(ctx)
		return nil, err
	}

	failed := new(atomic.Bool)

	// A SIP listener that failed for good, as its address removed, is a P-CSCF, I-CSCF or S-CSCF no longer reachable
	// on it: the core is restarted, as for new settings, and retried until it listens again.
	sipFailed := func(netip.AddrPort, error) {
		failed.Store(true)

		select {
		case s.sipFailed <- struct{}{}:
		default:
		}
	}

	sipServer, err := startSIP(ctx, cfg, st, s.numbering, s.timers(), node, rlm, cxh, rxh, pf, s.database, s.records,
		s.metrics, s.IPsec, sipFailed, s.ases, s.Logger)
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

// follow applies the settings as they change: new Diameter peers and routes reconfigure the running node; a new
// identity, Diameter transport or policy function replaces the core. Other settings are read when used.
func (s *Server) follow(ctx context.Context) {
	defer close(s.followDone)

	backoff := minRetry
	number := s.settings.Get().IPSMGW.Number

	for {
		changed := s.settings.Changed()

		if st := s.settings.Get(); st.IPSMGW.Number != number {
			number = st.IPSMGW.Number

			s.ipsmgwNumberChanged()
		}

		var retry <-chan time.Time

		if st := s.settings.Get(); ctx.Err() == nil {
			current := s.coreSettings()
			c := s.core.Load()

			switch {
			case c != nil && c.failed.Load():
				s.restartCore(ctx, st, "for a failed SIP listener")
			case current.SameCore(st):
			case c != nil && current.SameNode(st):
				s.reconfigureNode(ctx, st)
			default:
				s.restartCore(ctx, st, "for the new settings")
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
		case <-s.sipFailed:
		}
	}
}

const (
	minRetry = time.Second
	maxRetry = 30 * time.Second
)

// reconfigureNode gives the running Diameter node its new peers and routes, without a restart. While the realms
// change, the node routes both the old and the new ones, so that a request built with either finds its route. A node
// that refuses its new peers is replaced, so that it does not keep serving the old ones.
func (s *Server) reconfigureNode(ctx context.Context, st settings.Settings) {
	c := s.core.Load()

	err := c.node.SetPeers(diameterPeers(st, c.settings))
	if err == nil {
		c.realms.set(st)
		err = c.node.SetPeers(diameterPeers(st))
	}

	if err != nil {
		s.Logger.Error("failed to reconfigure the Diameter peers", slog.Any("error", err))
		s.restartCore(ctx, st, "for the new settings")

		return
	}

	next := *c
	next.settings = st
	s.core.Store(&next)

	s.Logger.Info("reconfigured the Diameter peers", slog.Int("diameter_peers", len(st.Peers)),
		slog.String("cx_realm", st.Realm(settings.ApplicationCx)), slog.String("rx_realm", st.Realm(settings.ApplicationRx)))
}

// coreSettings are the settings the current core was built from. Without a core, as after it failed to restart,
// they are none, so that the next change tries again.
func (s *Server) coreSettings() settings.Settings {
	if c := s.core.Load(); c != nil {
		return c.settings
	}

	return settings.Settings{}
}

// restartCore replaces the core with one built from st, why it does. Peers and UEs reconnect to it; registrations and
// security associations are restored from the database.
func (s *Server) restartCore(ctx context.Context, st settings.Settings, why string) {
	s.Logger.Info("restarting "+why,
		slog.String("home_domain", st.Operator.HomeDomain()), slog.Int("diameter_peers", len(st.Peers)),
		slog.String("policy", string(st.Policy.Interface)))

	// A new home domain renames the MCPTT AS, its PSIs, and the session identities of its calls, which it ends first,
	// through the core: it starts again under the new name, as after a restart of the IMS.
	renamed := s.mcptt.Load() != nil && s.mcpttHomeDomain != st.Operator.HomeDomain()
	if renamed {
		stopCtx, cancel := context.WithTimeout(ctx, mcpttStopTime)
		s.stopMCPTT(stopCtx)
		cancel()
	}

	// So does the IP-SM-GW: its host is under the home domain.
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

	// The calls in progress were lost with the core.
	s.records.CloseOpen()

	if renamed {
		if err := s.startMCPTT(ctx, st.Operator.HomeDomain()); err != nil {
			s.Logger.Error("failed to restart MCPTT for the new home domain", slog.Any("error", err))
		}
	}

	if renamedIPSMGW {
		if err := s.startIPSMGW(ctx, st.Operator.HomeDomain()); err != nil {
			s.Logger.Error("failed to restart the IP-SM-GW for the new home domain", slog.Any("error", err))
		}
	}

	c, err := s.startCore(ctx, st)
	if err != nil {
		s.Logger.Error("failed to restart "+why, slog.Any("error", err))
		return
	}

	s.core.Store(c)
	s.resubscribeMCPTT()
	s.resubscribeIPSMGW()
	s.Logger.Info("restarted "+why, c.attrs()...)
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

// shutdown stops the core until ctx is done, Diameter last, since SIP ends its policy sessions over Diameter or N5.
func (c *core) shutdown(ctx context.Context, logger *slog.Logger) {
	sipCtx, cancel := before(ctx, diameterDisconnectTime)
	defer cancel()

	if err := c.sip.Close(sipCtx); err != nil {
		logger.Warn("failed to stop SIP cleanly", slog.Any("error", err))
	}

	if err := c.policy.close(sipCtx); err != nil {
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

	// MCPTT first: its BYEs and unsubscriptions go through the core.
	mcpttCtx, cancel := before(ctx, ShutdownTimeout-mcpttStopTime)
	s.stopMCPTT(mcpttCtx)
	s.stopIPSMGW(mcpttCtx)
	cancel()

	if c := s.core.Swap(nil); c != nil {
		coreCtx, cancel := before(ctx, recordsFlushTime)
		c.shutdown(coreCtx, s.Logger)
		cancel()
	}

	// The records of the calls in progress are saved as they are, and closed when the IMS starts again.
	if err := s.records.Close(ctx); err != nil {
		s.Logger.Warn("failed to save the call records", slog.Any("error", err))
	}

	s.stopPrune()
	<-s.pruneDone

	if err := s.database.Close(); err != nil {
		s.Logger.Warn("failed to close the database", slog.Any("error", err))
	}
}

// before is ctx, done d before ctx is, so that what runs under it leaves d to what comes after.
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

// apiTLS returns the TLS configuration of the API, or nil for none. Go's defaults choose the cipher suites and key
// exchanges, which keeps the post-quantum hybrid key exchange that a list of curves would turn off.
func apiTLS(c *config.APITLS) (*tls.Config, error) {
	if c == nil {
		return nil, nil //nolint:nilnil // No TLS is not an error.
	}

	cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return nil, fmt.Errorf("load the API certificate: %w", err)
	}

	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}
