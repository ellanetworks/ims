package cluster

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

	r, ok := n.health.get(nodeID)

	return !ok || now.Sub(r.At) >= limit
}
