package cluster

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
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
	selfInterval     = time.Second
	announceTimeout  = 2 * time.Second
	maintainInterval = 10 * time.Second
	tokenCleanup     = time.Hour
	maxVoters        = 5
	stabilization    = 10 * time.Second
	lastContactLimit = 10 * time.Second
	maxTrailingLogs  = 5000
)

type peerState struct {
	lastContact time.Time
	healthy     bool
}

type health struct {
	n     *Node
	mu    sync.Mutex
	peers map[raft.ServerID]*peerState
	state *autopilot.State
}

func newHealth(n *Node) *health {
	return &health{n: n, peers: map[raft.ServerID]*peerState{}}
}

func (h *health) track(ctx context.Context, r *raft.Raft) {
	now := h.n.cfg.Now()
	peers := map[raft.ServerID]*peerState{}

	if future := r.GetConfiguration(); future.Error() == nil {
		for _, srv := range future.Configuration().Servers {
			if string(srv.ID) != h.n.id {
				peers[srv.ID] = &peerState{lastContact: now, healthy: true}
			}
		}
	}

	h.mu.Lock()
	h.peers = peers
	h.mu.Unlock()

	ch := make(chan raft.Observation, 64)
	observer := raft.NewObserver(ch, false, func(o *raft.Observation) bool {
		switch o.Data.(type) {
		case raft.FailedHeartbeatObservation, raft.ResumedHeartbeatObservation, raft.PeerObservation:
			return true
		}

		return false
	})

	r.RegisterObserver(observer)
	defer r.DeregisterObserver(observer)

	defer func() {
		h.mu.Lock()
		h.peers, h.state = map[raft.ServerID]*peerState{}, nil
		h.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case o := <-ch:
			h.observe(o)
		}
	}
}

func (h *health) observe(o raft.Observation) {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch v := o.Data.(type) {
	case raft.FailedHeartbeatObservation:
		if p, ok := h.peers[v.PeerID]; ok {
			if p.healthy {
				h.n.cfg.Logger.Warn("a cluster member stopped answering heartbeats", slog.String("node", string(v.PeerID)))
			}

			p.healthy, p.lastContact = false, v.LastContact
		}
	case raft.ResumedHeartbeatObservation:
		if p, ok := h.peers[v.PeerID]; ok {
			p.healthy, p.lastContact = true, h.n.cfg.Now()
		}
	case raft.PeerObservation:
		switch {
		case v.Removed:
			delete(h.peers, v.Peer.ID)
		case string(v.Peer.ID) != h.n.id && h.peers[v.Peer.ID] == nil:
			h.peers[v.Peer.ID] = &peerState{lastContact: h.n.cfg.Now(), healthy: true}
		}
	}
}

func (h *health) contact(id raft.ServerID) (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, ok := h.peers[id]

	switch {
	case !ok:
		return 0, false
	case p.healthy:
		return 0, true
	}

	return h.n.cfg.Now().Sub(p.lastContact), true
}

func (h *health) lastSeen(id raft.ServerID) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, ok := h.peers[id]

	switch {
	case !ok:
		return time.Time{}, false
	case p.healthy:
		return h.n.cfg.Now(), true
	}

	return p.lastContact, true
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

type ServerHealth struct {
	NodeID      string    `json:"node_id"`
	Address     string    `json:"address"`
	Suffrage    string    `json:"suffrage"`
	Leader      bool      `json:"leader"`
	Healthy     bool      `json:"healthy"`
	StableSince time.Time `json:"stable_since,omitzero"`
	LastContact string    `json:"last_contact,omitempty"`
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
		}

		if s.State != autopilot.RaftLeader {
			sh.LastContact = s.Stats.LastContact.String()
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
	stats := p.r.Stats()
	term, index := parseUint(stats["last_log_term"]), parseUint(stats["last_log_index"])

	out := make(map[raft.ServerID]*autopilot.ServerStats, len(servers))

	for id := range servers {
		if string(id) == p.n.id {
			out[id] = &autopilot.ServerStats{LastTerm: term, LastIndex: index}
			continue
		}

		lastContact, known := p.n.health.contact(id)
		if !known {
			continue
		}

		out[id] = &autopilot.ServerStats{LastContact: lastContact, LastTerm: term, LastIndex: index}
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

	var tracking sync.WaitGroup

	tracking.Go(func() { n.health.track(ctx, r) })

	ap := autopilot.New(r, &pilot{n: n, r: r}, autopilot.WithLogger(hclog.NewNullLogger()),
		autopilot.WithPromoter(&cappedPromoter{draining: n.isDraining}),
		autopilot.WithReconcileInterval(time.Second), autopilot.WithUpdateInterval(time.Second))

	ap.Start(ctx)

	<-ctx.Done()

	<-ap.Stop()

	tracking.Wait()
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
	maintain := time.NewTicker(selfInterval)
	defer maintain.Stop()

	var lastCleanup, lastReconcile, lastProbe time.Time

	lastLeader := n.cfg.Now()

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
		case <-maintain.C:
		}

		now := n.cfg.Now()

		if n.formation.current() != FormationMember || n.hasLeader() {
			lastLeader = now
		} else if now.Sub(lastLeader) >= cmp.Or(n.cfg.removalAfter, removalAfter) && now.Sub(lastProbe) >= removalProbe {
			lastProbe = now

			ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
			n.probeRemoval(ctx)
			cancel()
		}

		n.updateSelf()

		if leading, _ := n.Leader(); !leading {
			continue
		}

		if now.Sub(lastReconcile) < maintainInterval {
			continue
		}

		lastReconcile = now

		n.reconcileAddresses()

		if now.Sub(lastCleanup) >= tokenCleanup {
			lastCleanup = now

			ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
			if err := n.db.DeleteExpiredJoinTokens(ctx, now); err != nil {
				n.cfg.Logger.Warn("failed to delete expired join tokens", slog.Any("error", err))
			}

			cancel()
		}
	}
}

func (n *Node) updateSelf() {
	r, _ := n.current()
	if r == nil || n.peers == nil {
		return
	}

	if _, id := r.LeaderWithID(); id == "" {
		n.announceAddress(r)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	have, err := n.db.Member(ctx, n.id)
	if err != nil {
		return
	}

	want := n.certs.member(n.cfg.Advertise, n.cfg.Version, n.label, n.generated)
	want.APIAddress, want.SCSCFAddresses, want.MCPTTAddresses = n.cfg.APIAddress, n.scscfAddresses(), n.mcpttAddresses()

	if sameMember(have, want) {
		return
	}

	if err := n.db.RegisterMember(ctx, want); err != nil {
		n.cfg.Logger.Warn("failed to update this node's member record", slog.Any("error", err))
	}
}

func (n *Node) announceAddress(r *raft.Raft) {
	if n.formation.current() != FormationMember {
		return
	}

	future := r.GetConfiguration()
	if future.Error() != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), announceTimeout)
	defer cancel()

	have, err := n.db.Member(ctx, n.id)
	if err != nil || have.Address == "" || have.Address == n.cfg.Advertise {
		return
	}

	want := have
	want.Address = n.cfg.Advertise

	for _, s := range future.Configuration().Servers {
		if string(s.ID) == n.id {
			continue
		}

		via := peerProposer{peers: n.peers, addr: string(s.Address), id: string(s.ID)}
		if err := n.registerVia(via, want); err == nil {
			n.cfg.Logger.Info("announced this node's new cluster address", slog.String("address", want.Address))
			return
		}
	}
}

func (n *Node) registerVia(via db.Proposer, m db.Member) error {
	ctx, cancel := context.WithTimeout(context.Background(), announceTimeout)
	defer cancel()

	return n.db.RegisterMemberVia(ctx, via, m)
}

type peerProposer struct {
	peers    *peerClient
	addr, id string
}

func (p peerProposer) Propose(ctx context.Context, cmds []db.Command) ([]db.Result, error) {
	var reply proposeReply

	if err := p.peers.callAt(ctx, p.addr, p.id, "/cluster/v1/propose", proposeRequest{Commands: cmds}, &reply); err != nil {
		return nil, err
	}

	if reply.Busy {
		return nil, db.ErrBusy
	}

	return reply.Results, nil
}

func (n *Node) reconcileAddresses() {
	r, _ := n.current()

	future := r.GetConfiguration()
	if future.Error() != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	members, err := n.db.Members(ctx)
	if err != nil {
		return
	}

	for _, s := range future.Configuration().Servers {
		i := slices.IndexFunc(members, func(m db.Member) bool { return m.NodeID == string(s.ID) })
		if i < 0 || members[i].Address == "" || members[i].Address == string(s.Address) {
			continue
		}

		if err := n.addServer(s.ID, raft.ServerAddress(members[i].Address)); err != nil {
			n.cfg.Logger.Warn("failed to update a member's address", slog.String("node", string(s.ID)),
				slog.Any("error", err))
		}
	}
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
	want.APIAddress, want.SCSCFAddresses = n.cfg.APIAddress, n.scscfAddresses()
	want.MCPTTAddresses = n.mcpttAddresses()

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
		a.Version == b.Version && a.DiameterLabel == b.DiameterLabel && a.LabelGenerated == b.LabelGenerated &&
		a.APIAddress == b.APIAddress && slices.Equal(a.SCSCFAddresses, b.SCSCFAddresses) &&
		slices.Equal(a.MCPTTAddresses, b.MCPTTAddresses)
}

func (n *Node) SetSCSCFAddresses(addrs []netip.AddrPort) {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}

	n.scscf.Store(&out)
}

func (n *Node) scscfAddresses() []string {
	if p := n.scscf.Load(); p != nil {
		return *p
	}

	return nil
}

func (n *Node) SetMCPTTAddresses(addrs []netip.AddrPort) {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}

	n.mcptt.Store(&out)
}

func (n *Node) mcpttAddresses() []string {
	if p := n.mcptt.Load(); p != nil {
		return *p
	}

	return nil
}

var ErrNotDrained = errors.New("drain the member before removing it, or force the removal")

func (n *Node) RemoveMember(ctx context.Context, nodeID string, force bool) error {
	if leading, _ := n.Leader(); !leading {
		return n.notLeader(ctx)
	}

	return n.removeAsLeader(ctx, nodeID, force)
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

		return n.awaitOtherLeader(ctx, r)
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

		if seen, ok := n.health.lastSeen(s.ID); ok && now.Sub(seen) < limit {
			healthy++
		}
	}

	if healthy <= voters/2 {
		return fmt.Errorf("%w: %d of the %d remaining voters answer", ErrQuorum, healthy, voters)
	}

	return nil
}
