package cluster

import (
	"context"
	"time"

	"github.com/hashicorp/raft"
)

type LeaderError struct {
	NodeID     string
	APIAddress string
}

func (e *LeaderError) Error() string {
	if e.APIAddress == "" {
		return "not the leader: the leader is unknown"
	}

	return "not the leader: the leader is " + e.APIAddress
}

func (e *LeaderError) Unwrap() error {
	return ErrNotLeader
}

func (n *Node) notLeader(ctx context.Context) error {
	e := &LeaderError{}

	r, _ := n.current()
	if r == nil {
		return e
	}

	_, id := r.LeaderWithID()
	if id == "" || string(id) == n.id {
		return e
	}

	e.NodeID = string(id)

	if m, err := n.db.Member(ctx, string(id)); err == nil {
		e.APIAddress = m.APIAddress
	}

	return e
}

func (n *Node) awaitOtherLeader(ctx context.Context, r *raft.Raft) error {
	ctx, cancel := context.WithTimeout(ctx, n.forwardBudget())
	defer cancel()

	for attempt := 0; ; attempt++ {
		if _, id := r.LeaderWithID(); id != "" && string(id) != n.id {
			return n.notLeader(ctx)
		}

		select {
		case <-ctx.Done():
			return n.notLeader(context.WithoutCancel(ctx))
		case <-time.After(backoff(attempt)):
		}
	}
}
