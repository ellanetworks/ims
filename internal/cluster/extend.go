package cluster

import (
	"time"

	"github.com/hashicorp/raft"
)

func (n *Node) Unreachable(nodeID string) bool {
	return n.Unheard(nodeID, max(lastContactLimit, 2*n.electionTimeout()))
}

func (n *Node) Unheard(nodeID string, limit time.Duration) bool {
	n.mu.Lock()
	since := n.since
	n.mu.Unlock()

	if leading, _ := n.Leader(); !leading || nodeID == n.id {
		return false
	}

	now := n.cfg.Now()

	if now.Sub(since) < limit {
		return false
	}

	seen, ok := n.health.lastSeen(raft.ServerID(nodeID))

	return !ok || now.Sub(seen) >= limit
}
