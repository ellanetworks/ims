package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/pki"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"
	autopilot "github.com/hashicorp/raft-autopilot"
)

const (
	reportInterval   = time.Second
	maintainInterval = 10 * time.Second
	tokenCleanup     = time.Hour
	maxVoters        = 5
	stabilization    = 10 * time.Second
	lastContactLimit = 10 * time.Second
	maxTrailingLogs  = 5000
)

type report struct {
	Address      string `json:"address"`
	AppliedIndex uint64 `json:"applied_index"`
	LastTerm     uint64 `json:"last_term"`
	LastIndex    uint64 `json:"last_index"`
	Schema       int    `json:"schema"`
	Version      string `json:"version"`
	Label        string `json:"diameter_label,omitempty"`
	Generated    bool   `json:"label_generated,omitempty"`
	Hash         []byte `json:"hash,omitempty"`
}

type reportReply struct {
	CommitIndex uint64   `json:"commit_index"`
	Diverged    bool     `json:"diverged,omitempty"`
	Voters      *bool    `json:"voters_healthy,omitempty"`
	Unhealthy   []string `json:"unhealthy_voters,omitempty"`
}

type leaderView struct {
	reportReply
	At time.Time
}

type peerReport struct {
	report
	At       time.Time
	Diverged bool
}

type health struct {
	n       *Node
	mu      sync.Mutex
	reports map[string]peerReport
	state   *autopilot.State
	view    leaderView
}

func newHealth(n *Node) *health {
	return &health{n: n, reports: map[string]peerReport{}}
}

func (h *health) record(nodeID string, r report) {
	if nodeID == "" {
		return
	}

	diverged := false

	if mine, ok := h.n.db.HashAt(r.AppliedIndex); ok && r.Hash != nil && !bytes.Equal(mine, r.Hash) {
		diverged = true
	}

	h.mu.Lock()
	previous := h.reports[nodeID].Diverged
	h.reports[nodeID] = peerReport{report: r, At: h.n.cfg.Now(), Diverged: diverged || previous}
	h.mu.Unlock()

	if diverged && !previous {
		h.n.metrics.mismatches.Inc()
		h.n.cfg.Logger.Error("a member's database differs from the leader's: remove it and join it again with an empty data directory",
			slog.String("node", nodeID), slog.Uint64("index", r.AppliedIndex))
	}
}

func (h *health) voters() (bool, []string) {
	h.mu.Lock()
	state := h.state
	h.mu.Unlock()

	if state == nil {
		return false, nil
	}

	var unhealthy []string

	for id, s := range state.Servers {
		if s.HasVotingRights() && !s.Health.Healthy {
			unhealthy = append(unhealthy, string(id))
		}
	}

	slices.Sort(unhealthy)

	return true, unhealthy
}

func (h *health) reply(nodeID string, commitIndex uint64) reportReply {
	r, _ := h.get(nodeID)
	out := reportReply{CommitIndex: commitIndex, Diverged: r.Diverged}

	if known, unhealthy := h.voters(); known {
		healthy := len(unhealthy) == 0
		out.Voters, out.Unhealthy = &healthy, unhealthy
	}

	return out
}

func (h *health) setView(r reportReply) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.view = leaderView{reportReply: r, At: h.n.cfg.Now()}
}

func (h *health) leaderView() leaderView {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.view
}

func (h *health) get(nodeID string) (peerReport, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	r, ok := h.reports[nodeID]

	return r, ok
}

type ServerHealth struct {
	NodeID       string    `json:"node_id"`
	Address      string    `json:"address"`
	Suffrage     string    `json:"suffrage"`
	Leader       bool      `json:"leader"`
	Healthy      bool      `json:"healthy"`
	StableSince  time.Time `json:"stable_since,omitzero"`
	LastContact  string    `json:"last_contact,omitempty"`
	AppliedIndex uint64    `json:"applied_index"`
	Version      string    `json:"version,omitempty"`
	Diverged     bool      `json:"diverged,omitempty"`
}

type Health struct {
	Healthy          bool           `json:"healthy"`
	FailureTolerance int            `json:"failure_tolerance"`
	Servers          []ServerHealth `json:"servers"`
}

func (h *health) snapshot() Health {
	h.mu.Lock()
	state := h.state
	h.mu.Unlock()

	if state == nil {
		return Health{}
	}

	out := Health{Healthy: state.Healthy, FailureTolerance: state.FailureTolerance}

	for id, s := range state.Servers {
		sh := ServerHealth{
			NodeID: string(id), Address: string(s.Server.Address), Suffrage: suffrageName(s.State),
			Leader: s.State == autopilot.RaftLeader, Healthy: s.Health.Healthy, StableSince: s.Health.StableSince,
			AppliedIndex: s.Stats.LastIndex,
		}

		if s.State != autopilot.RaftLeader {
			sh.LastContact = s.Stats.LastContact.String()
		}

		if r, ok := h.get(string(id)); ok {
			sh.Version, sh.AppliedIndex, sh.Diverged = r.Version, r.AppliedIndex, r.Diverged
		}

		out.Servers = append(out.Servers, sh)
	}

	slices.SortFunc(out.Servers, func(a, b ServerHealth) int { return compareStrings(a.NodeID, b.NodeID) })

	return out
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}

	return 0
}

func suffrageName(s autopilot.RaftState) string {
	switch s {
	case autopilot.RaftLeader, autopilot.RaftVoter:
		return "voter"
	case autopilot.RaftNonVoter:
		return "nonvoter"
	}

	return "staging"
}

type pilot struct {
	n *Node
	r *raft.Raft
}

func (p *pilot) AutopilotConfig() *autopilot.Config {
	return &autopilot.Config{
		CleanupDeadServers:      false,
		LastContactThreshold:    max(lastContactLimit, 2*p.n.electionTimeout()),
		MaxTrailingLogs:         maxTrailingLogs,
		ServerStabilizationTime: stabilization,
	}
}

func (p *pilot) NotifyState(s *autopilot.State) {
	p.n.health.mu.Lock()
	p.n.health.state = s
	p.n.health.mu.Unlock()
}

func (p *pilot) FetchServerStats(_ context.Context, servers map[raft.ServerID]*autopilot.Server) map[raft.ServerID]*autopilot.ServerStats {
	out := make(map[raft.ServerID]*autopilot.ServerStats, len(servers))
	now := p.n.cfg.Now()

	for id := range servers {
		if string(id) == p.n.id {
			stats := p.r.Stats()
			out[id] = &autopilot.ServerStats{
				LastTerm:  parseUint(stats["last_log_term"]),
				LastIndex: parseUint(stats["last_log_index"]),
			}

			continue
		}

		r, ok := p.n.health.get(string(id))
		if !ok {
			continue
		}

		out[id] = &autopilot.ServerStats{LastContact: now.Sub(r.At), LastTerm: r.LastTerm, LastIndex: r.LastIndex}
	}

	return out
}

func parseUint(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

func (p *pilot) KnownServers() map[raft.ServerID]*autopilot.Server {
	future := p.r.GetConfiguration()
	if err := future.Error(); err != nil {
		return nil
	}

	out := map[raft.ServerID]*autopilot.Server{}

	for _, s := range future.Configuration().Servers {
		out[s.ID] = &autopilot.Server{
			ID: s.ID, Name: string(s.ID), Address: s.Address, NodeStatus: autopilot.NodeAlive,
			Version: "", IsLeader: string(s.ID) == p.n.id,
		}
	}

	return out
}

func (p *pilot) RemoveFailedServer(*autopilot.Server) {}

type cappedPromoter struct {
	autopilot.StablePromoter

	draining func(raft.ServerID) bool
}

func (p cappedPromoter) isDraining(id raft.ServerID) bool {
	return p.draining != nil && p.draining(id)
}

func (p cappedPromoter) CalculatePromotionsAndDemotions(c *autopilot.Config, s *autopilot.State) autopilot.RaftChanges {
	var (
		changes                  autopilot.RaftChanges
		standbys, failed, others []raft.ServerID
		active, nonvoters        int
	)

	now := time.Now()
	stable := s.ServerStabilizationTime(c)

	leaderDraining := false

	for id, srv := range s.Servers {
		draining := p.isDraining(id)

		switch {
		case srv.HasVotingRights():
			leaderDraining = leaderDraining || draining
			active++

			switch {
			case srv.State == autopilot.RaftLeader:
			case !srv.Health.Healthy && now.Sub(srv.Health.StableSince) >= stable:
				failed = append(failed, id)
			default:
				others = append(others, id)
			}
		case srv.State == autopilot.RaftNonVoter && !draining:
			nonvoters++

			if srv.Health.IsStable(now, stable) {
				standbys = append(standbys, id)
			}
		}
	}

	slices.Sort(standbys)
	slices.Sort(failed)
	slices.Sort(others)

	target := min(maxVoters, active+nonvoters)
	if target%2 == 0 {
		target--
	}

	for active < target && len(standbys) > 0 {
		changes.Promotions = append(changes.Promotions, standbys[0])
		standbys = standbys[1:]
		active++
	}

	switch {
	case len(changes.Promotions) > 0:
	case leaderDraining:
		if active < 2 && len(standbys) > 0 {
			changes.Promotions = append(changes.Promotions, standbys[0])
		}
	case active > target:
		if victims := append(failed, others...); len(victims) > 0 {
			changes.Demotions = append(changes.Demotions, victims[0])
		}
	case len(failed) > 0 && len(standbys) > 0:
		changes.Demotions = append(changes.Demotions, failed[0])
		changes.Promotions = append(changes.Promotions, standbys[0])
	}

	return changes
}

func (n *Node) runAutopilot(ctx context.Context, _ uint64) {
	if !n.cfg.clustered() {
		return
	}

	r, _ := n.current()

	ap := autopilot.New(r, &pilot{n: n, r: r}, autopilot.WithLogger(hclog.NewNullLogger()),
		autopilot.WithPromoter(&cappedPromoter{draining: n.isDraining}), autopilot.WithReconcileInterval(time.Second),
		autopilot.WithUpdateInterval(time.Second))

	ap.Start(ctx)

	<-ctx.Done()

	<-ap.Stop()
}

func (n *Node) isDraining(id raft.ServerID) bool {
	m, err := n.db.Member(context.Background(), string(id))

	return err != nil || m.DrainState != db.DrainActive
}

func (n *Node) onEvents(ev db.Events) {
	if ev.Members || ev.Replaced {
		if err := n.certs.refresh(context.Background(), n.db); err != nil {
			n.cfg.Logger.Error("failed to reload the cluster pins", slog.Any("error", err))
		}

		if n.ln != nil {
			n.ln.Revalidate()
		}
	}

	if slices.Contains(ev.Removed, n.id) {
		n.markRemoved()
	}
}

func (n *Node) maintain() {
	report := time.NewTicker(reportInterval)
	defer report.Stop()

	maintain := time.NewTicker(maintainInterval)
	defer maintain.Stop()

	var lastCleanup time.Time

	for {
		select {
		case <-n.done:
			return
		case <-n.removed:
			n.stepDown()

			if err := n.stopRaft(); err != nil {
				n.cfg.Logger.Warn("failed to stop raft after the removal", slog.Any("error", err))
			}

			<-n.done

			return
		case <-report.C:
			if leading, _ := n.Leader(); !leading && n.peers != nil {
				n.sendReport()
			}
		case <-maintain.C:
			leading, _ := n.Leader()
			if !leading {
				continue
			}

			n.registerMembers()

			if now := n.cfg.Now(); now.Sub(lastCleanup) >= tokenCleanup {
				lastCleanup = now

				ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
				if err := n.db.DeleteExpiredJoinTokens(ctx, now); err != nil {
					n.cfg.Logger.Warn("failed to delete expired join tokens", slog.Any("error", err))
				}

				cancel()
			}
		}
	}
}

func (n *Node) sendReport() {
	r, _ := n.current()
	stats := r.Stats()

	ctx, cancel := context.WithTimeout(context.Background(), reportInterval)
	defer cancel()

	rep := report{
		Address: n.cfg.Advertise, AppliedIndex: n.db.AppliedIndex(), LastTerm: parseUint(stats["last_log_term"]),
		LastIndex: parseUint(stats["last_log_index"]), Schema: db.SchemaVersion(), Version: n.cfg.Version,
		Label: n.label, Generated: n.generated,
	}

	if h, ok := n.db.HashAt(rep.AppliedIndex); ok {
		rep.Hash = h
	}

	if _, id := r.LeaderWithID(); id == "" {
		n.reportToServers(ctx, r, rep)
		return
	}

	var reply reportReply

	if err := n.peers.call(ctx, "/cluster/v1/report", rep, &reply); err != nil {
		n.cfg.Logger.DebugContext(ctx, "failed to report to the leader", slog.Any("error", err))
		return
	}

	n.health.setView(reply)
}

func (n *Node) reportToServers(ctx context.Context, r *raft.Raft, rep report) {
	future := r.GetConfiguration()
	if future.Error() != nil {
		return
	}

	var wg sync.WaitGroup

	for _, s := range future.Configuration().Servers {
		if string(s.ID) == n.id {
			continue
		}

		wg.Go(func() {
			var reply reportReply

			if err := n.peers.callAt(ctx, string(s.Address), string(s.ID), "/cluster/v1/report", rep, &reply); err == nil {
				n.health.setView(reply)
			}
		})
	}

	wg.Wait()
}

func (n *Node) registerSelf(ctx context.Context, via db.Proposer) error {
	secrets, err := n.db.InitSecrets(ctx, via)
	if err != nil {
		return err
	}

	address := ""

	if n.cfg.clustered() {
		address = n.cfg.Advertise

		if n.certs.clusterID() != secrets.ClusterID {
			cert, err := pki.GenerateCert(secrets.ClusterID, n.id, n.cfg.Now())
			if err != nil {
				return err
			}

			if err := n.certs.install(cert, nil); err != nil {
				return err
			}
		}

		if err := n.addServer(raft.ServerID(n.id), raft.ServerAddress(address)); err != nil {
			return fmt.Errorf("advertise %s: %w", address, err)
		}
	}

	want := n.certs.member(address, n.cfg.Version, n.label, n.generated)

	have, err := n.db.Member(ctx, n.id)
	if err == nil && sameMember(have, want) {
		return nil
	}

	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}

	return n.db.RegisterMemberVia(ctx, via, want)
}

func sameMember(a, b db.Member) bool {
	return a.Address == b.Address && a.Fingerprint == b.Fingerprint && a.SchemaVersion == b.SchemaVersion &&
		a.Version == b.Version && a.DiameterLabel == b.DiameterLabel && a.LabelGenerated == b.LabelGenerated
}

func (n *Node) registerMembers() {
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	members, err := n.db.Members(ctx)
	if err != nil {
		return
	}

	for _, m := range members {
		if m.NodeID == n.id {
			continue
		}

		r, ok := n.health.get(m.NodeID)
		if !ok || r.Address == "" {
			continue
		}

		want := m
		want.Address, want.SchemaVersion, want.Version = r.Address, r.Schema, r.Version

		if r.Label != "" {
			want.DiameterLabel, want.LabelGenerated = r.Label, r.Generated
		}

		if sameMember(m, want) {
			continue
		}

		if err := n.db.RegisterMember(ctx, want); err != nil {
			n.cfg.Logger.Warn("failed to update a member", slog.String("node", m.NodeID), slog.Any("error", err))
			continue
		}

		if want.Address != m.Address {
			if err := n.addServer(raft.ServerID(m.NodeID), raft.ServerAddress(want.Address)); err != nil {
				n.cfg.Logger.Warn("failed to update a member's address", slog.String("node", m.NodeID),
					slog.Any("error", err))
			}
		}
	}
}

var ErrNotDrained = errors.New("drain the member before removing it, or force the removal")

func (n *Node) RemoveMember(ctx context.Context, nodeID string, force bool) error {
	ctx, cancel := context.WithTimeout(ctx, n.forwardBudget())
	defer cancel()

	for attempt := 0; ; attempt++ {
		var err error

		if leading, _ := n.Leader(); leading {
			err = n.removeAsLeader(ctx, nodeID, force)
		} else if n.peers != nil {
			err = n.peers.call(ctx, "/cluster/v1/remove", removeRequest{NodeID: nodeID, Force: force}, nil)
		} else {
			err = fmt.Errorf("%w: no leader", errRetry)
		}

		if !errors.Is(err, errRetry) {
			return err
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrNoLeader, err)
		case <-time.After(backoff(attempt)):
		}
	}
}

func (n *Node) removeAsLeader(ctx context.Context, nodeID string, force bool) error {
	r, b := n.current()
	if r == nil || b == nil {
		return fmt.Errorf("%w: raft is restarting", errRetry)
	}

	if !force {
		m, err := n.db.Member(ctx, nodeID)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return err
		}

		if err == nil && m.DrainState != db.DrainDrained {
			return ErrNotDrained
		}
	}

	if nodeID == n.id {
		if err := r.LeadershipTransfer().Error(); err != nil {
			return fmt.Errorf("hand over the leadership before leaving: %w", err)
		}

		return fmt.Errorf("%w: leadership handed over", errRetry)
	}

	future := r.GetConfiguration()
	if err := future.Error(); err != nil {
		return classify(err)
	}

	servers := future.Configuration().Servers

	i := slices.IndexFunc(servers, func(s raft.Server) bool { return string(s.ID) == nodeID })
	inRaft := i >= 0

	if inRaft && servers[i].Suffrage == raft.Voter {
		if err := n.keepsQuorum(servers, nodeID); err != nil {
			return err
		}

		if err := r.DemoteVoter(raft.ServerID(nodeID), 0, addServerTimeout).Error(); err != nil {
			return classify(err)
		}
	}

	reason := db.RemovedDrained
	if force {
		reason = db.RemovedForced
	}

	err := n.db.RemoveMember(ctx, nodeID, reason)

	switch {
	case errors.Is(err, db.ErrNotFound) && !inRaft:
		return err
	case err != nil && !errors.Is(err, db.ErrNotFound):
		return err
	}

	if inRaft {
		if err := r.RemoveServer(raft.ServerID(nodeID), 0, addServerTimeout).Error(); err != nil {
			return classify(err)
		}
	}

	n.health.mu.Lock()
	delete(n.health.reports, nodeID)
	n.health.mu.Unlock()

	n.cfg.Logger.InfoContext(ctx, "node removed from the cluster", slog.String("node", nodeID))

	return nil
}

var ErrQuorum = errors.New("removing this voter would leave too few healthy voters to commit writes")

func (n *Node) keepsQuorum(servers []raft.Server, removed string) error {
	now := n.cfg.Now()
	limit := max(lastContactLimit, 2*n.electionTimeout())
	voters, healthy := 0, 0

	for _, s := range servers {
		if s.Suffrage != raft.Voter || string(s.ID) == removed {
			continue
		}

		voters++

		if string(s.ID) == n.id {
			healthy++
			continue
		}

		if r, ok := n.health.get(string(s.ID)); ok && now.Sub(r.At) < limit {
			healthy++
		}
	}

	if healthy <= voters/2 {
		return fmt.Errorf("%w: %d of the %d remaining voters answer", ErrQuorum, healthy, voters)
	}

	return nil
}
