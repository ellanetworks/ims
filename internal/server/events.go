package server

import (
	"context"
	"log/slog"
	"sync"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/oidc"
)

type events struct {
	mu      sync.Mutex
	pending db.Events
	wake    chan struct{}
}

func (s *Server) followEvents(ctx context.Context, database *db.DB, oidcLive *oidc.Live) func() {
	e := &events{wake: make(chan struct{}, 1)}

	database.OnEvents(func(ev db.Events) {
		e.mu.Lock()
		e.pending.Settings = e.pending.Settings || ev.Settings || ev.Replaced
		e.pending.OIDC = e.pending.OIDC || ev.OIDC || ev.Replaced
		e.pending.MCPTT = e.pending.MCPTT || ev.MCPTT || ev.Replaced
		e.mu.Unlock()

		select {
		case e.wake <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		for {
			select {
			case <-ctx.Done():
				return
			case <-e.wake:
			}

			e.mu.Lock()
			ev := e.pending
			e.pending = db.Events{}
			e.mu.Unlock()

			s.applyEvents(ctx, ev, database, oidcLive)
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

func (s *Server) applyEvents(ctx context.Context, ev db.Events, database *db.DB, oidcLive *oidc.Live) {
	if ev.Settings {
		current, err := database.GetSettings(ctx)
		if err != nil {
			s.Logger.ErrorContext(ctx, "failed to reload the settings", slog.Any("error", err))
		} else {
			s.settings.Reload(current)
		}
	}

	if ev.OIDC {
		current, err := database.GetOIDC(ctx)
		if err != nil {
			s.Logger.ErrorContext(ctx, "failed to reload the OIDC settings", slog.Any("error", err))
		} else {
			oidcLive.Reload(current)
		}
	}

	if ev.MCPTT {
		if a := s.mcptt.Load(); a != nil {
			if err := a.Reload(ctx); err != nil {
				s.Logger.ErrorContext(ctx, "failed to reload the MCPTT settings", slog.Any("error", err))
			}
		}
	}
}
