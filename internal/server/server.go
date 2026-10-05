package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/pcscf"
)

const version = "0.0.1"

var ErrAlreadyStarted = errors.New("server: already started")

type Server struct {
	Config config.Config
	Logger *slog.Logger

	IPsec pcscf.Kernel

	DiameterHandshakeTimeout time.Duration

	database    *db.DB
	node        *diameter.Node
	diameterLns []diameter.Listener
	sip         *sipServer
	apiServer   *http.Server
	apiListener net.Listener
}

func (s *Server) Start(ctx context.Context) error {
	if s.apiServer != nil {
		return ErrAlreadyStarted
	}

	if s.Logger == nil {
		s.Logger = slog.Default()
	}

	cfg := s.Config

	database, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		return err
	}

	var apiLC net.ListenConfig

	apiLn, err := apiLC.Listen(ctx, "tcp", netip.AddrPortFrom(cfg.API.Address, uint16(cfg.API.Port)).String())
	if err != nil {
		_ = database.Close()
		return fmt.Errorf("listen for the API: %w", err)
	}

	diameterLns, err := listenDiameter(ctx, cfg.Diameter)
	if err != nil {
		_ = apiLn.Close()
		_ = database.Close()

		return err
	}

	rtr := newRTRHandler(s.Logger)
	rxh := newRxHandler(s.Logger)

	node, err := newDiameterNode(cfg.Diameter, s.DiameterHandshakeTimeout, rtr, rxh, s.Logger)
	if err != nil {
		closeListeners(diameterLns)

		_ = apiLn.Close()
		_ = database.Close()

		return fmt.Errorf("start Diameter: %w", err)
	}

	for _, ln := range diameterLns {
		go func() {
			if err := node.Serve(ln); !errors.Is(err, diameter.ErrClosed) && !errors.Is(err, net.ErrClosed) {
				s.Logger.Warn("Diameter listener stopped", slog.String("address", ln.Addr().String()), slog.Any("error", err))
			}
		}()
	}

	sipServer, err := startSIP(ctx, cfg, node, rtr, rxh, database, s.IPsec, s.Logger)
	if err != nil {
		_ = node.Shutdown(ctx)

		closeListeners(diameterLns)

		_ = apiLn.Close()
		_ = database.Close()

		return fmt.Errorf("start SIP: %w", err)
	}

	s.database = database
	s.node = node
	s.diameterLns = diameterLns
	s.sip = sipServer
	s.apiListener = apiLn
	s.apiServer = &http.Server{
		Handler: api.NewHandler(api.Config{
			Version:       version,
			Diameter:      s.node,
			SIP:           s.sip,
			Registrations: s.sip,
			HomeDomain:    cfg.IMS.HomeDomain,
			SIPAliases:    cfg.SIPAliases(),
			Logger:        s.Logger,
		}),
		ErrorLog:          slog.NewLogLogger(s.Logger.Handler(), slog.LevelWarn),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       time.Minute,
		WriteTimeout:      time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	go func() { _ = s.apiServer.Serve(apiLn) }()

	sipAttrs := make([]string, 0, len(sipServer.Listeners()))
	for _, l := range sipServer.Listeners() {
		sipAttrs = append(sipAttrs, l.Role+" "+l.Address.String())
	}

	diameterAttrs := make([]string, 0, len(diameterLns))
	for _, ln := range diameterLns {
		diameterAttrs = append(diameterAttrs, ln.Addr().Network()+" "+ln.Addr().String())
	}

	s.Logger.Info("ims started", slog.String("api", apiLn.Addr().String()), slog.Any("sip", sipAttrs),
		slog.Any("diameter", diameterAttrs))

	return nil
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

	if err := s.sip.Close(); err != nil {
		s.Logger.Warn("failed to stop SIP cleanly", slog.Any("error", err))
	}

	if err := s.node.ShutdownWithCause(ctx, diameter.DisconnectCauseRebooting); err != nil {
		s.Logger.Warn("failed to stop Diameter cleanly", slog.Any("error", err))
	}

	// Serve may not have registered a listener with the node yet.
	closeListeners(s.diameterLns)

	if err := s.database.Close(); err != nil {
		s.Logger.Warn("failed to close the database", slog.Any("error", err))
	}
}
