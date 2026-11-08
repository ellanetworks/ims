package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/as"
	"github.com/ellanetworks/ims/internal/bgcf"
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/homedomain"
	"github.com/ellanetworks/ims/internal/ibcf"
	"github.com/ellanetworks/ims/internal/icscf"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/pcscf"
	"github.com/ellanetworks/ims/internal/scscf"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/trgw"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultSIPPort  = 5060
	defaultSIPSPort = 5061
)

const placeholderAllow = "INVITE, ACK, CANCEL, OPTIONS, REGISTER"

const placeholderInviteTimeout = 64 * transaction.DefaultT1

var knownMethods = map[string]bool{
	"BYE":       true,
	"PRACK":     true,
	"UPDATE":    true,
	"SUBSCRIBE": true,
	"NOTIFY":    true,
	"MESSAGE":   true,
	"INFO":      true,
	"REFER":     true,
	"PUBLISH":   true,
}

const (
	rolePCSCF           = "pcscf"
	rolePCSCFProtected  = "pcscf-protected"
	roleICSCF           = "icscf"
	roleSCSCF           = "scscf"
	roleIBCF            = "ibcf"
	roleIBCFExternal    = "ibcf-external"
	roleIBCFExternalTLS = "ibcf-external-tls"
)

type sipServer struct {
	layer       *transaction.Layer
	roles       *dispatcher
	placeholder *placeholderHandler
	registrar   *scscf.Registrar
	cx          *cxHandler
	rx          *rxHandler
	pcscf       atomic.Pointer[pcscf.PCSCF]
	ibcf        *ibcf.IBCF
	trgw        *trgw.Gateway
	xfrm        *ipsec.XFRM
	listeners   []api.SIPEndpoint
}

type timers struct {
	minExpires       time.Duration
	reauthInterval   time.Duration
	reauthExpires    time.Duration
	noAnswer         time.Duration
	mediaLossTimeout time.Duration
	groupNoAnswer    time.Duration
	asTimeout        time.Duration
	trunkMedia       time.Duration
}

func startSIP(ctx context.Context, cfg config.Config, st settings.Settings, live func() settings.Settings,
	changed func() <-chan struct{}, states settings.TrunkStates,
	numbering func() scscf.Numbering,
	tm timers, node *diameter.Node, rlm *realms, cxh *cxHandler,
	rxh *rxHandler, pf *policyFunction, database *db.DB, records *callrecords.Recorder, m *metrics,
	kernel pcscf.Kernel, failed func(local netip.AddrPort, err error), ases builtinASs, tp trace.TracerProvider,
	logger *slog.Logger,
) (*sipServer, error) {
	op := st.Operator
	ph := newPlaceholderHandler(logger, op.SIPAliases())
	pcscfDomain := trust.New(cfg.SIP.Addresses, nil)
	domain := trust.New(cfg.SIP.Addresses, cfg.SIP.TrustedNetworks)
	resolver := ases.resolver
	observed := observedDiameter{node, m}
	roles := newDispatcher(logger)
	s := &sipServer{roles: roles, placeholder: ph, cx: cxh, rx: rxh}

	if kernel == nil {
		x, err := openXFRM(cfg.SIP.Addresses)
		if err != nil {
			return nil, err
		}

		s.xfrm, kernel = x, x
	}

	s.layer = transaction.New(roles.config(transaction.Config{
		Network: ases.network,
		Logger:  logger,
		Aliases: sipAliases(cfg, op),
		OnListenerFailed: func(local netip.AddrPort, err error) {
			logger.Error("SIP listener failed: the core restarts", slog.String("address", local.String()), slog.Any("error", err))
			failed(local, err)
		},
		TracerProvider: tp,
	}))

	layer := s.layer
	ph.layer = layer

	pcscfPort, err := s.listen(ctx, rolePCSCF, rolePCSCF, cfg.SIP.Addresses, cfg.PCSCF.Port)
	if err != nil {
		return nil, errors.Join(err, s.Close(ctx))
	}

	ipsecServer, err := s.listen(ctx, rolePCSCFProtected, rolePCSCF, cfg.SIP.Addresses, cfg.PCSCF.IPsec.ServerPort)
	if err != nil {
		return nil, errors.Join(err, s.Close(ctx))
	}

	var ipsecClients [2]uint16

	for i := range ipsecClients {
		port := 0
		if i < len(cfg.PCSCF.IPsec.ClientPorts) {
			port = cfg.PCSCF.IPsec.ClientPorts[i]
		}

		if ipsecClients[i], err = s.listen(ctx, rolePCSCFProtected, rolePCSCF, cfg.SIP.Addresses, port); err != nil {
			return nil, errors.Join(err, s.Close(ctx))
		}
	}

	icscfPort, err := s.listen(ctx, roleICSCF, roleICSCF, cfg.SIP.Addresses, cfg.ICSCF.Port)
	if err != nil {
		return nil, errors.Join(err, s.Close(ctx))
	}

	scscfPort, err := s.listen(ctx, roleSCSCF, roleSCSCF, cfg.SIP.Addresses, cfg.SCSCF.Port)
	if err != nil {
		return nil, errors.Join(err, s.Close(ctx))
	}

	for _, a := range append(s.bound(roleICSCF), s.bound(roleSCSCF)...) {
		if err := s.layer.Serve(a); err != nil {
			return nil, errors.Join(fmt.Errorf("serve %s in memory: %w", a, err), s.Close(ctx))
		}
	}

	homeDomains := op.HomeDomains()
	breakout := bgcf.New(live, states)

	if cfg.IBCF != nil {
		if err := s.startIBCF(ctx, cfg, tm, homeDomains, domain, breakout, live, changed, records, m, logger); err != nil {
			return nil, errors.Join(err, s.Close(ctx))
		}
	}

	scscfName, err := sip.ParseURI(op.SCSCFName(int(scscfPort)))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("S-CSCF name: %w", err), s.Close(ctx))
	}

	s.registrar = scscf.New(scscf.Config{
		Home:       homeDomains,
		Name:       scscfName,
		MinExpires: tm.minExpires,

		ReauthInterval: tm.reauthInterval,
		ReauthExpires:  tm.reauthExpires,
		ASTimeout:      cmp.Or(tm.asTimeout, cfg.SCSCF.ASTimeout),
		Trust:          domain,
		Resolver:       resolver,

		HSSRealm:             rlm.of(settings.ApplicationCx),
		Diameter:             observed,
		DB:                   database,
		Records:              records,
		RegistrationAttempts: m.registrations,
		Layer:                layer,
		Listeners:            s.bound(roleSCSCF),
		Logger:               logger,
	})

	if cxh != nil {
		cxh.bind(s.registrar)
	}

	pol := pcscf.Policy{Backend: pf.backend, MediaLossTimeout: tm.mediaLossTimeout}

	pc := pcscf.New(pcscf.Config{
		Layer: layer,
		Proxy: proxy.New(proxy.Config{
			Layer: layer, Logger: logger, Supported: []string{"sec-agree"},
			Listeners: append(s.bound(rolePCSCF), s.bound(rolePCSCFProtected)...),
		}),
		Port:          pcscfPort,
		ICSCFPort:     icscfPort,
		HomeDomain:    homeDomains.Primary(),
		NetworkID:     op.DerivedDomain(),
		SCSCF:         pcscf.SCSCF{Name: scscfName, Listeners: s.bound(roleSCSCF)},
		Registrations: database,
		IPsec: pcscf.IPsec{
			Kernel:      kernel,
			Store:       database,
			Policy:      ipsec.DefaultPolicy(),
			ServerPort:  ipsecServer,
			ClientPorts: ipsecClients,
		},
		Policy:               pol,
		NoAnswer:             tm.noAnswer,
		Records:              records,
		RegistrationAttempts: m.registrations,
		Trust:                pcscfDomain,
		Fallback:             ph,
		Logger:               logger,
	})

	if err := pc.Restore(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("restore the P-CSCF: %w", err), pc.Close(ctx), s.Close(ctx))
	}

	s.pcscf.Store(pc)

	if pf.rx != nil {
		rxh.bind(pf.rx)
	}

	roles.set(&role{
		name:         rolePCSCF,
		handler:      pc,
		filter:       pc.Filter,
		responseFlow: pc.ResponseFlow,
		traceContext: func(f sip.Flow) bool { return pcscfDomain.Trusted(f.Remote.Addr()) },
		done: func(_ context.Context, req *sip.Request, res *sip.Response, elapsed time.Duration) {
			if !pcscfDomain.Trusted(req.Flow.Remote.Addr()) {
				m.sipResponse(req, res, elapsed)
			}
		},
	})

	trusted := func(f sip.Flow) bool { return domain.Trusted(f.Remote.Addr()) }

	ic := icscf.New(icscf.Config{
		Home:      homeDomains,
		NetworkID: op.DerivedDomain(),
		Layer:     layer,
		Proxy:     proxy.New(proxy.Config{Layer: layer, Logger: logger, Listeners: s.bound(roleICSCF)}),
		Port:      icscfPort,
		Trust:     domain,
		Resolver:  resolver,
		SCSCF: icscf.SCSCF{
			Name:      scscfName,
			Listeners: s.bound(roleSCSCF),
		},
		IBCF:                 s.bound(roleIBCF),
		PSISubdomain:         resolver.Has,
		HSSRealm:             rlm.of(settings.ApplicationCx),
		Diameter:             observed,
		Records:              records,
		RegistrationAttempts: m.registrations,
		Logger:               logger,
	})

	roles.set(&role{name: roleICSCF, traceContext: trusted, handler: ic})

	scscfProxy := proxy.New(proxy.Config{Layer: layer, Logger: logger, Listeners: s.bound(roleSCSCF)})

	roles.set(&role{name: roleSCSCF, traceContext: trusted, handler: &scscfHandler{
		log:       logger,
		layer:     layer,
		proxy:     scscfProxy,
		trust:     domain,
		registrar: s.registrar,
		sessions: s.registrar.Sessions(scscf.SessionConfig{
			Proxy:     scscfProxy,
			ICSCF:     s.bound(roleICSCF),
			Numbering: numbering,

			GroupNoAnswer: tm.groupNoAnswer,
		}),
		fallback: ph,
	}})

	s.registrar.Start()

	return s, nil
}

// TS 23.228 clause 4.14
// TS 24.229 clause 5.10.1
func (s *sipServer) startIBCF(ctx context.Context, cfg config.Config, tm timers, homeDomains homedomain.Domains, domain *trust.Domain, breakout *bgcf.BGCF,
	live func() settings.Settings, changed func() <-chan struct{}, records *callrecords.Recorder, m *metrics, logger *slog.Logger,
) error {
	secure, cert, err := ibcfTLS(cfg.IBCF.External)
	if err != nil {
		return err
	}

	internal, err := s.attach(roleIBCF, roleIBCF, cfg.SIP.Addresses, uint16(cfg.IBCF.Port), func(a netip.AddrPort) (netip.AddrPort, error) {
		return s.layer.Listen(ctx, a)
	})
	if err != nil {
		return err
	}

	external, err := s.attach(roleIBCFExternal, roleIBCF, cfg.IBCF.External.Addrs(), config.IBCFExternalPort, func(a netip.AddrPort) (netip.AddrPort, error) {
		return s.layer.Listen(ctx, a)
	})
	if err != nil {
		return err
	}

	tlsRole, bindTLS := "", func(a netip.AddrPort) (netip.AddrPort, error) { return s.layer.ClientTLS(ctx, a) }

	if cert != nil {
		srv := ibcf.ServerTLS(cert, live)
		tlsRole, bindTLS = roleIBCFExternalTLS, func(a netip.AddrPort) (netip.AddrPort, error) { return s.layer.ListenTLS(ctx, a, srv) }
	}

	tlsAddrs := make([]netip.Addr, len(secure))
	for i, a := range secure {
		tlsAddrs[i] = a.Addr()
	}

	tlsLocals, err := s.attach(tlsRole, roleIBCF, tlsAddrs, config.IBCFTLSPort, bindTLS)
	if err != nil {
		return err
	}

	media, err := s.startTrGW(cfg, tm.trunkMedia, m.trunkMedia)
	if err != nil {
		return err
	}

	b, err := ibcf.New(ctx, ibcf.Config{
		Home:        homeDomains,
		Layer:       s.layer,
		Internal:    internal,
		External:    external,
		TLS:         tlsLocals,
		Advertised:  cfg.IBCF.External.Advertised(),
		Certificate: cert,
		Changed:     changed,
		Resolve:     ibcf.SystemResolve,
		ICSCF:       s.bound(roleICSCF),
		Trust:       domain,
		BGCF:        breakout,
		Settings:    live,
		Metrics:     m.ibcf,
		Records:     records,
		Media:       media,
		Logger:      logger,
	})
	if err != nil {
		return fmt.Errorf("start the IBCF: %w", err)
	}

	s.ibcf = b

	s.roles.set(&role{
		name:           roleIBCF,
		handler:        b.Handler(),
		filter:         b.Filter,
		accept:         b.Accept,
		connections:    ibcf.MaxConnectionsPerSource,
		tlsClient:      b.TLSClientOptions,
		traceContext:   b.TraceContext,
		spanAttributes: b.SpanAttributes,
		untraced:       b.Untraced,
	})

	return nil
}

func sipAliases(cfg config.Config, op settings.Operator) map[netip.Addr][]string {
	out := make(map[netip.Addr][]string)

	for _, a := range cfg.SIP.Addresses {
		out[a.Unmap()] = op.SIPAliases()
	}

	if cfg.IBCF != nil {
		for local, a := range cfg.IBCF.External.Advertised() {
			out[local] = append(out[local], a.String())
		}
	}

	return out
}

func (s *sipServer) listen(ctx context.Context, label, name string, addrs []netip.Addr, port int) (uint16, error) {
	bound, err := s.attach(label, name, addrs, uint16(port), func(a netip.AddrPort) (netip.AddrPort, error) { return s.layer.Listen(ctx, a) })
	if err != nil {
		return 0, err
	}

	for _, a := range bound {
		s.placeholder.addListener(a)
	}

	if len(bound) == 0 {
		return uint16(port), nil
	}

	return bound[0].Port(), nil
}

func (s *sipServer) attach(label, name string, addrs []netip.Addr, port uint16, bind func(netip.AddrPort) (netip.AddrPort, error)) ([]netip.AddrPort, error) {
	out := make([]netip.AddrPort, 0, len(addrs))

	for _, a := range addrs {
		bound, err := bind(netip.AddrPortFrom(a.Unmap(), port))
		if err != nil {
			return nil, err
		}

		port = bound.Port()
		out = append(out, bound)
		s.roles.bind(bound, name)

		if label != "" {
			s.listeners = append(s.listeners, api.SIPEndpoint{Role: label, Address: bound, TLS: label == roleIBCFExternalTLS})
		}
	}

	return out, nil
}

func (s *sipServer) bound(role string) []netip.AddrPort {
	var out []netip.AddrPort

	for _, l := range s.listeners {
		if l.Role == role {
			out = append(out, l.Address)
		}
	}

	return out
}

func (s *sipServer) Listeners() []api.SIPEndpoint {
	return s.listeners
}

func (s *sipServer) Reauthenticate(ctx context.Context, impi string) error {
	err := s.registrar.Reauthenticate(ctx, impi)
	if errors.Is(err, scscf.ErrNotRegistered) {
		return api.ErrNotRegistered
	}

	return err
}

func (s *sipServer) Close(ctx context.Context) error {
	if s.cx != nil {
		s.cx.bind(nil)
	}

	if s.rx != nil {
		s.rx.bind(nil)
	}

	if s.registrar != nil {
		s.registrar.Close()
	}

	var err error

	if s.ibcf != nil {
		err = s.ibcf.Shutdown(ctx)
	}

	if s.trgw != nil {
		err = errors.Join(err, s.trgw.Close())
	}

	if p := s.pcscf.Load(); p != nil {
		err = errors.Join(err, p.Close(ctx))
	}

	if s.layer != nil {
		err = errors.Join(err, s.layer.Shutdown(ctx))
	}

	s.placeholder.close()

	if s.xfrm != nil {
		err = errors.Join(err, s.xfrm.Close())
	}

	return err
}

func openXFRM(addrs []netip.Addr) (*ipsec.XFRM, error) {
	x, err := ipsec.Open()
	if err != nil {
		return nil, err
	}

	for _, a := range addrs {
		if err := x.Probe(a); err != nil {
			_ = x.Close()
			return nil, err
		}
	}

	return x, nil
}

type registrar interface {
	Register(ctx context.Context, req *sip.Request, respond func(*sip.Response))
	Subscribe(ctx context.Context, req *sip.Request, routes []sip.URI, respond func(*sip.Response))
}

type scscfHandler struct {
	log       *slog.Logger
	layer     *transaction.Layer
	proxy     *proxy.Proxy
	trust     *trust.Domain
	registrar registrar
	sessions  *scscf.Sessions
	fallback  transaction.Handler
}

func (h *scscfHandler) SpanLinks(req *sip.Request) []trace.SpanContext {
	return h.proxy.SpanLinks(req)
}

func (h *scscfHandler) HandleRequest(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	if !h.trust.Trusted(req.Flow.Remote.Addr()) {
		h.log.WarnContext(ctx, "S-CSCF request from outside the trust domain", slog.String("method", req.Method),
			slog.String("source", req.Flow.Remote.String()))

		res := sip.NewResponse(req, 403, "")
		if err := tx.Respond(res); err != nil {
			h.log.DebugContext(ctx, "SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}

		return
	}

	respond := func(res *sip.Response) {
		if err := tx.Respond(res); err != nil {
			h.log.DebugContext(ctx, "SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}
	}

	var handle func(ctx context.Context)

	switch {
	case req.Method == "REGISTER":
		handle = func(ctx context.Context) { h.registrar.Register(ctx, req, respond) }
	case req.Method == "SUBSCRIBE" && scscf.IsRegEvent(req):
		if res := h.proxy.Check(req); res != nil {
			respond(res)
			return
		}

		out, removed, err := h.proxy.Preprocess(req)
		if err != nil {
			respond(sip.NewResponse(req, 400, "Bad Route"))
			return
		}

		handle = func(ctx context.Context) { h.registrar.Subscribe(ctx, out, removed, respond) }
	case req.Method == "OPTIONS" && h.addressedToSelf(req):
		h.fallback.HandleRequest(ctx, tx, req)
		return
	default:
		h.sessions.HandleRequest(ctx, tx, req)
		return
	}

	if err := h.layer.Go(ctx, handle); err != nil {
		h.log.DebugContext(ctx, "dropped SIP request", slog.String("method", req.Method), slog.String("call-id", req.Header.CallID()),
			slog.Any("error", err))
	}
}

func (h *scscfHandler) addressedToSelf(req *sip.Request) bool {
	out, _, err := h.proxy.Preprocess(req)

	return err == nil && !out.Header.Has("Route") && out.URI.User == "" && h.proxy.IsLocal(out.URI)
}

func (h *scscfHandler) HandleCancel(ctx context.Context, tx *transaction.ServerTransaction, cancel *sip.Request) {
	h.sessions.HandleCancel(ctx, tx, cancel)
}

func (h *scscfHandler) HandleAck(ctx context.Context, ack *sip.Request) {
	if !h.trust.Trusted(ack.Flow.Remote.Addr()) {
		h.log.DebugContext(ctx, "dropped an ACK from outside the trust domain", slog.String("source", ack.Flow.Remote.String()))
		return
	}

	h.sessions.HandleAck(ctx, ack)
}

func (h *scscfHandler) HandleTransactionError(ctx context.Context, tx *transaction.ServerTransaction, err error) {
	h.fallback.HandleTransactionError(ctx, tx, err)
}

type placeholderHandler struct {
	log           *slog.Logger
	layer         *transaction.Layer
	inviteTimeout time.Duration
	aliases       map[string]bool

	mu        sync.Mutex
	closed    bool
	listeners map[netip.AddrPort]bool
	invites   map[*transaction.ServerTransaction]*time.Timer
}

func newPlaceholderHandler(logger *slog.Logger, aliases []string) *placeholderHandler {
	h := &placeholderHandler{
		log:           logger,
		inviteTimeout: placeholderInviteTimeout,
		aliases:       make(map[string]bool, len(aliases)),
		listeners:     make(map[netip.AddrPort]bool),
		invites:       make(map[*transaction.ServerTransaction]*time.Timer),
	}

	for _, a := range aliases {
		h.aliases[strings.ToLower(a)] = true
	}

	return h
}

func (h *placeholderHandler) addListener(local netip.AddrPort) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.listeners[local] = true
}

func (h *placeholderHandler) HandleRequest(ctx context.Context, tx *transaction.ServerTransaction, req *sip.Request) {
	switch {
	case req.Method == "OPTIONS" && h.isSelf(req.URI):
		h.respond(ctx, tx, optionsResponse(req))
	case req.Method == "OPTIONS":
		h.respond(ctx, tx, sip.NewResponse(req, 480, ""))
	case req.Method == "INVITE":
		h.holdInvite(ctx, tx)
	case knownMethods[req.Method]:
		res := sip.NewResponse(req, 405, "")
		res.Header.Set("Allow", placeholderAllow)
		h.respond(ctx, tx, res)
	default:
		h.respond(ctx, tx, sip.NewResponse(req, 501, ""))
	}
}

func optionsResponse(req *sip.Request) *sip.Response {
	res := sip.NewResponse(req, 200, "")
	res.Header.Set("Allow", placeholderAllow)
	res.Header.Set("Accept", "")
	res.Header.Set("Accept-Encoding", "")
	res.Header.Set("Accept-Language", "en")
	res.Header.Set("Supported", "")

	return res
}

func (h *placeholderHandler) isSelf(u sip.URI) bool {
	if !u.IsSIP() && !u.IsSIPS() || u.User != "" {
		return false
	}

	port := u.Port

	switch {
	case port != 0:
	case u.IsSIPS():
		port = defaultSIPSPort
	default:
		port = defaultSIPPort
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if a, ok := u.Addr(); ok {
		return h.listeners[netip.AddrPortFrom(a.Unmap(), port)]
	}

	if !h.aliases[strings.ToLower(u.Host)] {
		return false
	}

	for l := range h.listeners {
		if l.Port() == port {
			return true
		}
	}

	return false
}

func (h *placeholderHandler) HandleCancel(ctx context.Context, tx *transaction.ServerTransaction, _ *sip.Request) {
	if h.release(tx) {
		h.respond(ctx, tx, sip.NewResponse(tx.Request(), 487, ""))
	}
}

func (h *placeholderHandler) HandleAck(ctx context.Context, ack *sip.Request) {
	h.log.DebugContext(ctx, "dropped SIP ACK", slog.String("call-id", ack.Header.CallID()))
}

func (h *placeholderHandler) HandleTransactionError(ctx context.Context, tx *transaction.ServerTransaction, err error) {
	h.release(tx)
	h.log.DebugContext(ctx, "SIP server transaction failed", slog.String("request", tx.Request().StartLine()), slog.Any("error", err))
}

func (h *placeholderHandler) holdInvite(ctx context.Context, tx *transaction.ServerTransaction) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	sc := trace.SpanContextFromContext(ctx)

	h.invites[tx] = time.AfterFunc(h.inviteTimeout, func() {
		if h.release(tx) {
			h.respond(trace.ContextWithSpanContext(context.Background(), sc), tx, sip.NewResponse(tx.Request(), 480, ""))
		}
	})
}

func (h *placeholderHandler) release(tx *transaction.ServerTransaction) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	t, ok := h.invites[tx]
	if ok {
		t.Stop()
		delete(h.invites, tx)
	}

	return ok
}

func (h *placeholderHandler) close() {
	h.mu.Lock()

	h.closed = true

	for tx, t := range h.invites {
		t.Stop()
		delete(h.invites, tx)
	}

	h.mu.Unlock()
}

func (h *placeholderHandler) respond(ctx context.Context, tx *transaction.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		h.log.DebugContext(ctx, "SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}

type builtinASs struct {
	network  *transaction.Network
	resolver *as.Resolver
}

func (b builtinASs) route(old, hosts []string, servers []sip.Server) []string {
	for _, h := range old {
		if !slices.Contains(hosts, h) {
			b.resolver.Set(h, nil)
		}
	}

	for _, h := range hosts {
		b.resolver.Set(h, servers)
	}

	return hosts
}

func asHosts(domains homedomain.Domains, host func(string) string) []string {
	hosts := make([]string, len(domains))
	for i, d := range domains {
		hosts[i] = host(d)
	}

	return hosts
}
