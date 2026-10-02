package server

import (
	"context"
	"log/slog"
	"net/netip"
	"sync/atomic"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/config"
)

const productName = "ims"

// Diameter is what the API and the CSCFs need from the Diameter node.
type Diameter interface {
	Identity() diameter.Identity
	Peers() []diameter.PeerStatus
	Do(ctx context.Context, peerID string, req *diameter.Message, opts ...diameter.DoOption) (*diameter.Message, error)
}

var applications = map[config.Application]diameter.Application{
	config.ApplicationCx: {ID: cx.ApplicationID, VendorID: tgpp.VendorID},
	config.ApplicationRx: {ID: rx.ApplicationID, VendorID: tgpp.VendorID},
}

var transports = map[config.Transport]diameter.Transport{
	config.TransportTCP:  diameter.TransportTCP,
	config.TransportSCTP: diameter.TransportSCTP,
}

func newDiameterNode(cfg config.Diameter, rtr *rtrHandler, rxh *rxHandler, logger *slog.Logger) (*diameter.Node, error) {
	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      cfg.OriginHost,
			OriginRealm:     cfg.OriginRealm,
			HostIPAddresses: []netip.Addr{cfg.Address},
			ProductName:     productName,
		},
		Handler:           newDiameterMux(rtr, rxh),
		OnPeerStateChange: func(p diameter.PeerStatus) { logPeerState(logger, p) },
		Logger:            logger,
	})
	if err != nil {
		return nil, err
	}

	if err := node.SetPeers(diameterPeers(cfg.Peers)); err != nil {
		_ = node.Shutdown(context.Background())
		return nil, err
	}

	return node, nil
}

func diameterPeers(peers []config.DiameterPeer) []diameter.Peer {
	out := make([]diameter.Peer, 0, len(peers))

	for _, p := range peers {
		apps := make([]diameter.Application, 0, len(p.Applications))
		for _, a := range p.Applications {
			apps = append(apps, applications[a])
		}

		out = append(out, diameter.Peer{
			ID:           p.ID,
			Host:         p.Host,
			Addresses:    []netip.Addr{p.Address},
			Port:         uint16(p.Port),
			Transport:    transports[p.Transport],
			Applications: apps,
		})
	}

	return out
}

func newDiameterMux(rtr *rtrHandler, rxh *rxHandler) *diameter.Mux {
	mux := diameter.NewMux()

	mux.Handle(cx.ApplicationID, cx.CommandRegistrationTermination, rtr)
	mux.Handle(rx.ApplicationID, rx.CommandReAuth, diameter.HandlerFunc(rxh.reAuth))
	mux.Handle(rx.ApplicationID, rx.CommandAbortSession, diameter.HandlerFunc(rxh.abortSession))

	return mux
}

type terminator interface {
	Terminate(ctx context.Context, rtr cx.RegistrationTerminationRequest) ([]string, error)
}

type rtrHandler struct {
	log    *slog.Logger
	target atomic.Pointer[terminator]
}

func newRTRHandler(logger *slog.Logger) *rtrHandler {
	return &rtrHandler{log: logger}
}

func (h *rtrHandler) bind(t terminator) {
	if t == nil {
		h.target.Store(nil)
		return
	}

	h.target.Store(&t)
}

func (h *rtrHandler) ServeDiameter(ctx context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	rtr, err := cx.ParseRegistrationTerminationRequest(req)
	if err != nil {
		h.log.Info("invalid Cx RTR", slog.String("peer", c.PeerID()), slog.Any("error", err))
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	t := h.target.Load()
	if t == nil {
		h.log.Warn("Cx RTR before the registrar started", slog.String("impi", rtr.PrivateIdentity))
		return cx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	h.log.Info("Cx RTR", slog.String("impi", rtr.PrivateIdentity), slog.Any("impus", rtr.PublicIdentities),
		slog.String("reason", rtr.Reason.Code.String()))

	associated, err := (*t).Terminate(ctx, rtr)
	if err != nil {
		h.log.Warn("Cx RTR failed", slog.String("impi", rtr.PrivateIdentity), slog.Any("error", err))
		return cx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	a := cx.RegistrationTermination{AssociatedIdentities: associated}

	ans, err := cx.NewRegistrationTerminationAnswer(req, c.LocalIdentity(), a)
	if err != nil {
		h.log.Warn("building the RTA failed", slog.String("impi", rtr.PrivateIdentity), slog.Any("error", err))
		return cx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	return ans
}

// rxSessions is what the Rx handlers need from the P-CSCF.
type rxSessions interface {
	ReAuth(sessionID string, r rx.ReAuthRequest) bool
	AbortSession(sessionID string, r rx.AbortSessionRequest) bool
}

type rxHandler struct {
	log    *slog.Logger
	target atomic.Pointer[rxSessions]
}

func newRxHandler(logger *slog.Logger) *rxHandler {
	return &rxHandler{log: logger}
}

func (h *rxHandler) bind(s rxSessions) {
	if s == nil {
		h.target.Store(nil)
		return
	}

	h.target.Store(&s)
}

// reAuth answers an Rx RAR (TS 29.214 §4.4.6.3).
func (h *rxHandler) reAuth(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	session := tgpp.ParseEnvelope(req).SessionID

	rar, err := rx.ParseReAuthRequest(req)
	if err != nil {
		h.log.Info("invalid Rx RAR", slog.String("peer", c.PeerID()), slog.String("session", session), slog.Any("error", err))
		return rx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	t := h.target.Load()
	if t == nil {
		h.log.Warn("Rx RAR before the P-CSCF started", slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	if !(*t).ReAuth(session, rar) {
		h.log.Info("Rx RAR for an unknown session", slog.String("peer", c.PeerID()), slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnknownSessionID}, 0)
	}

	ans, err := rx.NewReAuthAnswer(req, c.LocalIdentity(), rx.ReAuthAnswer{})
	if err != nil {
		h.log.Warn("building the Rx RAA failed", slog.String("session", session), slog.Any("error", err))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	return ans
}

// abortSession answers an Rx ASR (TS 29.214 §4.4.6.1).
func (h *rxHandler) abortSession(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	session := tgpp.ParseEnvelope(req).SessionID

	asr, err := rx.ParseAbortSessionRequest(req)
	if err != nil {
		h.log.Info("invalid Rx ASR", slog.String("peer", c.PeerID()), slog.String("session", session), slog.Any("error", err))
		return rx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	t := h.target.Load()
	if t == nil {
		h.log.Warn("Rx ASR before the P-CSCF started", slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	if !(*t).AbortSession(session, asr) {
		h.log.Info("Rx ASR for an unknown session", slog.String("peer", c.PeerID()), slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnknownSessionID}, 0)
	}

	ans, err := rx.NewAbortSessionAnswer(req, c.LocalIdentity(), rx.AbortSessionAnswer{})
	if err != nil {
		h.log.Warn("building the Rx ASA failed", slog.String("session", session), slog.Any("error", err))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	return ans
}

func logPeerState(logger *slog.Logger, p diameter.PeerStatus) {
	attrs := []any{
		slog.String("peer", p.ID),
		slog.String("host", p.Host),
		slog.String("state", p.State.String()),
		slog.String("transport", p.Transport.String()),
	}

	if p.RemoteAddr.IsValid() {
		attrs = append(attrs, slog.String("address", p.RemoteAddr.Unmap().String()))
	}

	if p.State == diameter.PeerOpen {
		names := make([]string, 0, len(p.Applications))

		for _, a := range p.Applications {
			for name, app := range applications {
				if app.ID == a.ID {
					names = append(names, string(name))
				}
			}
		}

		logger.Info("Diameter peer open", append(attrs, slog.String("realm", p.Realm), slog.Any("applications", names))...)

		return
	}

	if p.LastError != "" {
		attrs = append(attrs, slog.String("error", p.LastError))
	}

	logger.Info("Diameter peer state changed", attrs...)
}
