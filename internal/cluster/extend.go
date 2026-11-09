package cluster

import "github.com/hashicorp/raft"

func (n *Node) Unreachable(nodeID string) bool {
	n.mu.Lock()
	since := n.since
	n.mu.Unlock()

	if leading, _ := n.Leader(); !leading {
		return false
	}

	limit := max(lastContactLimit, 2*n.electionTimeout())
	now := n.cfg.Now()

	if now.Sub(since) < limit {
		return false
	}

	seen, ok := n.health.lastSeen(raft.ServerID(nodeID))

	return !ok || now.Sub(seen) >= limit
}
