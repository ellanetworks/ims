package server

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
	"github.com/ellanetworks/ims/sip/transport"
)

// SIP is what the API and the tests need from the SIP stack.
type SIP interface {
	// Listeners returns the bound addresses, each serving UDP and TCP.
	Listeners() []netip.AddrPort
	Close() error
}

// placeholderAllow lists the methods the placeholder handler answers.
const placeholderAllow = "INVITE, ACK, CANCEL, OPTIONS"

// placeholderInviteTimeout bounds how long an INVITE waits for a CANCEL
// before it is refused. The INVITE server transaction has no timer of its
// own in Proceeding, so the TU must end it (RFC 3261 §16.6 Timer C).
const placeholderInviteTimeout = 64 * transaction.DefaultT1

type sipServer struct {
	layer     *transaction.Layer
	handler   *placeholderHandler
	listeners []netip.AddrPort
}

// startSIP listens on each SIP address at the SIP port, over UDP and TCP.
func startSIP(ctx context.Context, cfg config.Config, logger *slog.Logger) (*sipServer, error) {
	h := newPlaceholderHandler(logger)

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

// placeholderHandler answers requests until the CSCFs take them over.
type placeholderHandler struct {
	log           *slog.Logger
	inviteTimeout time.Duration

	mu      sync.Mutex
	closed  bool
	invites map[*transaction.ServerTransaction]*time.Timer
}

func newPlaceholderHandler(logger *slog.Logger) *placeholderHandler {
	return &placeholderHandler{
		log:           logger,
		inviteTimeout: placeholderInviteTimeout,
		invites:       make(map[*transaction.ServerTransaction]*time.Timer),
	}
}

func (h *placeholderHandler) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	switch req.Method {
	case "OPTIONS":
		res := sip.NewResponse(req, 200, "")
		res.Header.Set("Allow", placeholderAllow)
		h.respond(tx, res)
	case "INVITE":
		h.holdInvite(tx)
	default:
		h.respond(tx, sip.NewResponse(req, 501, ""))
	}
}

// HandleCancel answers the INVITE with 487; the layer has already answered
// the CANCEL (RFC 3261 §9.2).
func (h *placeholderHandler) HandleCancel(tx *transaction.ServerTransaction, _ *sip.Request) {
	if h.release(tx) {
		h.respond(tx, sip.NewResponse(tx.Request(), 487, ""))
	}
}

// HandleAck drops ACKs to 2xx responses, which the placeholder never sends.
func (h *placeholderHandler) HandleAck(ack *sip.Request) {
	h.log.Debug("dropped SIP ACK", slog.String("call-id", ack.Header.CallID()))
}

func (h *placeholderHandler) HandleTransactionError(tx *transaction.ServerTransaction, err error) {
	h.release(tx)
	h.log.Debug("SIP server transaction failed", slog.String("request", tx.Request().StartLine()), slog.Any("error", err))
}

// holdInvite leaves the INVITE pending so that a CANCEL can end it, and
// refuses it after inviteTimeout.
func (h *placeholderHandler) holdInvite(tx *transaction.ServerTransaction) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	h.invites[tx] = time.AfterFunc(h.inviteTimeout, func() {
		if h.release(tx) {
			h.respond(tx, sip.NewResponse(tx.Request(), 501, ""))
		}
	})
}

// release forgets a held INVITE and reports whether it was held.
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
	defer h.mu.Unlock()

	h.closed = true

	for tx, t := range h.invites {
		t.Stop()
		delete(h.invites, tx)
	}
}

func (h *placeholderHandler) respond(tx *transaction.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		h.log.Debug("SIP response failed", slog.String("response", res.StartLine()), slog.Any("error", err))
	}
}
