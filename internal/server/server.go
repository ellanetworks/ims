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

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
)

const (
	version               = "0.0.1"
	callHistoryPurgeEvery = time.Hour
)

var ErrAlreadyStarted = errors.New("server: already started")

type Server struct {
	Config config.Config
	Logger *slog.Logger

	database    *db.DB
	apiServer   *http.Server
	apiListener net.Listener
	stopPurge   context.CancelFunc
	purgeDone   chan struct{}
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

	if err := s.purgeCallHistory(ctx, database); err != nil {
		_ = database.Close()
		return err
	}

	var apiLC net.ListenConfig

	apiLn, err := apiLC.Listen(ctx, "tcp", netip.AddrPortFrom(cfg.API.Address, uint16(cfg.API.Port)).String())
	if err != nil {
		_ = database.Close()
		return fmt.Errorf("listen for the API: %w", err)
	}

	s.database = database
	s.apiListener = apiLn
	s.apiServer = &http.Server{
		Handler: api.NewHandler(api.Config{
			Version: version,
			Logger:  s.Logger,
		}),
		ErrorLog:          slog.NewLogLogger(s.Logger.Handler(), slog.LevelWarn),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       time.Minute,
		WriteTimeout:      time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	go func() { _ = s.apiServer.Serve(apiLn) }()

	purgeCtx, stopPurge := context.WithCancel(context.Background())
	s.stopPurge = stopPurge
	s.purgeDone = make(chan struct{})

	go s.runCallHistoryPurge(purgeCtx)

	s.Logger.Info("ims started", "api", apiLn.Addr().String())

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

	s.stopPurge()
	<-s.purgeDone

	if err := s.database.Close(); err != nil {
		s.Logger.Warn("failed to close the database", slog.Any("error", err))
	}
}

func (s *Server) runCallHistoryPurge(ctx context.Context) {
	defer close(s.purgeDone)

	ticker := time.NewTicker(callHistoryPurgeEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.purgeCallHistory(ctx, s.database); err != nil {
				s.Logger.Warn("failed to purge call history", slog.Any("error", err))
			}
		}
	}
}

func (s *Server) purgeCallHistory(ctx context.Context, database *db.DB) error {
	n, err := database.DeleteCallsEndedBefore(ctx, time.Now().Add(-s.Config.CallHistory.Retention))
	if err != nil {
		return err
	}

	if n > 0 {
		s.Logger.Info("purged call history", "calls", n)
	}

	return nil
}
