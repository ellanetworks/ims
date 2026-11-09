package server

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/internal/cluster"
	"github.com/ellanetworks/ims/internal/db"
)

const drainTick = time.Second

type drainer struct {
	node     *cluster.Node
	store    *db.DB
	refusing *atomic.Bool
	logger   *slog.Logger
	wake     chan struct{}
}

func (d *drainer) changed() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *drainer) run(ctx context.Context) {
	ticker := time.NewTicker(drainTick)
	defer ticker.Stop()

	for {
		d.reconcile(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.wake:
		}
	}
}

// TS 24.229 clause 5.2.2.7
func (d *drainer) reconcile(ctx context.Context) {
	m, err := d.store.Member(ctx, d.node.ID())
	if err != nil {
		return
	}

	draining := m.DrainState != db.DrainActive

	if d.refusing.Swap(draining) != draining {
		if draining {
			d.logger.InfoContext(ctx, "draining: the P-CSCF answers REGISTER with 305 (Use Proxy), so that phones register through another node")
		} else {
			d.logger.InfoContext(ctx, "resumed: the P-CSCF accepts registrations again")
		}
	}

	if !draining {
		return
	}

	if err := d.node.HandOver(); err != nil {
		d.logger.WarnContext(ctx, "draining: failed to hand the leadership over", slog.Any("error", err))
		return
	}

	if d.node.RaftLeader() || m.DrainState == db.DrainDrained {
		return
	}

	if err := d.store.SetDrainState(ctx, d.node.ID(), db.DrainDrained); err != nil {
		d.logger.WarnContext(ctx, "draining: failed to record the drained state", slog.Any("error", err))
		return
	}

	d.logger.InfoContext(ctx, "drained")
}
