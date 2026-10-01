package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/icscf"
	"github.com/ellanetworks/ims/internal/pcscf"
	"github.com/ellanetworks/ims/internal/scscf"
	"github.com/ellanetworks/ims/internal/trust"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
)

type SIP interface {
	Listeners() []api.SIPEndpoint
	Close() error
}

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
	rolePCSCF = "pcscf"
	roleICSCF = "icscf"
	roleSCSCF = "scscf"
)

type sipServer struct {
	layer       *transaction.Layer
	roles       *dispatcher
	placeholder *placeholderHandler
	registrar   *scscf.Registrar
	listeners   []api.SIPEndpoint
}

func startSIP(ctx context.Context, cfg config.Config, node *diameter.Node, database *db.DB, logger *slog.Logger) (*sipServer, error) {
	ph := newPlaceholderHandler(logger, cfg.SIPAliases())
	roles := newDispatcher(logger)

	layer := transaction.New(transaction.Config{
		Handler:   roles,
		Logger:    logger,
		Transport: transport.Config{MaxConnections: cfg.SIP.MaxConnections},
		Aliases:   cfg.SIPAliases(),
	})

	ph.layer = layer
	s := &sipServer{layer: layer, roles: roles, placeholder: ph}

	pcscfPort, err := s.listen(ctx, rolePCSCF, cfg.SIP.Addresses, cfg.PCSCF.Port)
	if err != nil {
		return nil, errors.Join(err, s.Close())
	}

	icscfPort, err := s.listen(ctx, roleICSCF, cfg.SIP.Addresses, cfg.ICSCF.Port)
	if err != nil {
		return nil, errors.Join(err, s.Close())
	}

	scscfPort, err := s.listen(ctx, roleSCSCF, cfg.SIP.Addresses, cfg.SCSCF.Port)
	if err != nil {
		return nil, errors.Join(err, s.Close())
	}

	name := cfg.SCSCF.Name
	if name == "" {
		name = config.DefaultSCSCFName(cfg.IMS.HomeDomain, int(scscfPort))
	}

	scscfName, err := sip.ParseURI(name)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("S-CSCF name: %w", err), s.Close())
	}

	hss := cfg.Diameter.CxPeer()

	s.registrar = scscf.New(scscf.Config{
		HomeDomain: cfg.IMS.HomeDomain,
		Name:       scscfName,
		MinExpires: time.Duration(cfg.SCSCF.MinExpires) * time.Second,
		MaxExpires: time.Duration(cfg.SCSCF.MaxExpires) * time.Second,
		HSS:        scscf.HSS{ID: hss.ID, Host: hss.Host, Realm: hss.Realm},
		Diameter:   node,
		DB:         database,
		Logger:     logger,
	})

	roles.set(pcscfPort, pcscf.New(pcscf.Config{
		Proxy:     proxy.New(proxy.Config{Layer: layer, Logger: logger, Port: pcscfPort}),
		ICSCFPort: icscfPort,
		Fallback:  ph,
		Logger:    logger,
	}))

	roles.set(icscfPort, icscf.New(icscf.Config{
		HomeDomain: cfg.IMS.HomeDomain,
		Layer:      layer,
		Proxy:      proxy.New(proxy.Config{Layer: layer, Logger: logger, Port: icscfPort}),
		Port:       icscfPort,
		Trust:      trust.New(cfg.SIP.Addresses, cfg.IMS.TrustedNetworks),
		SCSCFs: []icscf.SCSCF{{
			Name:         scscfName,
			Capabilities: cfg.SCSCF.Capabilities,
			Listeners:    s.bound(roleSCSCF),
		}},
		HSS:      icscf.HSS{ID: hss.ID, Realm: hss.Realm},
		Diameter: node,
		Logger:   logger,
	}))

	roles.set(scscfPort, &scscfHandler{log: logger, layer: layer, registrar: s.registrar, fallback: ph})

	return s, nil
}

// listen binds a role's port on every address. Port 0 binds the port the
// first address gets on the others.
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

func (s *sipServer) Close() error {
	if s.registrar != nil {
		s.registrar.Close()
	}

	err := s.layer.Close()
	s.placeholder.close()

	return err
}

// dispatcher hands each message to the role that owns the local port it
// arrived on.
type dispatcher struct {
	log *slog.Logger

	mu    sync.RWMutex
	roles map[uint16]transaction.Handler
}

func newDispatcher(logger *slog.Logger) *dispatcher {
	return &dispatcher{log: logger, roles: make(map[uint16]transaction.Handler)}
}

func (d *dispatcher) set(port uint16, h transaction.Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.roles[port] = h
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
	Register(ctx context.Context, req *sip.Request) *sip.Response
}

// scscfHandler is the S-CSCF's SIP side: REGISTER goes to the registrar.
type scscfHandler struct {
	log       *slog.Logger
	layer     *transaction.Layer
	registrar registrar
	fallback  transaction.Handler
}

func (h *scscfHandler) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	if req.Method != "REGISTER" {
		h.fallback.HandleRequest(tx, req)
		return
	}

	err := h.layer.Go(func(ctx context.Context) {
		res := h.registrar.Register(ctx, req)
		if err := tx.Respond(res); err != nil {
			h.log.Debug("SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
		}
	})
	if err != nil {
		h.log.Debug("dropped SIP REGISTER", slog.String("call-id", req.Header.CallID()), slog.Any("error", err))
	}
}

func (h *scscfHandler) HandleCancel(tx *transaction.ServerTransaction, cancel *sip.Request) {
	h.fallback.HandleCancel(tx, cancel)
}

func (h *scscfHandler) HandleAck(ack *sip.Request) {
	h.fallback.HandleAck(ack)
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
