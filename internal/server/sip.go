package server

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/scscf"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
)

type SIP interface {
	Listeners() []netip.AddrPort
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

type sipServer struct {
	layer     *transaction.Layer
	handler   *placeholderHandler
	listeners []netip.AddrPort
}

type registrar interface {
	Register(ctx context.Context, req scscf.Request) *sip.Response
}

func startSIP(ctx context.Context, cfg config.Config, reg registrar, logger *slog.Logger) (*sipServer, error) {
	h := newPlaceholderHandler(logger, cfg.SIPAliases())
	h.registrar = reg

	layer := transaction.New(transaction.Config{
		Handler:   h,
		Logger:    logger,
		Transport: transport.Config{MaxConnections: cfg.SIP.MaxConnections},
		Aliases:   cfg.SIPAliases(),
	})

	s := &sipServer{layer: layer, handler: h}

	for _, a := range cfg.SIP.Addresses {
		bound, err := layer.Listen(ctx, netip.AddrPortFrom(a, uint16(cfg.SIP.Port)))
		if err != nil {
			return nil, errors.Join(err, s.Close())
		}

		s.listeners = append(s.listeners, bound)
		h.addListener(bound)
	}

	return s, nil
}

func (s *sipServer) Listeners() []netip.AddrPort {
	return s.listeners
}

func (s *sipServer) Close() error {
	err := s.layer.Close()
	s.handler.close()

	return err
}

type placeholderHandler struct {
	log           *slog.Logger
	registrar     registrar
	inviteTimeout time.Duration
	aliases       map[string]bool

	mu        sync.Mutex
	closed    bool
	listeners map[netip.AddrPort]bool
	invites   map[*transaction.ServerTransaction]*time.Timer
	registers sync.WaitGroup
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
	case req.Method == "REGISTER":
		h.register(tx, req)
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

func (h *placeholderHandler) register(tx *transaction.ServerTransaction, req *sip.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	h.registers.Go(func() {
		res := h.registrar.Register(context.Background(), scscf.Request{SIP: req, UEAddress: req.Flow.Remote.Addr()})
		if res.StatusCode == 401 {
			removeAKAKeys(res)
		}

		h.respond(tx, res)
	})
}

func removeAKAKeys(res *sip.Response) {
	values := res.Header.Values("WWW-Authenticate")
	res.Header.Del("WWW-Authenticate")

	for _, v := range values {
		scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")

		var kept []string

		for _, p := range sip.SplitList(rest) {
			name, _, _ := strings.Cut(p, "=")
			if n := strings.ToLower(strings.TrimSpace(name)); n != "ck" && n != "ik" {
				kept = append(kept, strings.TrimSpace(p))
			}
		}

		res.Header.Add("WWW-Authenticate", scheme+" "+strings.Join(kept, ", "))
	}
}

func (h *placeholderHandler) close() {
	h.mu.Lock()

	h.closed = true

	for tx, t := range h.invites {
		t.Stop()
		delete(h.invites, tx)
	}

	h.mu.Unlock()

	h.registers.Wait()
}

func (h *placeholderHandler) respond(tx *transaction.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		h.log.Debug("SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}
