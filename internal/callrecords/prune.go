package callrecords

import (
	"context"
	"log/slog"
	"time"
)

const (
	MaxRecords = 1_000_000

	DefaultPruneInterval = time.Hour
)

type Pruner interface {
	PruneCallRecords(ctx context.Context, before time.Time, maxRows int) (int64, error)
}

type PruneConfig struct {
	Store      Pruner
	Retention  func() time.Duration
	MaxRecords int
	Interval   time.Duration
	Active     func() bool
	Clock      Clock
	Logger     *slog.Logger
}

func Prune(ctx context.Context, cfg PruneConfig) {
	if cfg.MaxRecords <= 0 {
		cfg.MaxRecords = MaxRecords
	}

	if cfg.Interval <= 0 {
		cfg.Interval = DefaultPruneInterval
	}

	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		if cfg.Active == nil || cfg.Active() {
			n, err := cfg.Store.PruneCallRecords(ctx, cfg.Clock.Now().Add(-cfg.Retention()), cfg.MaxRecords)

			switch {
			case err != nil && ctx.Err() == nil:
				cfg.Logger.ErrorContext(ctx, "pruning call records failed", slog.Any("error", err))
			case n > 0:
				cfg.Logger.InfoContext(ctx, "pruned call records", slog.Int64("records", n))
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
