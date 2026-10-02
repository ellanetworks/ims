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

func newDiameterNode(cfg config.Diameter, rtr *rtrHandler, logger *slog.Logger) (*diameter.Node, error) {
	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      cfg.OriginHost,
			OriginRealm:     cfg.OriginRealm,
			HostIPAddresses: []netip.Addr{cfg.Address},
			ProductName:     productName,
		},
		Handler:           newDiameterMux(rtr, logger),
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

// newDiameterMux registers the requests the HSS and the PCRF send. RTR goes
// to the registrar once it exists. Until calls handle them, the Rx requests
// are refused with DIAMETER_UNABLE_TO_COMPLY.
func newDiameterMux(rtr *rtrHandler, logger *slog.Logger) *diameter.Mux {
	unableToComply := tgpp.Result{Code: diameter.ResultUnableToComply}

	mux := diameter.NewMux()

	mux.Handle(cx.ApplicationID, cx.CommandRegistrationTermination, rtr)
	mux.Handle(rx.ApplicationID, rx.CommandReAuth, unimplemented(logger, "Rx RAR",
		func(c *diameter.Conn, req *diameter.Message) *diameter.Message {
			return rx.NewAnswer(req, c.LocalIdentity(), unableToComply, 0)
		}))
	mux.Handle(rx.ApplicationID, rx.CommandAbortSession, unimplemented(logger, "Rx ASR",
		func(c *diameter.Conn, req *diameter.Message) *diameter.Message {
			return rx.NewAnswer(req, c.LocalIdentity(), unableToComply, 0)
		}))

	return mux
}

type terminator interface {
	Terminate(ctx context.Context, rtr cx.RegistrationTerminationRequest) ([]string, error)
}

// rtrHandler answers Registration-Termination-Requests (TS 29.228 §6.1.3)
// through the registrar bound to it, and DIAMETER_UNABLE_TO_COMPLY until then.
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

	// Associated-Identities lists the private identities de-registered
	// together with the User-Name's (TS 29.228 Table 6.1.3.2).
	a := cx.RegistrationTermination{AssociatedIdentities: associated}

	ans, err := cx.NewRegistrationTerminationAnswer(req, c.LocalIdentity(), a)
	if err != nil {
		h.log.Warn("building the RTA failed", slog.String("impi", rtr.PrivateIdentity), slog.Any("error", err))
		return cx.NewAnswer(req, c.LocalIdentity(), tgpp.Result{Code: diameter.ResultUnableToComply}, 0)
	}

	return ans
}

func unimplemented(logger *slog.Logger, name string, answer func(*diameter.Conn, *diameter.Message) *diameter.Message) diameter.Handler {
	return diameter.HandlerFunc(func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
		var session string
		if a, ok := req.Find(diameter.AVPSessionID, 0); ok {
			session = a.UTF8String()
		}

		logger.Warn("refusing unsupported Diameter request",
			slog.String("request", name), slog.String("peer", c.PeerID()), slog.String("session", session))

		return answer(c, req)
	})
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
