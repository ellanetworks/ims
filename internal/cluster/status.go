package cluster

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/hashicorp/raft"
)

const (
	readyLag        = 1000
	leaderViewAge   = 3 * time.Second
	healthCallLimit = 2 * time.Second
)

type MemberStatus struct {
	NodeID        string    `json:"node_id"`
	Address       string    `json:"address"`
	DiameterLabel string    `json:"diameter_label"`
	DiameterHost  string    `json:"diameter_host"`
	Generated     bool      `json:"label_generated"`
	DisplayName   string    `json:"display_name"`
	Version       string    `json:"version"`
	JoinedAt      time.Time `json:"joined_at"`
	DrainState    string    `json:"drain_state"`
	Suffrage      string    `json:"suffrage"`
	Leader        bool      `json:"leader"`
	Healthy       *bool     `json:"healthy,omitempty"`
	LastContact   string    `json:"last_contact,omitempty"`
	AppliedIndex  uint64    `json:"applied_index,omitempty"`
	Diverged      bool      `json:"diverged,omitempty"`
}

type Status struct {
	NodeID           string         `json:"node_id"`
	ClusterID        string         `json:"cluster_id"`
	Clustered        bool           `json:"clustered"`
	LeaderID         string         `json:"leader_id,omitempty"`
	Term             uint64         `json:"term"`
	AppliedIndex     uint64         `json:"applied_index"`
	CommitIndex      uint64         `json:"commit_index"`
	Schema           int            `json:"schema"`
	Healthy          *bool          `json:"healthy,omitempty"`
	FailureTolerance *int           `json:"failure_tolerance,omitempty"`
	Members          []MemberStatus `json:"members"`
}

func (n *Node) Status(ctx context.Context) (Status, error) {
	st := Status{
		NodeID: n.id, Clustered: n.cfg.clustered(), AppliedIndex: n.db.AppliedIndex(), Schema: n.db.Schema(),
		Members: []MemberStatus{},
	}

	r, _ := n.current()

	var (
		leaderID raft.ServerID
		servers  []raft.Server
	)

	if r != nil {
		_, leaderID = r.LeaderWithID()
		st.LeaderID, st.Term, st.CommitIndex = string(leaderID), r.CurrentTerm(), r.CommitIndex()

		future := r.GetConfiguration()
		if err := future.Error(); err != nil && !errors.Is(err, raft.ErrRaftShutdown) {
			return Status{}, err
		}

		servers = future.Configuration().Servers
	}

	secrets, err := n.db.ReadSecrets(ctx)
	if err == nil {
		st.ClusterID = secrets.ClusterID
	}

	members, err := n.db.Members(ctx)
	if err != nil {
		return Status{}, err
	}

	s, err := n.db.GetSettings(ctx)
	if err != nil {
		return Status{}, err
	}

	h, known := Health{}, false
	if r != nil {
		h, known = n.leaderHealth(ctx)
	}

	if known {
		st.Healthy, st.FailureTolerance = &h.Healthy, &h.FailureTolerance
	}

	for _, m := range members {
		ms := MemberStatus{
			NodeID: m.NodeID, Address: m.Address, DiameterLabel: m.DiameterLabel,
			DiameterHost: s.Operator.DiameterHost(m.DiameterLabel), Generated: m.LabelGenerated, DisplayName: m.DisplayName,
			Version:  m.Version,
			JoinedAt: m.JoinedAt, DrainState: string(m.DrainState), Suffrage: "none", Leader: m.NodeID == string(leaderID),
		}

		if i := slices.IndexFunc(servers, func(s raft.Server) bool { return string(s.ID) == m.NodeID }); i >= 0 {
			ms.Suffrage = "voter"
			if servers[i].Suffrage != raft.Voter {
				ms.Suffrage = "nonvoter"
			}
		}

		if i := slices.IndexFunc(h.Servers, func(s ServerHealth) bool { return s.NodeID == m.NodeID }); i >= 0 {
			s := h.Servers[i]
			ms.Healthy, ms.LastContact, ms.AppliedIndex, ms.Diverged = &s.Healthy, s.LastContact, s.AppliedIndex, s.Diverged
		}

		st.Members = append(st.Members, ms)
	}

	return st, nil
}

func (n *Node) leaderHealth(ctx context.Context) (Health, bool) {
	if leading, _ := n.Leader(); leading {
		h := n.health.snapshot()
		return h, h.Servers != nil
	}

	if n.peers == nil {
		return Health{}, false
	}

	ctx, cancel := context.WithTimeout(ctx, healthCallLimit)
	defer cancel()

	addr, id, err := n.peers.leader()
	if err != nil {
		return Health{}, false
	}

	resp, err := n.peers.do(ctx, addr, id, "GET", "/cluster/v1/health", nil)
	if err != nil {
		return Health{}, false
	}

	defer func() { _ = resp.Body.Close() }()

	var h Health
	if err := decodeReply(resp, &h); err != nil {
		return Health{}, false
	}

	return h, h.Servers != nil
}

const (
	CheckMember      = "member"
	CheckInitialized = "initialized"
	CheckLabel       = "diameter_label"
	CheckDrain       = "drain"
	CheckConsistent  = "consistent"
	CheckDatabase    = "database"
	CheckLeader      = "leader"
	CheckCaughtUp    = "caught_up"
	CheckCluster     = "cluster"
)

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func pass(name string) Check {
	return Check{Name: name, OK: true}
}

func fail(name, detail string) Check {
	return Check{Name: name, Detail: detail}
}

func (n *Node) Readiness(ctx context.Context, withCluster bool) []Check {
	if r, _ := n.current(); r == nil {
		checks := []Check{n.checkMember(), n.checkDatabase(ctx)}
		if withCluster {
			checks = append(checks, fail(CheckCluster, ErrRemoved.Error()))
		}

		return checks
	}

	checks := []Check{
		n.checkMember(), n.checkInitialized(ctx), n.checkLabel(ctx), n.checkDrain(ctx), n.checkConsistent(), n.checkDatabase(ctx),
		n.checkLeader(), n.checkCaughtUp(),
	}

	if withCluster {
		checks = append(checks, n.checkCluster())
	}

	return checks
}

func (n *Node) checkMember() Check {
	select {
	case <-n.removed:
		return fail(CheckMember, ErrRemoved.Error())
	default:
	}

	if f := n.Formation(); f.State != FormationMember {
		detail := "the cluster is forming"
		if f.Detail != "" {
			detail += ": " + f.Detail
		}

		return fail(CheckMember, detail)
	}

	return pass(CheckMember)
}

func (n *Node) checkInitialized(ctx context.Context) Check {
	if !n.cfg.clustered() {
		return pass(CheckInitialized)
	}

	users, err := n.db.CountUsers(ctx)

	switch {
	case err != nil:
		return fail(CheckInitialized, "count the users: "+err.Error())
	case users == 0:
		return fail(CheckInitialized, "not initialized: create the first user on this node, or join it to a cluster")
	}

	return pass(CheckInitialized)
}

func (n *Node) CommittedLabel(ctx context.Context) (string, error) {
	m, err := n.db.Member(ctx, n.id)
	if err != nil {
		return "", err
	}

	if m.DiameterLabel != n.label {
		members, err := n.db.Members(ctx)
		if err != nil {
			return "", err
		}

		for _, other := range members {
			if other.NodeID != n.id && other.DiameterLabel == n.label {
				return "", fmt.Errorf("%w: the Diameter host label %s is member %s's: remove that member, or set another "+
					"diameter.host_label", settings.ErrConflict, n.label, other.NodeID)
			}
		}

		return "", fmt.Errorf("the cluster records the Diameter host label %s for this node, waiting for it to record %s",
			m.DiameterLabel, n.label)
	}

	return m.DiameterLabel, nil
}

func (n *Node) checkLabel(ctx context.Context) Check {
	if _, err := n.CommittedLabel(ctx); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return fail(CheckLabel, "this node is not registered as a cluster member yet")
		}

		return fail(CheckLabel, err.Error())
	}

	return pass(CheckLabel)
}

func (n *Node) checkDrain(ctx context.Context) Check {
	m, err := n.db.Member(ctx, n.id)

	switch {
	case errors.Is(err, db.ErrNotFound):
		return pass(CheckDrain)
	case err != nil:
		return fail(CheckDrain, "read the drain state: "+err.Error())
	case m.DrainState != db.DrainActive:
		return fail(CheckDrain, "the node is "+string(m.DrainState))
	}

	return pass(CheckDrain)
}

func (n *Node) checkConsistent() Check {
	if r, _ := n.current(); r.State() == raft.Leader {
		return pass(CheckConsistent)
	}

	if n.health.leaderView().Diverged {
		return fail(CheckConsistent, "the leader found that this node's database differs from its own: remove the node, "+
			"delete its data directory and join it again")
	}

	return pass(CheckConsistent)
}

func (n *Node) checkDatabase(ctx context.Context) Check {
	if err := n.db.Ping(ctx); err != nil {
		return fail(CheckDatabase, err.Error())
	}

	return pass(CheckDatabase)
}

func (n *Node) checkLeader() Check {
	if leading, _ := n.Leader(); leading {
		return pass(CheckLeader)
	}

	r, _ := n.current()

	if r.State() == raft.Leader {
		return fail(CheckLeader, "this node leads, but its term is not established yet")
	}

	last := r.LastContact()
	if last.IsZero() {
		return fail(CheckLeader, "no contact with a cluster leader")
	}

	if age := n.cfg.Now().Sub(last); age > 2*n.electionTimeout() {
		return fail(CheckLeader, fmt.Sprintf("no contact with the cluster leader for %s", age.Round(time.Second)))
	}

	return pass(CheckLeader)
}

func (n *Node) ApplyLag() (uint64, bool) {
	applied := n.db.AppliedIndex()

	r, _ := n.current()
	if r == nil {
		return 0, false
	}

	if r.State() == raft.Leader {
		commit := r.CommitIndex()
		return commit - min(commit, applied), true
	}

	view := n.health.leaderView()
	if view.At.IsZero() || n.cfg.Now().Sub(view.At) > leaderViewAge {
		return 0, false
	}

	return view.CommitIndex - min(view.CommitIndex, applied), true
}

func (n *Node) checkCaughtUp() Check {
	lag, known := n.ApplyLag()

	switch {
	case !known:
		return fail(CheckCaughtUp, "no recent commit index from the cluster leader")
	case lag > cmp.Or(n.cfg.readyLag, readyLag):
		return fail(CheckCaughtUp, fmt.Sprintf("the database is %d entries behind the cluster leader", lag))
	}

	return pass(CheckCaughtUp)
}

func (n *Node) checkCluster() Check {
	if !n.cfg.clustered() {
		return pass(CheckCluster)
	}

	var (
		known     bool
		unhealthy []string
	)

	if leading, _ := n.Leader(); leading {
		known, unhealthy = n.health.voters()
	} else if view := n.health.leaderView(); !view.At.IsZero() && n.cfg.Now().Sub(view.At) <= leaderViewAge &&
		view.Voters != nil {
		known, unhealthy = true, view.Unhealthy
	}

	switch {
	case !known:
		return fail(CheckCluster, "no recent view of the voters from the cluster leader")
	case len(unhealthy) > 0:
		return fail(CheckCluster, "unhealthy voters: "+strings.Join(unhealthy, ", "))
	}

	return pass(CheckCluster)
}

func (n *Node) HandOver() error {
	if leading, _ := n.Leader(); !leading {
		return nil
	}

	r, _ := n.current()

	future := r.GetConfiguration()
	if err := future.Error(); err != nil {
		return err
	}

	for _, s := range future.Configuration().Servers {
		if s.Suffrage == raft.Voter && string(s.ID) != n.id && !n.isDraining(s.ID) {
			return r.LeadershipTransferToServer(s.ID, s.Address).Error()
		}
	}

	return nil
}

func (n *Node) RaftLeader() bool {
	r, _ := n.current()

	return r != nil && r.State() == raft.Leader
}

var ErrLastMember = errors.New("the only member of the cluster cannot be drained")

func (n *Node) SetDrainState(ctx context.Context, nodeID string, state db.DrainState) error {
	if state == db.DrainDraining {
		members, err := n.db.Members(ctx)
		if err != nil {
			return err
		}

		if len(members) < 2 {
			return ErrLastMember
		}
	}

	return n.db.SetDrainState(ctx, nodeID, state)
}

func (n *Node) SetDisplayName(ctx context.Context, nodeID, name string) error {
	return n.db.SetDisplayName(ctx, nodeID, name)
}

func (n *Node) DisplayName(ctx context.Context) string {
	m, err := n.db.Member(ctx, n.id)
	if err != nil {
		return ""
	}

	return m.DisplayName
}
