package server

import (
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
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/icscf"
	"github.com/ellanetworks/ims/internal/ipsec"
	"github.com/ellanetworks/ims/internal/pcscf"
	"github.com/ellanetworks/ims/internal/scscf"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
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
	rolePCSCF          = "pcscf"
	rolePCSCFProtected = "pcscf-protected"
	roleICSCF          = "icscf"
	roleSCSCF          = "scscf"
)

type sipServer struct {
	layer       *transaction.Layer
	roles       *dispatcher
	placeholder *placeholderHandler
	registrar   *scscf.Registrar
	rtr         *rtrHandler
	rx          *rxHandler
	pcscf       atomic.Pointer[pcscf.PCSCF]
	xfrm        *ipsec.XFRM
	listeners   []api.SIPEndpoint
	served      []api.SIPEndpoint
}

// timers override the defaults of the SIP components' timers.
type timers struct {
	minExpires       time.Duration
	reauthInterval   time.Duration
	reauthExpires    time.Duration
	noAnswer         time.Duration
	mediaLossTimeout time.Duration
}

func startSIP(ctx context.Context, cfg config.Config, st settings.Settings, numbering func() scscf.Numbering,
	tm timers, node *diameter.Node, rtr *rtrHandler,
	rxh *rxHandler, pf *policyFunction, database *db.DB, kernel pcscf.Kernel, logger *slog.Logger,
) (*sipServer, error) {
	op := st.Operator
	ph := newPlaceholderHandler(logger, op.SIPAliases())
	roles := newDispatcher(logger)
	s := &sipServer{roles: roles, placeholder: ph, rtr: rtr, rx: rxh}

	if kernel == nil {
		x, err := openXFRM(cfg.SIP.Addresses)
		if err != nil {
			return nil, err
		}

		s.xfrm, kernel = x, x
	}

	s.layer = transaction.New(transaction.Config{
		Handler: roles,
		Logger:  logger,
		Aliases: op.SIPAliases(),
		Filter: func(m sip.Message) error {
			if p := s.pcscf.Load(); p != nil {
				return p.Filter(m)
			}

			return nil
		},
		ResponseFlow: func(req *sip.Request, res *sip.Response) (sip.Flow, bool, error) {
			if p := s.pcscf.Load(); p != nil {
				return p.ResponseFlow(req, res)
			}

			return sip.Flow{}, false, nil
		},
		OnServerDone: roles.logTransaction,
	})

	layer := s.layer
	ph.layer = layer

	pcscfPort, err := s.listen(ctx, rolePCSCF, cfg.SIP.Addresses, cfg.PCSCF.Port)
	if err != nil {
		return nil, errors.Join(err, s.Close())
	}

	ipsecServer, err := s.listen(ctx, rolePCSCFProtected, cfg.SIP.Addresses, cfg.PCSCF.IPsec.ServerPort)
	if err != nil {
		return nil, errors.Join(err, s.Close())
	}

	var ipsecClients [2]uint16

	for i := range ipsecClients {
		port := 0
		if i < len(cfg.PCSCF.IPsec.ClientPorts) {
			port = cfg.PCSCF.IPsec.ClientPorts[i]
		}

		if ipsecClients[i], err = s.listen(ctx, rolePCSCFProtected, cfg.SIP.Addresses, port); err != nil {
			return nil, errors.Join(err, s.Close())
		}
	}

	icscfPort, err := s.serve(roleICSCF, cfg.SIP.Addresses, cfg.ICSCF.Port)
	if err != nil {
		return nil, errors.Join(err, s.Close())
	}

	scscfPort, err := s.serve(roleSCSCF, cfg.SIP.Addresses, cfg.SCSCF.Port)
	if err != nil {
		return nil, errors.Join(err, s.Close())
	}

	scscfName, err := sip.ParseURI(op.SCSCFName(int(scscfPort)))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("S-CSCF name: %w", err), s.Close())
	}

	hss, _ := st.PeerServing(settings.ApplicationCx)
	domain := trust.New(cfg.SIP.Addresses, nil)
	homeDomain := op.HomeDomain()

	s.registrar = scscf.New(scscf.Config{
		HomeDomain: homeDomain,
		Name:       scscfName,
		MinExpires: tm.minExpires,

		ReauthInterval: tm.reauthInterval,
		ReauthExpires:  tm.reauthExpires,

		HSS:       scscf.HSS{ID: hss.ID, Host: hss.Host, Realm: hss.Realm},
		Diameter:  node,
		DB:        database,
		Layer:     layer,
		Listeners: s.bound(roleSCSCF),
		Logger:    logger,
	})

	if rtr != nil {
		rtr.bind(s.registrar)
	}

	pol := pcscf.Policy{Backend: pf.backend, MediaLossTimeout: tm.mediaLossTimeout}

	pc := pcscf.New(pcscf.Config{
		Layer: layer,
		Proxy: proxy.New(proxy.Config{
			Layer: layer, Logger: logger, Port: pcscfPort, Supported: []string{"sec-agree"},
			LocalPorts: []uint16{ipsecServer, ipsecClients[0], ipsecClients[1]},
		}),
		Port:          pcscfPort,
		ICSCFPort:     icscfPort,
		HomeDomain:    homeDomain,
		SCSCF:         pcscf.SCSCF{Name: scscfName, Listeners: s.bound(roleSCSCF)},
		Registrations: database,
		IPsec: pcscf.IPsec{
			Kernel:      kernel,
			Store:       database,
			Policy:      ipsec.DefaultPolicy(),
			ServerPort:  ipsecServer,
			ClientPorts: ipsecClients,
		},
		Policy:   pol,
		NoAnswer: tm.noAnswer,
		Trust:    domain,
		Fallback: ph,
		Logger:   logger,
	})

	if err := pc.Restore(ctx); err != nil {
		pc.Close()
		return nil, errors.Join(fmt.Errorf("restore the P-CSCF: %w", err), s.Close())
	}

	s.pcscf.Store(pc)

	if pf.rx != nil {
		rxh.bind(pf.rx)
	}

	for _, port := range []uint16{pcscfPort, ipsecServer, ipsecClients[0], ipsecClients[1]} {
		roles.set(port, rolePCSCF, pc)
	}

	roles.set(icscfPort, roleICSCF, icscf.New(icscf.Config{
		HomeDomain: homeDomain,
		Layer:      layer,
		Proxy:      proxy.New(proxy.Config{Layer: layer, Logger: logger, Port: icscfPort}),
		Port:       icscfPort,
		Trust:      domain,
		SCSCF: icscf.SCSCF{
			Name:      scscfName,
			Listeners: s.bound(roleSCSCF),
		},
		HSS:      icscf.HSS{ID: hss.ID, Realm: hss.Realm},
		Diameter: node,
		Logger:   logger,
	}))

	scscfProxy := proxy.New(proxy.Config{Layer: layer, Logger: logger, Port: scscfPort})

	roles.set(scscfPort, roleSCSCF, &scscfHandler{
		log:       logger,
		layer:     layer,
		proxy:     scscfProxy,
		trust:     domain,
		registrar: s.registrar,
		sessions: s.registrar.Sessions(scscf.SessionConfig{
			Proxy:     scscfProxy,
			ICSCF:     s.bound(roleICSCF),
			Numbering: numbering,
		}),
		fallback: ph,
	})

	s.registrar.Start()

	return s, nil
}

func (s *sipServer) listen(ctx context.Context, role string, addrs []netip.Addr, port int) (uint16, error) {
	p := uint16(port)

	for _, a := range addrs {
		bound, err := s.layer.Listen(ctx, netip.AddrPortFrom(a, p))
		if err != nil {
			return 0, err
		}

		p = bound.Port()
		s.listeners = append(s.listeners, api.SIPEndpoint{Role: role, Address: bound})
		s.placeholder.addListener(bound)
	}

	return p, nil
}

func (s *sipServer) serve(role string, addrs []netip.Addr, port int) (uint16, error) {
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("%s port %d: a role served in memory needs a fixed port", role, port)
	}

	p := uint16(port)

	for _, a := range addrs {
		local := netip.AddrPortFrom(a, p)
		if err := s.layer.Serve(local); err != nil {
			return 0, err
		}

		s.served = append(s.served, api.SIPEndpoint{Role: role, Address: local})
		s.placeholder.addListener(local)
	}

	return p, nil
}

func (s *sipServer) bound(role string) []netip.AddrPort {
	var out []netip.AddrPort

	for _, l := range append(slices.Clip(s.listeners), s.served...) {
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

func (s *sipServer) Close() error {
	if s.rtr != nil {
		s.rtr.bind(nil)
	}

	if s.rx != nil {
		s.rx.bind(nil)
	}

	if s.registrar != nil {
		s.registrar.Close()
	}

	if p := s.pcscf.Load(); p != nil {
		p.Close()
	}

	var err error

	if s.layer != nil {
		err = s.layer.Close()
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

type dispatcher struct {
	log *slog.Logger

	mu    sync.RWMutex
	roles map[uint16]transaction.Handler
	names map[uint16]string
}

func newDispatcher(logger *slog.Logger) *dispatcher {
	return &dispatcher{log: logger, roles: make(map[uint16]transaction.Handler), names: make(map[uint16]string)}
}

func (d *dispatcher) set(port uint16, name string, h transaction.Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.roles[port] = h
	d.names[port] = name
}

func (d *dispatcher) logTransaction(req *sip.Request, res *sip.Response, elapsed time.Duration) {
	level := slog.LevelDebug

	switch req.Method {
	case "INVITE", "BYE", "CANCEL", "UPDATE", "MESSAGE":
		level = slog.LevelInfo
	}

	d.mu.RLock()
	role := d.names[req.Flow.Local.Port()]
	d.mu.RUnlock()

	attrs := []slog.Attr{
		slog.String("role", role),
		slog.String("method", req.Method),
		slog.String("call_id", req.Header.CallID()),
	}

	if from, err := req.Header.From(); err == nil {
		attrs = append(attrs, slog.String("from", from.URI.String()))
	}

	if to, err := req.Header.To(); err == nil {
		attrs = append(attrs, slog.String("to", to.URI.String()))
	}

	attrs = append(attrs, slog.String("remote", req.Flow.Remote.String()), slog.Duration("duration", elapsed))

	if res == nil {
		d.log.LogAttrs(context.Background(), level, "SIP transaction ended without a final response", attrs...)
		return
	}

	attrs = append(attrs, slog.Int("status", res.StatusCode), slog.String("reason", res.Reason))
	d.log.LogAttrs(context.Background(), level, "SIP transaction", attrs...)
}

func (d *dispatcher) role(f sip.Flow) transaction.Handler {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.roles[f.Local.Port()]
}

func (d *dispatcher) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	h := d.role(req.Flow)
	if h == nil {
		res := sip.NewResponse(req, 503, "")
		res.Header.Set("Retry-After", "1")

		if err := tx.Respond(res); err != nil {
			d.log.Debug("SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}

		return
	}

	h.HandleRequest(tx, req)
}

func (d *dispatcher) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	if h := d.role(tx.Request().Flow); h != nil {
		h.HandleCancel(tx, cancel)
	}
}

func (d *dispatcher) HandleAck(ack *sip.Request) {
	if h := d.role(ack.Flow); h != nil {
		h.HandleAck(ack)
	}
}

func (d *dispatcher) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	if h := d.role(tx.Request().Flow); h != nil {
		h.HandleTransactionError(tx, err)
	}
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

func (h *scscfHandler) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	if !h.trust.Trusted(req.Flow.Remote.Addr()) {
		h.log.Warn("S-CSCF request from outside the trust domain", slog.String("method", req.Method),
			slog.String("source", req.Flow.Remote.String()))

		res := sip.NewResponse(req, 403, "")
		if err := tx.Respond(res); err != nil {
			h.log.Debug("SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}

		return
	}

	respond := func(res *sip.Response) {
		if err := tx.Respond(res); err != nil {
			h.log.Debug("SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
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
		h.fallback.HandleRequest(tx, req)
		return
	default:
		h.sessions.HandleRequest(tx, req)
		return
	}

	if err := h.layer.Go(handle); err != nil {
		h.log.Debug("dropped SIP request", slog.String("method", req.Method), slog.String("call-id", req.Header.CallID()),
			slog.Any("error", err))
	}
}

func (h *scscfHandler) addressedToSelf(req *sip.Request) bool {
	out, _, err := h.proxy.Preprocess(req)

	return err == nil && !out.Header.Has("Route") && out.URI.User == "" && h.proxy.IsLocal(out.URI)
}

func (h *scscfHandler) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	h.sessions.HandleCancel(tx, cancel)
}

func (h *scscfHandler) HandleAck(ack *sip.Request) {
	if !h.trust.Trusted(ack.Flow.Remote.Addr()) {
		h.log.Debug("dropped an ACK from outside the trust domain", slog.String("source", ack.Flow.Remote.String()))
		return
	}

	h.sessions.HandleAck(ack)
}

func (h *scscfHandler) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	h.fallback.HandleTransactionError(tx, err)
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

func (h *placeholderHandler) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	switch {
	case req.Method == "OPTIONS" && h.isSelf(req.URI):
		h.respond(tx, optionsResponse(req))
	case req.Method == "OPTIONS":
		h.respond(tx, sip.NewResponse(req, 480, ""))
	case req.Method == "INVITE":
		h.holdInvite(tx)
	case knownMethods[req.Method]:
		res := sip.NewResponse(req, 405, "")
		res.Header.Set("Allow", placeholderAllow)
		h.respond(tx, res)
	default:
		h.respond(tx, sip.NewResponse(req, 501, ""))
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

func (h *placeholderHandler) HandleCancel(tx *transaction.ServerTransaction, _ *sip.Request) {
	if h.release(tx) {
		h.respond(tx, sip.NewResponse(tx.Request(), 487, ""))
	}
}

func (h *placeholderHandler) HandleAck(ack *sip.Request) {
	h.log.Debug("dropped SIP ACK", slog.String("call-id", ack.Header.CallID()))
}

func (h *placeholderHandler) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	h.release(tx)
	h.log.Debug("SIP server transaction failed", slog.String("request", tx.Request().StartLine()), slog.Any("error", err))
}

func (h *placeholderHandler) holdInvite(tx *transaction.ServerTransaction) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	h.invites[tx] = time.AfterFunc(h.inviteTimeout, func() {
		if h.release(tx) {
			h.respond(tx, sip.NewResponse(tx.Request(), 480, ""))
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

func (h *placeholderHandler) respond(tx *transaction.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		h.log.Debug("SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}
