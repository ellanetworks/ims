package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/sctp"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/settings"
)

const productName = "ims"

// Above freeDiameter's 10 s CNX_TIMEOUT: while its own dial to us is pending,
// it holds our CER and answers only when that timer fires (libfdcore p_psm.c).
const DefaultHandshakeTimeout = 15 * time.Second

var applications = map[settings.Application]diameter.Application{
	settings.ApplicationCx: {ID: cx.ApplicationID, VendorID: tgpp.VendorID},
	settings.ApplicationRx: {ID: rx.ApplicationID, VendorID: tgpp.VendorID},
}

var transports = map[settings.Transport]diameter.Transport{
	settings.TransportTCP:  diameter.TransportTCP,
	settings.TransportSCTP: diameter.TransportSCTP,
}

// realms are the realms the running Diameter node routes Cx and Rx to, which the S-CSCF, I-CSCF and P-CSCF
// address their requests to. They change with the node's routes.
type realms struct {
	st atomic.Pointer[settings.Settings]
}

func newRealms(st settings.Settings) *realms {
	r := &realms{}
	r.set(st)

	return r
}

func (r *realms) set(st settings.Settings) {
	r.st.Store(&st)
}

func (r *realms) of(app settings.Application) func() string {
	return func() string { return r.st.Load().Realm(app) }
}

func newDiameterNode(cfg config.Diameter, s settings.Settings, handshake time.Duration, rtr *rtrHandler,
	rxh *rxHandler, logger *slog.Logger,
) (*diameter.Node, error) {
	op := s.Operator

	if handshake <= 0 {
		handshake = DefaultHandshakeTimeout
	}

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      op.DiameterHost(),
			OriginRealm:     op.DiameterRealm(),
			HostIPAddresses: []netip.Addr{cfg.Address},
			ProductName:     productName,
		},
		Handler:           newDiameterMux(rtr, rxh),
		HandshakeTimeout:  handshake,
		OnPeerStateChange: func(p diameter.PeerStatus) { logPeerState(logger, p) },
		Logger:            logger,
	})
	if err != nil {
		return nil, err
	}

	if err := node.SetPeers(diameterPeers(s)); err != nil {
		_ = node.Shutdown(context.Background())
		return nil, err
	}

	return node, nil
}

// RFC 6733 §2.1: a node that dials its peers must still accept their
// connections; §5.6.4 elects one when both sides connect at once.
func listenDiameter(ctx context.Context, cfg config.Diameter, st settings.Settings) ([]diameter.Listener, error) {
	addr := netip.AddrPortFrom(cfg.Address, uint16(cfg.Port))

	var lns []diameter.Listener

	for _, t := range st.Transports() {
		ln, err := listenDiameterOn(ctx, t, addr)
		if err != nil {
			closeListeners(lns)
			return nil, fmt.Errorf("listen for Diameter over %s on %s: %w", t, addr, err)
		}

		lns = append(lns, ln)
	}

	return lns, nil
}

// probeSCTP checks that the host can listen for Diameter over SCTP on an address.
func probeSCTP(ctx context.Context, a netip.Addr) error {
	ln, err := listenDiameterOn(ctx, settings.TransportSCTP, netip.AddrPortFrom(a, 0))
	if err != nil {
		return err
	}

	return ln.Close()
}

func listenDiameterOn(ctx context.Context, t settings.Transport, addr netip.AddrPort) (diameter.Listener, error) {
	if t == settings.TransportSCTP {
		var lc sctp.ListenConfig

		ln, err := lc.Listen(ctx, &sctp.SCTPAddr{IPAddrs: []net.IPAddr{{IP: addr.Addr().AsSlice()}}, Port: int(addr.Port())})
		if err != nil {
			return nil, err
		}

		return diameter.NewSCTPListener(ln, nil), nil
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", addr.String())
	if err != nil {
		return nil, err
	}

	return diameter.NewTCPListener(ln.(*net.TCPListener)), nil
}

func closeListeners(lns []diameter.Listener) {
	for _, ln := range lns {
		_ = ln.Close()
	}
}

// diameterPeers are the peers of the Diameter node, each on the route of the applications it serves, to the realm
// of the application.
func diameterPeers(st settings.Settings) []diameter.Peer {
	out := make([]diameter.Peer, 0, len(st.Peers))

	for _, p := range st.Peers {
		apps := make([]diameter.Application, 0, len(p.Applications))
		routes := make([]diameter.Route, 0, len(p.Applications))

		for _, a := range p.Applications {
			apps = append(apps, applications[a])
			routes = append(routes, diameter.Route{Realm: st.Realm(a), Application: applications[a].ID, Priority: p.Priority})
		}

		out = append(out, diameter.Peer{
			ID:           p.ID,
			Host:         p.Host,
			Addresses:    []netip.Addr{p.Address},
			Transports:   []diameter.Transport{transports[p.Transport]},
			Applications: apps,
			Routes:       routes,
			Dial:         &diameter.Dial{Port: uint16(p.Port)},
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
		h.log.Warn("invalid Cx RTR", slog.String("peer", c.PeerID()), slog.Any("error", err))
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	t := h.target.Load()
	if t == nil {
		h.log.Warn("Cx RTR before the registrar started", slog.String("impi", rtr.PrivateIdentity))
		return cx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	h.log.Debug("Cx RTR", slog.String("impi", rtr.PrivateIdentity), slog.Any("impus", rtr.PublicIdentities),
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

type rxSessions interface {
	ReAuth(sessionID string, r rx.ReAuthRequest) bool
	AbortSession(sessionID string, r rx.AbortSessionRequest) (terminate func(), known bool)
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

// TS 29.214 §4.4.6.3
func (h *rxHandler) reAuth(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	session := tgpp.ParseEnvelope(req).SessionID

	rar, err := rx.ParseReAuthRequest(req)
	if err != nil {
		h.log.Warn("invalid Rx RAR", slog.String("peer", c.PeerID()), slog.String("session", session), slog.Any("error", err))
		return rx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	t := h.target.Load()
	if t == nil {
		h.log.Warn("Rx RAR before the P-CSCF started", slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	if !(*t).ReAuth(session, rar) {
		h.log.Debug("Rx RAR for an unknown session", slog.String("peer", c.PeerID()), slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnknownSessionID}, 0)
	}

	ans, err := rx.NewReAuthAnswer(req, c.LocalIdentity(), rx.ReAuthAnswer{})
	if err != nil {
		h.log.Warn("building the Rx RAA failed", slog.String("session", session), slog.Any("error", err))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	return ans
}

// TS 29.214 §4.4.6.1
func (h *rxHandler) abortSession(ctx context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	session := tgpp.ParseEnvelope(req).SessionID

	asr, err := rx.ParseAbortSessionRequest(req)
	if err != nil {
		h.log.Warn("invalid Rx ASR", slog.String("peer", c.PeerID()), slog.String("session", session), slog.Any("error", err))
		return rx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	t := h.target.Load()
	if t == nil {
		h.log.Warn("Rx ASR before the P-CSCF started", slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	terminate, known := (*t).AbortSession(session, asr)
	if !known {
		h.log.Debug("Rx ASR for an unknown session", slog.String("peer", c.PeerID()), slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnknownSessionID}, 0)
	}

	if !diameter.AfterAnswer(ctx, func(error) { terminate() }) {
		terminate()
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

	logger.Debug("Diameter peer state changed", attrs...)
}
