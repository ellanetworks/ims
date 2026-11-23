package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/hashicorp/raft"
)

const healthTimeout = 2 * time.Second

type HealthState string

const (
	HealthHealthy  HealthState = "healthy"
	HealthDegraded HealthState = "degraded"
	HealthNoLeader HealthState = "no_leader"
	HealthUnknown  HealthState = "unknown"
)

type HealthSummary struct {
	State            HealthState `json:"state"`
	TotalVoters      int         `json:"total_voters"`
	HealthyVoters    *int        `json:"healthy_voters,omitempty"`
	FailureTolerance *int        `json:"failure_tolerance,omitempty"`
}

func (n *Node) HealthSummary(ctx context.Context) HealthSummary {
	if !n.cfg.clustered() {
		healthy, tolerance := 1, 0
		return HealthSummary{State: HealthHealthy, TotalVoters: 1, HealthyVoters: &healthy, FailureTolerance: &tolerance}
	}

	r, _ := n.current()
	if r == nil {
		return HealthSummary{State: HealthUnknown}
	}

	total := configuredVoters(r)

	if !n.hasLeader() {
		return HealthSummary{State: HealthNoLeader, TotalVoters: total}
	}

	h, err := n.leaderHealth(ctx, r)
	if err == nil && h.Servers == nil {
		err = errors.New("the leader has no health yet")
	}

	if err != nil {
		n.cfg.Logger.DebugContext(ctx, "failed to read the cluster health from the leader", slog.Any("error", err))
		return HealthSummary{State: HealthUnknown, TotalVoters: total}
	}

	return summarize(h)
}

func (n *Node) leaderHealth(ctx context.Context, r *raft.Raft) (Health, error) {
	if leading, _ := n.Leader(); leading {
		return n.health.snapshot(), nil
	}

	addr, id := r.LeaderWithID()

	switch {
	case id == "":
		return Health{}, errors.New("no leader")
	case string(id) == n.id:
		return Health{}, errors.New("the leader term is starting")
	case n.peers == nil:
		return Health{}, fmt.Errorf("leader %s is not reachable without a cluster address", id)
	}

	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	resp, err := n.peers.do(ctx, string(addr), string(id), http.MethodGet, "/cluster/v1/health", nil)
	if err != nil {
		return Health{}, err
	}

	defer func() { _ = resp.Body.Close() }()

	var h Health

	return h, decodeReply(resp, &h)
}

func (n *Node) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, n.health.snapshot())
}

func configuredVoters(r *raft.Raft) int {
	future := r.GetConfiguration()
	if future.Error() != nil {
		return 0
	}

	voters := 0

	for _, s := range future.Configuration().Servers {
		if s.Suffrage == raft.Voter {
			voters++
		}
	}

	return voters
}

func summarize(h Health) HealthSummary {
	voters, healthy := 0, 0

	for _, s := range h.Servers {
		if s.Suffrage != "voter" {
			continue
		}

		voters++

		if s.Healthy {
			healthy++
		}
	}

	state := HealthDegraded
	if h.Healthy {
		state = HealthHealthy
	}

	tolerance := h.FailureTolerance

	return HealthSummary{State: state, TotalVoters: voters, HealthyVoters: &healthy, FailureTolerance: &tolerance}
}
