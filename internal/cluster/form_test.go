package cluster

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/pki"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func newSecret(t testing.TB) string {
	t.Helper()

	b := make([]byte, 32)
	_, _ = rand.Read(b)

	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(b)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

type formingSpec struct {
	dir     string
	address string
	join    []string
	expect  int
	secret  string
	label   string
}

func openForming(t testing.TB, s formingSpec) *env {
	t.Helper()

	return openWith(t, s.dir, s.address, func(cfg *Config) {
		cfg.Join, cfg.BootstrapExpect, cfg.SecretFile, cfg.DiameterLabel = s.join, s.expect, s.secret, s.label
		cfg.removalAfter = time.Second
	})
}

type formingCluster struct {
	t     testing.TB
	specs []formingSpec
	nodes []*env
	mu    sync.Mutex
}

func newFormingCluster(t testing.TB, size, expect int, secret string) *formingCluster {
	t.Helper()

	c := &formingCluster{t: t}

	var addrs []string

	for range size {
		addrs = append(addrs, freeAddress(t))
	}

	for i := range size {
		c.specs = append(c.specs, formingSpec{
			dir: t.TempDir(), address: addrs[i], join: addrs, expect: expect, secret: secret,
			label: fmt.Sprintf("smsc-%d", i),
		})
	}

	c.nodes = make([]*env, size)

	t.Cleanup(func() {
		for i := range c.nodes {
			c.stop(i)
		}
	})

	return c
}

func (c *formingCluster) start(i int) *env {
	c.t.Helper()

	e := openForming(c.t, c.specs[i])

	c.mu.Lock()
	c.nodes[i] = e
	c.mu.Unlock()

	return e
}

func (c *formingCluster) startAll() {
	c.t.Helper()

	var wg sync.WaitGroup

	for i := range c.specs {
		wg.Go(func() { c.start(i) })
	}

	wg.Wait()
}

func (c *formingCluster) stop(i int) {
	c.mu.Lock()
	e := c.nodes[i]
	c.nodes[i] = nil
	c.mu.Unlock()

	if e != nil {
		_ = e.node.Close()
		_ = e.db.Close()
	}
}

func (c *formingCluster) live() []*env {
	var out []*env

	for _, e := range c.nodes {
		if e != nil {
			out = append(out, e)
		}
	}

	return out
}

func clusterIDOf(e *env) string {
	s, err := e.db.ReadSecrets(context.Background())
	if err != nil {
		return ""
	}

	return s.ClusterID
}

func (c *formingCluster) waitFormed(members int) string {
	c.t.Helper()

	var id string

	eventually(c.t, fmt.Sprintf("one cluster of %d members", members), func() error {
		ids := map[string]bool{}

		for _, e := range c.live() {
			if f := e.node.Formation(); f.State != FormationMember {
				return fmt.Errorf("node %s is %s: %s", e.node.ID(), f.State, f.Detail)
			}

			ids[clusterIDOf(e)] = true

			ms, err := e.db.Members(context.Background())
			if err != nil {
				return err
			}

			if len(ms) != members {
				return fmt.Errorf("node %s knows %d members", e.node.ID(), len(ms))
			}
		}

		if len(ids) != 1 || ids[""] {
			return fmt.Errorf("cluster IDs %v", ids)
		}

		for k := range ids {
			id = k
		}

		return nil
	})

	return id
}

func (c *formingCluster) voters() int {
	for _, e := range c.live() {
		if ok, _ := e.node.Leader(); !ok {
			continue
		}

		r, _ := e.node.current()

		future := r.GetConfiguration()
		if future.Error() != nil {
			return 0
		}

		count := 0

		for _, s := range future.Configuration().Servers {
			if s.Suffrage == raft.Voter {
				count++
			}
		}

		return count
	}

	return 0
}

func TestDecide(t *testing.T) {
	self := hello{NodeID: "b", State: helloForming, Expect: 3, Schema: 1, Protocol: 2, Label: "smsc-b"}
	peer := func(id string, edit ...func(*hello)) hello {
		h := hello{NodeID: id, State: helloForming, Expect: 3, Schema: 1, Protocol: 2, Label: "smsc-" + id}
		for _, e := range edit {
			e(&h)
		}

		return h
	}
	naming := func(c string) func(*hello) { return func(h *hello) { h.Candidate = c } }
	member := func(cluster string, leader bool) func(*hello) {
		return func(h *hello) {
			h.State, h.ClusterID, h.Leader, h.Address = helloMember, cluster, leader, h.NodeID+":7000"
		}
	}

	tests := []struct {
		name      string
		self      hello
		peers     []hello
		action    action
		blocked   bool
		candidate string
		members   []string
		detail    string
	}{
		{name: "alone", self: self, action: actionWait, detail: "found 1 of 3"},
		{name: "too few", self: self, peers: []hello{peer("c")}, action: actionWait, detail: "found 2 of 3"},
		{name: "another candidate", self: self, peers: []hello{peer("a"), peer("c")}, candidate: "a", detail: "node a"},
		{
			name: "not yet named", self: hello{NodeID: "a", State: helloForming, Expect: 3, Schema: 1, Protocol: 2, Label: "x"},
			peers: []hello{peer("b"), peer("c", naming("a"))}, candidate: "a", detail: "agree",
		},
		{
			name: "named by all", self: hello{NodeID: "a", State: helloForming, Expect: 3, Schema: 1, Protocol: 2, Label: "x"},
			peers: []hello{peer("b", naming("a")), peer("c", naming("a"))}, action: actionBootstrap, candidate: "a",
		},
		{
			name: "count mismatch", self: self, peers: []hello{peer("a", func(h *hello) { h.Expect = 5 }), peer("c")},
			blocked: true, detail: "expects 5",
		},
		{
			name: "duplicate label", self: self, peers: []hello{peer("a", func(h *hello) { h.Label = "smsc-b" }), peer("c")},
			blocked: true, detail: "same Diameter host label",
		},
		{
			name: "version mismatch", self: self, peers: []hello{peer("a", func(h *hello) { h.Schema = 9 }), peer("c")},
			blocked: true, detail: "schema 9",
		},
		{
			name: "a member", self: self, peers: []hello{peer("a"), peer("c", member("x", false)), peer("d", member("x", true))},
			action: actionJoin, members: []string{"d:7000", "c:7000"},
		},
		{
			name: "two clusters", self: self, peers: []hello{peer("c", member("x", false)), peer("d", member("y", true))},
			blocked: true, detail: "2 different clusters",
		},
		{
			name: "no expect", self: hello{NodeID: "b", State: helloForming, Label: "smsc-b"}, peers: []hello{peer("a")},
			detail: "cluster member",
		},
		{
			name: "removed peers are ignored", self: self,
			peers: []hello{peer("a", func(h *hello) { h.State = helloRemoved }), peer("c")}, detail: "found 2 of 3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := decide(tt.self, tt.peers)

			if d.Action != tt.action || d.Blocked != tt.blocked || d.Candidate != tt.candidate ||
				!strings.Contains(d.Detail, tt.detail) || fmt.Sprint(d.Members) != fmt.Sprint(tt.members) {
				t.Fatalf("decide = %+v", d)
			}
		})
	}
}

func TestProofs(t *testing.T) {
	keys, err := pki.ParseSecrets([]byte(base64.StdEncoding.EncodeToString(make([]byte, 32)) + "\n"))
	if err != nil {
		t.Fatal(err)
	}

	in := pki.ProofInput{
		Role: pki.RoleClient, Exporter: []byte("exporter-one"), NodeID: "n", Fingerprint: "f", Address: "a:1",
		BodyHash: pki.BodyHash([]byte("{}")),
	}
	proof := pki.Prove(keys[0], in)

	if !pki.VerifyProof(keys, in, proof) {
		t.Fatal("a valid proof was refused")
	}

	for name, edit := range map[string]func(*pki.ProofInput){
		"another exporter": func(p *pki.ProofInput) { p.Exporter = []byte("exporter-two") },
		"another role":     func(p *pki.ProofInput) { p.Role = pki.RoleServer },
		"another node":     func(p *pki.ProofInput) { p.NodeID = "m" },
		"another cert":     func(p *pki.ProofInput) { p.Fingerprint = "g" },
		"another address":  func(p *pki.ProofInput) { p.Address = "b:1" },
		"another body":     func(p *pki.ProofInput) { p.BodyHash = pki.BodyHash([]byte("[]")) },
	} {
		changed := in
		edit(&changed)

		if pki.VerifyProof(keys, changed, proof) {
			t.Errorf("%s: the proof verified", name)
		}
	}

	other := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))

	rotated, err := pki.ParseSecrets([]byte(other + "\n" + base64.StdEncoding.EncodeToString(make([]byte, 32)) + "\n"))
	if err != nil {
		t.Fatal(err)
	}

	if !pki.VerifyProof(rotated, in, proof) {
		t.Fatal("the second line of a rotated secret does not verify")
	}

	if pki.VerifyProof(rotated[:1], in, proof) {
		t.Fatal("another secret verified the proof")
	}
}

func TestThreeNodesStartedTogetherFormOneCluster(t *testing.T) {
	c := newFormingCluster(t, 3, 3, newSecret(t))
	c.startAll()
	c.waitFormed(3)

	eventually(t, "three voters", func() error {
		if v := c.voters(); v != 3 {
			return fmt.Errorf("%d voters", v)
		}

		return nil
	})

	for _, e := range c.live() {
		waitReadyExcept(t, e, CheckInitialized)
	}
}

func waitReadyExcept(t testing.TB, e *env, skip string) {
	t.Helper()

	eventually(t, "readiness of "+e.node.ID(), func() error {
		for _, c := range e.node.Readiness(context.Background(), false) {
			if !c.OK && c.Name != skip {
				return fmt.Errorf("%s: %s", c.Name, c.Detail)
			}
		}

		return nil
	})
}

func TestEveryStartOrderFormsOneCluster(t *testing.T) {
	for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			t.Parallel()

			c := newFormingCluster(t, 3, 3, newSecret(t))

			for _, i := range order {
				c.start(i)
				time.Sleep(discoverInterval)
			}

			c.waitFormed(3)
		})
	}
}

func TestFormingNodeIsUnreadyAndWaits(t *testing.T) {
	c := newFormingCluster(t, 3, 3, newSecret(t))
	e := c.start(0)

	eventually(t, "the forming detail", func() error {
		f := e.node.Formation()
		if f.State != FormationForming || f.Expected != 3 || f.Found != 1 || !strings.Contains(f.Detail, "found 1 of 3") {
			return fmt.Errorf("formation %+v", f)
		}

		return nil
	})

	waitFailing(t, e, CheckMember, "forming")

	c.start(1)

	eventually(t, "two of three found", func() error {
		if f := e.node.Formation(); f.Found != 2 {
			return fmt.Errorf("formation %+v", f)
		}

		return nil
	})

	select {
	case <-e.node.Formed():
		t.Fatal("formed with two of three nodes")
	default:
	}
}

func TestWrongSecretIsRefusedBothWays(t *testing.T) {
	c := newFormingCluster(t, 2, 2, newSecret(t))
	c.specs[1].secret = newSecret(t)
	c.startAll()

	for _, e := range c.live() {
		eventually(t, "the refused hellos on "+e.node.ID(), func() error {
			if v := testutil.ToFloat64(e.node.metrics.joinRequests.WithLabelValues("unauthorized")); v == 0 {
				return errors.New("no unauthorized request")
			}

			return nil
		})
	}

	time.Sleep(3 * discoverInterval)

	for _, e := range c.live() {
		if f := e.node.Formation(); f.State != FormationForming || f.Found != 1 {
			t.Fatalf("node %s with another secret: formation %+v", e.node.ID(), f)
		}
	}

	member := newFormingCluster(t, 1, 0, newSecret(t))
	member.specs[0].join = nil
	leader := member.start(0)
	waitLeader(t, leader.node)
	waitMember(t, leader, leader.node.ID())

	joiner := openForming(t, formingSpec{
		dir: t.TempDir(), address: freeAddress(t), join: []string{member.specs[0].address}, secret: newSecret(t),
		label: "smsc-joiner",
	})

	defer func() { _ = joiner.node.Close(); _ = joiner.db.Close() }()

	eventually(t, "the refused join", func() error {
		if v := testutil.ToFloat64(leader.node.metrics.joinRequests.WithLabelValues("unauthorized")); v == 0 {
			return errors.New("no unauthorized request on the member")
		}

		return nil
	})

	if f := joiner.node.Formation(); f.State == FormationMember {
		t.Fatal("a node with another secret joined")
	}

	if ms, _ := leader.db.Members(context.Background()); len(ms) != 1 {
		t.Fatalf("members = %+v", ms)
	}
}

func TestSecretRotationNeedsNoRestart(t *testing.T) {
	old := newSecret(t)
	c := newFormingCluster(t, 2, 2, old)
	c.specs[1].secret = newSecret(t)
	c.startAll()

	time.Sleep(2 * discoverInterval)

	oldLine, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}

	newLine, err := os.ReadFile(c.specs[1].secret)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(old, append(newLine, oldLine...), 0o600); err != nil {
		t.Fatal(err)
	}

	c.waitFormed(2)
}

func TestNoFormationNextToAMemberWithoutQuorum(t *testing.T) {
	secret := newSecret(t)
	c := newFormingCluster(t, 3, 3, secret)
	c.startAll()
	c.waitFormed(3)

	eventually(t, "three voters", func() error {
		if v := c.voters(); v != 3 {
			return fmt.Errorf("%d voters", v)
		}

		return nil
	})

	c.stop(1)
	c.stop(2)

	fresh := openForming(t, formingSpec{
		dir: t.TempDir(), address: freeAddress(t), join: []string{c.specs[0].address}, expect: 3, secret: secret,
		label: "smsc-fresh",
	})

	defer func() { _ = fresh.node.Close(); _ = fresh.db.Close() }()

	time.Sleep(5 * discoverInterval)

	if f := fresh.node.Formation(); f.State == FormationMember {
		t.Fatalf("a node next to a member without quorum formed or joined: %+v", f)
	}

	if clusterIDOf(fresh) != "" {
		t.Fatal("the fresh node created a cluster")
	}
}

func TestFormedClusterRestarts(t *testing.T) {
	c := newFormingCluster(t, 3, 3, newSecret(t))
	c.startAll()
	id := c.waitFormed(3)

	for i := range c.nodes {
		c.stop(i)
	}

	c.startAll()

	for _, e := range c.live() {
		select {
		case <-e.node.Formed():
		default:
			t.Fatalf("node %s with data is %s", e.node.ID(), e.node.Formation().State)
		}
	}

	if got := c.waitFormed(3); got != id {
		t.Fatalf("cluster %s after the restart, want %s", got, id)
	}

	eventually(t, "a leader", func() error {
		for _, e := range c.live() {
			if ok, _ := e.node.Leader(); ok {
				return nil
			}
		}

		return errors.New("no leader")
	})
}

func TestBootstrapperCrashAfterForming(t *testing.T) {
	c := newFormingCluster(t, 3, 3, newSecret(t))
	c.startAll()

	var first int

	eventually(t, "a bootstrapper", func() error {
		for i, e := range c.live() {
			if e.node.Formation().State == FormationMember {
				first = i
				return nil
			}
		}

		return errors.New("none")
	})

	c.stop(first)
	time.Sleep(discoverInterval)
	c.start(first)

	c.waitFormed(3)
}

func TestWipedNodeRejoinsOnceItsOldMemberIsRemoved(t *testing.T) {
	c := newFormingCluster(t, 3, 3, newSecret(t))
	c.startAll()
	c.waitFormed(3)

	eventually(t, "three voters", func() error {
		if v := c.voters(); v != 3 {
			return fmt.Errorf("%d voters", v)
		}

		return nil
	})

	var victim int

	for i, e := range c.live() {
		if ok, _ := e.node.Leader(); !ok {
			victim = i
			break
		}
	}

	oldID := c.nodes[victim].node.ID()
	c.stop(victim)

	c.specs[victim].dir = t.TempDir()
	wiped := c.start(victim)

	if wiped.node.ID() == oldID {
		t.Fatal("the wiped node kept its ID")
	}

	eventually(t, "the label conflict", func() error {
		if f := wiped.node.Formation(); f.State == FormationMember || !strings.Contains(f.Detail, "label") {
			return fmt.Errorf("formation %+v", f)
		}

		return nil
	})

	var leader *env

	for _, e := range c.live() {
		if ok, _ := e.node.Leader(); ok {
			leader = e
		}
	}

	if leader == nil {
		t.Fatal("no leader")
	}

	if err := leader.node.RemoveMember(timeout(t), oldID, true); err != nil {
		t.Fatal(err)
	}

	c.waitFormed(3)

	waitLabel(t, leader, wiped.node.ID(), c.specs[victim].label, false)
}

func TestRemovalWhileDownIsDetected(t *testing.T) {
	c := newFormingCluster(t, 3, 3, newSecret(t))
	c.startAll()
	c.waitFormed(3)

	eventually(t, "three voters", func() error {
		if v := c.voters(); v != 3 {
			return fmt.Errorf("%d voters", v)
		}

		return nil
	})

	var (
		victim int
		leader *env
	)

	for i, e := range c.live() {
		if ok, _ := e.node.Leader(); ok {
			leader = e
		} else {
			victim = i
		}
	}

	id := c.nodes[victim].node.ID()
	c.stop(victim)

	if err := leader.node.RemoveMember(timeout(t), id, true); err != nil {
		t.Fatal(err)
	}

	restarted := c.start(victim)

	waitRemoved(t, restarted)

	if f := restarted.node.Formation(); f.State != FormationRemoved {
		t.Fatalf("formation %+v", f)
	}
}

func TestHelloTellsAMemberItWasRemoved(t *testing.T) {
	c := newFormingCluster(t, 2, 2, newSecret(t))
	c.startAll()
	c.waitFormed(2)

	var leader, follower *env

	for _, e := range c.live() {
		if ok, _ := e.node.Leader(); ok {
			leader = e
		} else {
			follower = e
		}
	}

	if leader == nil || follower == nil {
		t.Fatal("no leader and follower")
	}

	known := func() *bool {
		for _, p := range follower.node.helloRound(context.Background(), []string{leader.node.cfg.Advertise}).peers {
			return p.Member
		}

		return nil
	}

	eventually(t, "the leader to know the follower", func() error {
		if m := known(); m == nil || !*m {
			return fmt.Errorf("member = %v", m)
		}

		return nil
	})

	if _, err := leader.db.Member(context.Background(), follower.node.ID()); err != nil {
		t.Fatal(err)
	}

	if err := leader.db.RemoveMember(timeout(t), follower.node.ID(), db.RemovedForced); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the leader to report the follower removed", func() error {
		if m := known(); m == nil || *m {
			return fmt.Errorf("member = %v", m)
		}

		return nil
	})
}
