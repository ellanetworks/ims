package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/sh"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/sctp"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/scscf"
	"github.com/ellanetworks/ims/internal/settings"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const productName = "ims"

const DefaultHandshakeTimeout = 15 * time.Second

var applications = map[settings.Application]diameter.Application{
	settings.ApplicationCx:  {ID: cx.ApplicationID, VendorID: tgpp.VendorID},
	settings.ApplicationRx:  {ID: rx.ApplicationID, VendorID: tgpp.VendorID},
	settings.ApplicationSh:  {ID: sh.ApplicationID, VendorID: tgpp.VendorID},
	settings.ApplicationS6c: {ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
	settings.ApplicationSGd: {ID: sgd.ApplicationID, VendorID: tgpp.VendorID},
}

var transports = map[settings.Transport]diameter.Transport{
	settings.TransportTCP:  diameter.TransportTCP,
	settings.TransportSCTP: diameter.TransportSCTP,
}

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

func newDiameterNode(cfg config.Diameter, s settings.Settings, handshake time.Duration, cxh *cxHandler,
	rxh *rxHandler, ish ipsmgwHandler, tp trace.TracerProvider, logger *slog.Logger,
) (*diameter.Node, error) {
	op := s.Operator

	if handshake <= 0 {
		handshake = DefaultHandshakeTimeout
	}

	if tp == nil {
		tp = noop.NewTracerProvider()
	}

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      op.DiameterHost(cfg.HostLabel),
			OriginRealm:     op.DiameterRealm(),
			HostIPAddresses: cfg.Addrs(),
			ProductName:     productName,
		},
		Handler:           newDiameterMux(cxh, rxh, ish),
		HandshakeTimeout:  handshake,
		OnPeerStateChange: func(p diameter.PeerStatus) { logPeerState(logger, p) },
		TracerProvider:    tp,
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

// RFC 6733 section 2.1
// RFC 6733 section 5.6.4
func listenDiameter(ctx context.Context, cfg config.Diameter, st settings.Settings) ([]diameter.Listener, error) {
	addrs := cfg.Addrs()
	port := uint16(cfg.Port)

	var lns []diameter.Listener

	for _, t := range st.Transports() {
		groups := [][]netip.Addr{addrs}

		if t == settings.TransportTCP {
			groups = make([][]netip.Addr, len(addrs))
			for i, a := range addrs {
				groups[i] = []netip.Addr{a}
			}
		}

		for _, g := range groups {
			ln, err := listenDiameterOn(ctx, t, g, port)
			if err != nil {
				closeListeners(lns)
				return nil, fmt.Errorf("listen for Diameter over %s on %s: %w", t, endpoints(g, port), err)
			}

			lns = append(lns, ln)
		}
	}

	return lns, nil
}

func endpoints(addrs []netip.Addr, port uint16) string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = netip.AddrPortFrom(a, port).String()
	}

	return strings.Join(out, ", ")
}

const probeTimeout = 5 * time.Second

func probeSCTP(ctx context.Context, addrs []netip.Addr) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	ln, err := listenDiameterOn(ctx, settings.TransportSCTP, addrs, 0)
	if err != nil {
		return err
	}

	return ln.Close()
}

func listenDiameterOn(ctx context.Context, t settings.Transport, addrs []netip.Addr, port uint16) (diameter.Listener, error) {
	if t == settings.TransportSCTP {
		laddr := &sctp.SCTPAddr{Port: int(port)}
		for _, a := range addrs {
			laddr.IPAddrs = append(laddr.IPAddrs, net.IPAddr{IP: a.AsSlice()})
		}

		var lc sctp.ListenConfig

		ln, err := lc.Listen(ctx, laddr)
		if err != nil {
			return nil, err
		}

		return diameter.NewSCTPListener(ln, nil), nil
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", netip.AddrPortFrom(addrs[0], port).String())
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

func diameterPeers(st settings.Settings, previous ...settings.Settings) []diameter.Peer {
	out := make([]diameter.Peer, 0, len(st.Peers))

	for _, p := range st.Peers {
		apps := make([]diameter.Application, 0, len(p.Applications))
		routes := make([]diameter.Route, 0, len(p.Applications))

		for _, a := range p.Applications {
			apps = append(apps, applications[a])

			if !a.Routed() {
				continue
			}

			routes = append(routes, diameter.Route{Realm: st.Realm(a), Application: applications[a].ID, Priority: p.Priority})

			for _, prev := range previous {
				if realm := prev.Realm(a); realm != "" && !strings.EqualFold(realm, st.Realm(a)) {
					routes = append(routes, diameter.Route{Realm: realm, Application: applications[a].ID, Priority: p.Priority})
				}
			}
		}

		peer := diameter.Peer{
			ID:           p.ID,
			Host:         p.Host,
			Transports:   []diameter.Transport{transports[p.Transport]},
			Applications: apps,
			Routes:       routes,
			Dial:         &diameter.Dial{Port: uint16(p.Port)},
		}

		if a, err := netip.ParseAddr(p.Address); err == nil {
			peer.Addresses = []netip.Addr{a}
		} else {
			peer.Name = p.Address
		}

		out = append(out, peer)
	}

	return out
}

func newDiameterMux(cxh *cxHandler, rxh *rxHandler, ish ipsmgwHandler) *diameter.Mux {
	mux := diameter.NewMux()

	if ish.as != nil {
		mux.Handle(s6c.ApplicationID, s6c.CommandSendRoutingInfoForSM, diameter.HandlerFunc(ish.routingInfo))
		mux.Handle(sgd.ApplicationID, sgd.CommandMTForwardShortMessage, diameter.HandlerFunc(ish.forwardShortMessage))
	}

	mux.Handle(cx.ApplicationID, cx.CommandRegistrationTermination, diameter.HandlerFunc(cxh.registrationTermination))
	mux.Handle(cx.ApplicationID, cx.CommandPushProfile, diameter.HandlerFunc(cxh.pushProfile))
	mux.Handle(rx.ApplicationID, rx.CommandReAuth, diameter.HandlerFunc(rxh.reAuth))
	mux.Handle(rx.ApplicationID, rx.CommandAbortSession, diameter.HandlerFunc(rxh.abortSession))

	return mux
}

type cxRegistrar interface {
	Terminate(ctx context.Context, rtr cx.RegistrationTerminationRequest) ([]string, error)
	PushProfile(ctx context.Context, ppr cx.PushProfileRequest) error
}

type cxHandler struct {
	log    *slog.Logger
	target atomic.Pointer[cxRegistrar]
}

func newCxHandler(logger *slog.Logger) *cxHandler {
	return &cxHandler{log: logger}
}

func (h *cxHandler) bind(t cxRegistrar) {
	if t == nil {
		h.target.Store(nil)
		return
	}

	h.target.Store(&t)
}

func (h *cxHandler) registrationTermination(ctx context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	rtr, err := cx.ParseRegistrationTerminationRequest(req)
	if err != nil {
		h.log.WarnContext(ctx, "invalid Cx RTR", slog.String("peer", c.PeerID()), slog.Any("error", err))
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	t := h.target.Load()
	if t == nil {
		h.log.WarnContext(ctx, "Cx RTR before the registrar started", slog.String("impi", rtr.PrivateIdentity))
		return cx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	if ans := featureUnsupported(req, c); ans != nil {
		h.log.WarnContext(ctx, "Cx RTR requires features the S-CSCF does not support", slog.String("impi", rtr.PrivateIdentity))
		return ans
	}

	h.log.DebugContext(ctx, "Cx RTR", slog.String("impi", rtr.PrivateIdentity), slog.Any("impus", rtr.PublicIdentities),
		slog.String("reason", rtr.Reason.Code.String()))

	associated, err := (*t).Terminate(ctx, rtr)
	if err != nil {
		h.log.WarnContext(ctx, "Cx RTR failed", slog.String("impi", rtr.PrivateIdentity), slog.Any("error", err))
		return cx.NewAnswer(req, c.LocalIdentity(), failure(err), 0)
	}

	a := cx.RegistrationTermination{AssociatedIdentities: associated}

	ans, err := cx.NewRegistrationTerminationAnswer(req, c.LocalIdentity(), a)
	if err != nil {
		h.log.WarnContext(ctx, "building the RTA failed", slog.String("impi", rtr.PrivateIdentity), slog.Any("error", err))
		return cx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	return ans
}

// TS 29.228 clause 6.2.2.1
func (h *cxHandler) pushProfile(ctx context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	ppr, err := cx.ParsePushProfileRequest(req)
	if err != nil {
		h.log.WarnContext(ctx, "invalid Cx PPR", slog.String("peer", c.PeerID()), slog.Any("error", err))
		return cx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	t := h.target.Load()
	if t == nil {
		h.log.WarnContext(ctx, "Cx PPR before the registrar started", slog.String("impi", ppr.PrivateIdentity))
		return cx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	if ans := featureUnsupported(req, c); ans != nil {
		h.log.WarnContext(ctx, "Cx PPR requires features the S-CSCF does not support", slog.String("impi", ppr.PrivateIdentity))
		return ans
	}

	h.log.DebugContext(ctx, "Cx PPR", slog.String("impi", ppr.PrivateIdentity), slog.Bool("userData", len(ppr.UserData) > 0),
		slog.Bool("charging", ppr.Charging != nil), slog.Bool("allowedWebRTC", ppr.AllowedWebRTC != nil))

	if _, ok := req.Find(cx.AVPSIPAuthDataItem, tgpp.VendorID); ok {
		h.log.DebugContext(ctx, "Cx PPR: SIP Digest authentication data ignored", slog.String("impi", ppr.PrivateIdentity))
	}

	if err := (*t).PushProfile(ctx, ppr); err != nil {
		h.log.WarnContext(ctx, "Cx PPR failed", slog.String("impi", ppr.PrivateIdentity), slog.Any("error", err))
		return cx.NewAnswer(req, c.LocalIdentity(), failure(err), 0)
	}

	ans, err := cx.NewPushProfileAnswer(req, c.LocalIdentity(), cx.PushProfile{})
	if err != nil {
		h.log.WarnContext(ctx, "building the PPA failed", slog.String("impi", ppr.PrivateIdentity), slog.Any("error", err))
		return cx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	return ans
}

// TS 29.228 clause 6.1.3.1
// TS 29.228 clause 6.2.2.1 table 6.2.2.1.1
func failure(err error) tgpp.Result {
	switch {
	case errors.Is(err, scscf.ErrUserUnknown):
		return tgpp.Experimental(tgpp.ResultErrorUserUnknown)
	case errors.Is(err, scscf.ErrUnsupportedUserData):
		return tgpp.Experimental(tgpp.ResultErrorNotSupportedUserData)
	default:
		return tgpp.Result{Code: diameter.ResultUnableToComply}
	}
}

// TS 29.229 clause 7.2.1
func featureUnsupported(req *diameter.Message, c *diameter.Conn) *diameter.Message {
	if !cx.RequiresUnsupportedFeatures(req, 0) {
		return nil
	}

	ans := cx.NewAnswer(req, c.LocalIdentity(), tgpp.Experimental(tgpp.ResultErrorFeatureUnsupported), 0)
	ans.AVPs = append(ans.AVPs, tgpp.SupportedFeatures{VendorID: tgpp.VendorID, FeatureListID: cxFeatureListID}.AVP())

	return ans
}

// TS 29.229 clause 7.1.1
const cxFeatureListID = 1

type rxSessions interface {
	ReAuth(ctx context.Context, sessionID string, r rx.ReAuthRequest) bool
	AbortSession(ctx context.Context, sessionID string, r rx.AbortSessionRequest) (terminate func(), known bool)
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

// TS 29.214 clause 4.4.6.3
func (h *rxHandler) reAuth(ctx context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	session := tgpp.ParseEnvelope(req).SessionID

	rar, err := rx.ParseReAuthRequest(req)
	if err != nil {
		h.log.WarnContext(ctx, "invalid Rx RAR", slog.String("peer", c.PeerID()), slog.String("session", session), slog.Any("error", err))
		return rx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	t := h.target.Load()
	if t == nil {
		h.log.WarnContext(ctx, "Rx RAR before the P-CSCF started", slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	if !(*t).ReAuth(ctx, session, rar) {
		h.log.DebugContext(ctx, "Rx RAR for an unknown session", slog.String("peer", c.PeerID()), slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnknownSessionID}, 0)
	}

	ans, err := rx.NewReAuthAnswer(req, c.LocalIdentity(), rx.ReAuthAnswer{})
	if err != nil {
		h.log.WarnContext(ctx, "building the Rx RAA failed", slog.String("session", session), slog.Any("error", err))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	return ans
}

// TS 29.214 clause 4.4.6.1
func (h *rxHandler) abortSession(ctx context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
	session := tgpp.ParseEnvelope(req).SessionID

	asr, err := rx.ParseAbortSessionRequest(req)
	if err != nil {
		h.log.WarnContext(ctx, "invalid Rx ASR", slog.String("peer", c.PeerID()), slog.String("session", session), slog.Any("error", err))
		return rx.NewErrorAnswer(req, c.LocalIdentity(), err, 0)
	}

	t := h.target.Load()
	if t == nil {
		h.log.WarnContext(ctx, "Rx ASR before the P-CSCF started", slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	terminate, known := (*t).AbortSession(ctx, session, asr)
	if !known {
		h.log.DebugContext(ctx, "Rx ASR for an unknown session", slog.String("peer", c.PeerID()), slog.String("session", session))
		return rx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnknownSessionID}, 0)
	}

	if !diameter.AfterAnswer(ctx, func(error) { terminate() }) {
		terminate()
	}

	ans, err := rx.NewAbortSessionAnswer(req, c.LocalIdentity(), rx.AbortSessionAnswer{})
	if err != nil {
		h.log.WarnContext(ctx, "building the Rx ASA failed", slog.String("session", session), slog.Any("error", err))
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

	if p.State == diameter.PeerReopen {
		logger.Info("Diameter peer reconnected, unused until three watchdog exchanges succeed", attrs...)
		return
	}

	logger.Info("Diameter peer state changed", attrs...)
}
