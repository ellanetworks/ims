package callrecords

import (
	"context"
	"log/slog"
	"time"
)

const (
	// MaxRecords is how many call records are kept at most: past it, the oldest are deleted, however recent.
	MaxRecords = 1_000_000

	DefaultPruneInterval = time.Hour
)

type Pruner interface {
	PruneCallRecords(ctx context.Context, before time.Time, maxRows int) (int64, error)
}

type PruneConfig struct {
	Store Pruner
	// Retention returns how long records are kept, when they are pruned, so that it can change at runtime.
	Retention func() time.Duration
	// MaxRecords is how many records are kept at most, MaxRecords if 0.
	MaxRecords int
	// Interval is how often records are pruned, DefaultPruneInterval if 0.
	Interval time.Duration
	Clock    Clock
	Logger   *slog.Logger
}

// Prune deletes the records requested longer than the retention ago, and the oldest past the most kept, at once
// and then at each interval, until ctx is done.
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
		n, err := cfg.Store.PruneCallRecords(ctx, cfg.Clock.Now().Add(-cfg.Retention()), cfg.MaxRecords)

		switch {
		case err != nil && ctx.Err() == nil:
			cfg.Logger.Error("pruning call records failed", slog.Any("error", err))
		case n > 0:
			cfg.Logger.Info("pruned call records", slog.Int64("records", n))
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
