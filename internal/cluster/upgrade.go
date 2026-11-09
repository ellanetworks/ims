package cluster

import (
	"context"
	"log/slog"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

const upgradeInterval = 10 * time.Second

func (n *Node) runUpgrades(ctx context.Context, _ uint64) {
	ticker := time.NewTicker(upgradeInterval)
	defer ticker.Stop()

	for {
		for n.upgrade(ctx) {
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func nextSchema(current, own int, servers []raft.Server, members []db.Member) (int, bool) {
	target := current + 1
	if target > own {
		return 0, false
	}

	supported := make(map[raft.ServerID]int, len(members))
	for _, m := range members {
		supported[raft.ServerID(m.NodeID)] = m.SchemaVersion
	}

	for _, s := range servers {
		if supported[s.ID] < target {
			return 0, false
		}
	}

	return target, true
}

func (n *Node) upgrade(ctx context.Context) bool {
	r, _ := n.current()

	future := r.GetConfiguration()
	if future.Error() != nil {
		return false
	}

	members, err := n.db.Members(ctx)
	if err != nil {
		return false
	}

	for i, m := range members {
		if m.NodeID == n.id {
			members[i].SchemaVersion = db.SchemaVersion()
		}
	}

	target, ok := nextSchema(n.db.Schema(), db.SchemaVersion(), future.Configuration().Servers, members)
	if !ok {
		return false
	}

	if err := n.db.Migrate(ctx, target); err != nil {
		if ctx.Err() == nil {
			n.cfg.Logger.WarnContext(ctx, "failed to upgrade the cluster's schema", slog.Int("schema", target),
				slog.Any("error", err))
		}

		return false
	}

	n.cfg.Logger.InfoContext(ctx, "upgraded the cluster's schema", slog.Int("schema", target))

	return true
}
