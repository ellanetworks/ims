package cluster

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/pki"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

const (
	FormationForming = "forming"
	FormationJoining = "joining"
	FormationMember  = "member"
	FormationRemoved = "removed"

	helloForming = "forming"
	helloMember  = "member"
	helloRemoved = "removed"

	discoverInterval = 2 * time.Second
	foreignInterval  = 30 * time.Second
	removalAfter     = 60 * time.Second
	removalProbe     = 10 * time.Second
	joinMethodSecret = "secret"
)

type Formation struct {
	State     string `json:"state"`
	Expected  int    `json:"expected,omitempty"`
	Found     int    `json:"found,omitempty"`
	Candidate string `json:"-"`
	Detail    string `json:"detail,omitempty"`
}

type formation struct {
	n *Node

	mu        sync.Mutex
	state     string
	detail    string
	found     int
	candidate string
	formed    chan struct{}
	logged    string
}

func newFormation(n *Node) *formation {
	return &formation{n: n, formed: make(chan struct{})}
}

func (f *formation) set(state, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.state, f.detail = state, detail

	if state == FormationMember {
		f.found, f.candidate = 0, ""

		select {
		case <-f.formed:
		default:
			close(f.formed)
		}
	}
}

func (f *formation) progress(found int, candidate, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.found, f.candidate, f.detail = found, candidate, detail
}

func (f *formation) setForeign(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.n.metrics.foreignPeers.Set(float64(count))
}

func (f *formation) get() Formation {
	f.mu.Lock()
	defer f.mu.Unlock()

	return Formation{
		State: f.state, Expected: f.n.cfg.BootstrapExpect, Found: f.found, Candidate: f.candidate, Detail: f.detail,
	}
}

func (f *formation) current() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.state
}

func (f *formation) logOnce(key string, log func()) {
	f.mu.Lock()
	changed := f.logged != key
	f.logged = key
	f.mu.Unlock()

	if changed {
		log()
	}
}

func (n *Node) Formation() Formation {
	return n.formation.get()
}

func (n *Node) Formed() <-chan struct{} {
	return n.formation.formed
}

type hello struct {
	NodeID    string `json:"node_id"`
	Address   string `json:"address"`
	State     string `json:"state"`
	ClusterID string `json:"cluster_id,omitempty"`
	Expect    int    `json:"bootstrap_expect,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	Schema    int    `json:"schema"`
	Protocol  int    `json:"protocol"`
	Label     string `json:"diameter_label"`
	Leader    bool   `json:"leader,omitempty"`
	Member    *bool  `json:"member,omitempty"`
}

func (n *Node) selfHello(ctx context.Context) hello {
	h := hello{
		NodeID: n.id, Address: n.cfg.Advertise, Expect: n.cfg.BootstrapExpect, Schema: db.SchemaVersion(),
		Protocol: db.ProtocolVersion, Label: n.label,
	}

	f := n.formation.get()

	switch f.State {
	case FormationRemoved:
		h.State = helloRemoved

		if secrets, err := n.db.ReadSecrets(ctx); err == nil {
			h.ClusterID = secrets.ClusterID
		}
	case FormationMember:
		if secrets, err := n.db.ReadSecrets(ctx); err == nil {
			h.State, h.ClusterID = helloMember, secrets.ClusterID
		}
	default:
		h.State, h.Candidate = helloForming, f.Candidate
	}

	h.Leader, _ = n.Leader()

	return h
}

func (n *Node) handleHello(r *http.Request, c caller, body []byte) (int, any) {
	var in hello
	if err := json.Unmarshal(body, &in); err != nil || in.NodeID != c.NodeID {
		return http.StatusBadRequest, rpcError{Error: "invalid hello", Code: "invalid"}
	}

	out := n.selfHello(r.Context())

	if out.ClusterID != "" && in.ClusterID != "" && in.ClusterID != out.ClusterID {
		n.cfg.Logger.WarnContext(r.Context(), "a node of another cluster sent a hello: check the cluster.join names of both clusters",
			slog.String("node", in.NodeID), slog.String("address", in.Address), slog.String("cluster", in.ClusterID))
	}

	if out.Leader && in.ClusterID != "" && in.ClusterID == out.ClusterID {
		_, err := n.db.Member(r.Context(), in.NodeID)
		known := err == nil
		out.Member = &known
	}

	return http.StatusOK, out
}

type action int

const (
	actionWait action = iota
	actionJoin
	actionBootstrap
)

type decision struct {
	Action    action
	Members   []string
	ClusterID string
	Found     int
	Candidate string
	Detail    string
	Blocked   bool
}

func decide(self hello, peers []hello) decision {
	var (
		forming []hello
		members []hello
	)

	clusters := map[string]bool{}

	for _, p := range peers {
		if p.NodeID == self.NodeID {
			continue
		}

		switch p.State {
		case helloMember:
			members = append(members, p)
			clusters[p.ClusterID] = true
		case helloForming:
			forming = append(forming, p)
		}
	}

	if len(clusters) > 1 {
		return decision{Blocked: true, Detail: fmt.Sprintf("the peers belong to %d different clusters", len(clusters))}
	}

	if len(members) > 0 {
		slices.SortFunc(members, func(a, b hello) int {
			switch {
			case a.Leader && !b.Leader:
				return -1
			case b.Leader && !a.Leader:
				return 1
			}

			return strings.Compare(a.NodeID, b.NodeID)
		})

		d := decision{Action: actionJoin, ClusterID: members[0].ClusterID, Detail: "joining cluster " + members[0].ClusterID}

		for _, m := range members {
			d.Members = append(d.Members, m.Address)
		}

		return d
	}

	if self.Expect == 0 {
		return decision{Detail: "waiting for a cluster member to answer"}
	}

	group := append([]hello{self}, forming...)
	labels := map[string]string{}

	for _, p := range group {
		switch {
		case p.Expect != self.Expect:
			return decision{Blocked: true, Detail: fmt.Sprintf("node %s expects %d nodes, this node %d: set the same "+
				"cluster.bootstrap_expect on every node", p.NodeID, p.Expect, self.Expect)}
		case p.Schema != self.Schema || p.Protocol != self.Protocol:
			return decision{Blocked: true, Detail: fmt.Sprintf("node %s runs schema %d and protocol %d, this node %d and %d",
				p.NodeID, p.Schema, p.Protocol, self.Schema, self.Protocol)}
		}

		if other, ok := labels[p.Label]; ok {
			return decision{Blocked: true, Detail: fmt.Sprintf("nodes %s and %s have the same Diameter host label %s",
				other, p.NodeID, p.Label)}
		}

		labels[p.Label] = p.NodeID
	}

	d := decision{Found: len(group)}

	if len(group) < self.Expect {
		d.Detail = fmt.Sprintf("found %d of %d nodes", len(group), self.Expect)
		return d
	}

	d.Candidate = self.NodeID

	for _, p := range forming {
		d.Candidate = min(d.Candidate, p.NodeID)
	}

	if d.Candidate != self.NodeID {
		d.Detail = "waiting for node " + d.Candidate + " to form the cluster"
		return d
	}

	for _, p := range forming {
		if p.Candidate != self.NodeID {
			d.Detail = "waiting for every node to agree that this node forms the cluster"
			return d
		}
	}

	d.Action, d.Detail = actionBootstrap, "forming the cluster"

	return d
}

func (n *Node) resolveJoin(ctx context.Context) []string {
	var out []string

	seen := map[string]bool{}

	for _, target := range n.cfg.Join {
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			continue
		}

		var addrs []netip.Addr

		if ip, err := netip.ParseAddr(host); err == nil {
			addrs = []netip.Addr{ip}
		} else {
			rctx, cancel := context.WithTimeout(ctx, helloTimeout)
			addrs, err = net.DefaultResolver.LookupNetIP(rctx, "ip", host)

			cancel()

			if err != nil {
				n.cfg.Logger.DebugContext(ctx, "failed to resolve a cluster.join name", slog.String("name", host), slog.Any("error", err))
				continue
			}
		}

		for _, a := range addrs {
			addr := net.JoinHostPort(a.Unmap().String(), port)
			if !seen[addr] {
				seen[addr] = true
				out = append(out, addr)
			}
		}
	}

	return out
}

type round struct {
	peers       []hello
	wrongSecret int
}

func (n *Node) helloRound(ctx context.Context, targets []string) round {
	self := n.selfHello(ctx)

	var (
		mu  sync.Mutex
		out round
		wg  sync.WaitGroup
	)

	byID := map[string]bool{}

	for _, addr := range targets {
		wg.Go(func() {
			reply, err := n.secretCall(ctx, addr, "/cluster/v1/hello", self)

			mu.Lock()
			defer mu.Unlock()

			switch {
			case errors.Is(err, ErrWrongSecret), errors.Is(err, errBadProof):
				out.wrongSecret++
				return
			case err != nil:
				n.cfg.Logger.DebugContext(ctx, "cluster hello failed", slog.String("address", addr), slog.Any("error", err))
				return
			case reply.Status != http.StatusOK:
				return
			}

			var h hello
			if err := json.Unmarshal(reply.Body, &h); err != nil || h.NodeID != reply.NodeID || h.NodeID == n.id ||
				byID[h.NodeID] {
				return
			}

			byID[h.NodeID] = true
			out.peers = append(out.peers, h)
		})
	}

	wg.Wait()

	slices.SortFunc(out.peers, func(a, b hello) int { return strings.Compare(a.NodeID, b.NodeID) })

	return out
}

func (n *Node) discover() {
	if !n.auth.enabled() {
		if n.formation.current() == FormationForming {
			n.formation.progress(0, "", "no cluster secret is configured: set cluster.secret_file")
			n.cfg.Logger.Error("cluster.join is set, but no cluster secret is configured")
		}

		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		select {
		case <-n.done:
		case <-n.removed:
		}

		cancel()
	}()

	var lastForeign, lastProbe time.Time

	lastLeader := n.cfg.Now()

	for {
		wait := discoverInterval/2 + rand.N(discoverInterval)

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		switch n.formation.current() {
		case FormationForming, FormationJoining:
			n.formStep(ctx)
			lastLeader = n.cfg.Now()
		case FormationMember:
			now := n.cfg.Now()

			if n.hasLeader() {
				lastLeader = now
			}

			if len(n.cfg.Join) > 0 && now.Sub(lastForeign) >= foreignInterval {
				lastForeign = now

				n.countForeign(ctx)
			}

			if now.Sub(lastLeader) >= cmp.Or(n.cfg.removalAfter, removalAfter) && now.Sub(lastProbe) >= removalProbe {
				lastProbe = now

				n.probeRemoval(ctx)
			}
		default:
			return
		}
	}
}

func (n *Node) hasLeader() bool {
	if leading, _ := n.Leader(); leading {
		return true
	}

	r, _ := n.current()
	if r == nil {
		return false
	}

	_, id := r.LeaderWithID()

	return id != ""
}

func (n *Node) formStep(ctx context.Context) {
	rd := n.helloRound(ctx, n.resolveJoin(ctx))

	if rd.wrongSecret > 0 {
		n.formation.logOnce("wrong-secret", func() {
			n.cfg.Logger.WarnContext(ctx, "peers refused this node's cluster secret, or answered without proving it: check that every "+
				"node has the same cluster secret", slog.Int("peers", rd.wrongSecret))
		})
	}

	d := decide(n.selfHello(ctx), rd.peers)
	n.formation.progress(d.Found, d.Candidate, d.Detail)

	switch {
	case d.Blocked:
		n.formation.logOnce("blocked:"+d.Detail, func() {
			n.cfg.Logger.ErrorContext(ctx, "not forming the cluster: "+d.Detail, slog.Int("found", d.Found),
				slog.Int("expected", n.cfg.BootstrapExpect))
		})
	case d.Action == actionWait:
		n.formation.logOnce(fmt.Sprintf("wait:%d:%s", d.Found, d.Detail), func() {
			n.cfg.Logger.InfoContext(ctx, "waiting for the cluster to form", slog.Int("found", d.Found),
				slog.Int("expected", n.cfg.BootstrapExpect), slog.String("detail", d.Detail))
		})
	case d.Action == actionJoin:
		n.formJoin(ctx, d)
	case d.Action == actionBootstrap:
		n.formBootstrap(ctx)
	}
}

func (n *Node) formJoin(ctx context.Context, d decision) {
	n.formation.set(FormationJoining, d.Detail)

	err := n.joinSecret(ctx, d.ClusterID, d.Members)

	switch {
	case err == nil:
		n.cfg.Logger.InfoContext(ctx, "joined the cluster", slog.String("cluster", d.ClusterID))

		return
	case errors.Is(err, ErrWrongSecret), errors.Is(err, errBadProof):
	case errors.Is(err, errRetry):
	default:
	}

	if n.formation.current() == FormationJoining {
		n.formation.set(FormationForming, "joining cluster "+d.ClusterID+" failed: "+err.Error())
	}

	n.formation.logOnce("join:"+err.Error(), func() {
		n.cfg.Logger.WarnContext(ctx, "failed to join the cluster, retrying", slog.String("cluster", d.ClusterID), slog.Any("error", err))
	})
}

func (n *Node) formBootstrap(ctx context.Context) {
	rd := n.helloRound(ctx, n.resolveJoin(ctx))

	if d := decide(n.selfHello(ctx), rd.peers); d.Action != actionBootstrap {
		n.formation.progress(d.Found, d.Candidate, d.Detail)
		return
	}

	n.joinMu.Lock()
	defer n.joinMu.Unlock()

	r, _ := n.current()

	err := r.BootstrapCluster(raft.Configuration{Servers: []raft.Server{
		{Suffrage: raft.Voter, ID: raft.ServerID(n.id), Address: raft.ServerAddress(n.cfg.Advertise)},
	}}).Error()
	if err != nil {
		n.cfg.Logger.ErrorContext(ctx, "failed to form the cluster", slog.Any("error", err))
		return
	}

	n.cfg.Logger.InfoContext(ctx, "formed the cluster: the other nodes join it", slog.Int("nodes", n.cfg.BootstrapExpect))
	n.formation.set(FormationMember, "")
}

func (n *Node) countForeign(ctx context.Context) {
	secrets, err := n.db.ReadSecrets(ctx)
	if err != nil {
		return
	}

	rd := n.helloRound(ctx, n.resolveJoin(ctx))

	var foreign []string

	for _, p := range rd.peers {
		if p.State == helloMember && p.ClusterID != secrets.ClusterID {
			foreign = append(foreign, p.NodeID+" ("+p.ClusterID+")")
		}
	}

	n.formation.setForeign(len(foreign))

	if len(foreign) > 0 {
		n.formation.logOnce("foreign:"+strings.Join(foreign, ","), func() {
			n.cfg.Logger.ErrorContext(ctx, "nodes behind cluster.join belong to another cluster: two clusters formed, or the names "+
				"reach another deployment", slog.Any("nodes", foreign))
		})
	}
}

func (n *Node) probeRemoval(ctx context.Context) {
	secrets, err := n.db.ReadSecrets(ctx)
	if err != nil {
		return
	}

	targets := n.resolveJoin(ctx)

	if r, _ := n.current(); r != nil {
		if future := r.GetConfiguration(); future.Error() == nil {
			for _, s := range future.Configuration().Servers {
				if string(s.ID) != n.id {
					targets = append(targets, string(s.Address))
				}
			}
		}
	}

	if members, err := n.db.Members(ctx); err == nil {
		for _, m := range members {
			if m.NodeID != n.id && m.Address != "" {
				targets = append(targets, m.Address)
			}
		}
	}

	slices.Sort(targets)
	targets = slices.Compact(targets)

	for _, p := range n.helloRound(ctx, targets).peers {
		if p.Leader && p.ClusterID == secrets.ClusterID && p.Member != nil && !*p.Member {
			n.fence(ctx, p.NodeID)
			return
		}
	}
}

func (n *Node) joinSecret(ctx context.Context, clusterID string, addrs []string) error {
	n.joinMu.Lock()
	defer n.joinMu.Unlock()

	if n.formation.current() == FormationMember {
		return nil
	}

	r, _ := n.current()

	future := r.GetConfiguration()
	if err := future.Error(); err != nil {
		return err
	}

	for _, s := range future.Configuration().Servers {
		if s.ID != raft.ServerID(n.id) {
			return ErrAlreadyMember
		}
	}

	cert, err := n.certs.pending(clusterID, n.cfg.Now())
	if err != nil {
		return err
	}

	certPEM, _, err := pki.EncodeCert(cert)
	if err != nil {
		return err
	}

	req := joinRequest{
		NodeID: n.id, Address: n.cfg.Advertise, Certificate: string(certPEM), Schema: db.SchemaVersion(),
		Protocol: db.ProtocolVersion, Version: n.cfg.Version, Label: n.label, Generated: n.generated,
	}

	last := fmt.Errorf("%w: no member to join", errRetry)

	for _, addr := range addrs {
		reply, err := n.secretCall(ctx, addr, "/cluster/v1/join", req)
		if err != nil {
			last = err
			continue
		}

		if reply.Status != http.StatusOK {
			var e rpcError

			_ = json.Unmarshal(reply.Body, &e)
			last = &RemoteError{Status: reply.Status, Code: e.Code, Message: e.Error}

			if reply.Status >= http.StatusInternalServerError || reply.Status == http.StatusMisdirectedRequest {
				last = fmt.Errorf("%w: %w", errRetry, last)
			}

			continue
		}

		var joined joinReply
		if err := json.Unmarshal(reply.Body, &joined); err != nil {
			last = err
			continue
		}

		if joined.Pins[reply.Fingerprint] != reply.NodeID {
			last = fmt.Errorf("%w: node %s at %s is not among the members it returned", ErrInvalidJoin, reply.NodeID, addr)
			continue
		}

		if err := n.reset(context.WithoutCancel(ctx), cert, joined.Pins); err != nil {
			return fmt.Errorf("restart as a cluster member: %w", err)
		}

		return nil
	}

	return last
}
