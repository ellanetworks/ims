package cluster

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/pki"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

func freeAddress(t testing.TB) string {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = ln.Close() }()

	return ln.Addr().String()
}

func openClustered(t testing.TB, dir, address string) *env {
	t.Helper()

	ctx := context.Background()

	d, err := db.Open(ctx, filepath.Join(dir, "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	n, err := Open(ctx, Config{Dir: filepath.Join(dir, "raft"), Address: address, Version: "test"}, d)
	if err != nil {
		_ = d.Close()

		t.Fatal(err)
	}

	d.SetProposer(n)

	return &env{dir: dir, db: d, node: n}
}

type testCluster struct {
	t     testing.TB
	nodes []*env
	addrs []string
}

func newCluster(t testing.TB, size int) *testCluster {
	t.Helper()

	return newClusterWith(t, size, func(_ int, dir, address string) *env { return openClustered(t, dir, address) })
}

func newClusterWith(t testing.TB, size int, open func(i int, dir, address string) *env) *testCluster {
	t.Helper()

	c := &testCluster{t: t}

	t.Cleanup(func() {
		for _, e := range c.nodes {
			if e != nil {
				_ = e.node.Close()

				if e.stop != nil {
					e.stop()
				}

				_ = e.db.Close()
			}
		}
	})

	for i := range size {
		c.addrs = append(c.addrs, freeAddress(t))
		c.nodes = append(c.nodes, open(i, t.TempDir(), c.addrs[i]))
	}

	waitLeader(t, c.nodes[0].node)
	waitMember(t, c.nodes[0], c.nodes[0].node.ID())

	for _, e := range c.nodes[1:] {
		waitLeader(t, e.node)

		token, err := c.nodes[0].node.MintToken(timeout(t), time.Hour)
		if err != nil {
			t.Fatal(err)
		}

		if err := e.node.Join(timeout(t), token.Token); err != nil {
			t.Fatal(err)
		}
	}

	for _, e := range c.nodes {
		for _, other := range c.nodes {
			waitMember(t, e, other.node.ID())
		}
	}

	return c
}

func waitMember(t testing.TB, e *env, id string) {
	t.Helper()

	eventually(t, fmt.Sprintf("member %s on %s", id, e.node.ID()), func() error {
		_, err := e.db.Member(context.Background(), id)
		return err
	})
}

func eventually(t testing.TB, what string, f func() error) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)

	var err error

	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("%s: %v", what, err)
}

func (c *testCluster) leader() *env {
	c.t.Helper()

	var leader *env

	eventually(c.t, "a leader", func() error {
		for _, e := range c.nodes {
			if e == nil {
				continue
			}

			if ok, _ := e.node.Leader(); ok {
				leader = e
				return nil
			}
		}

		return errors.New("no leader")
	})

	return leader
}

func (c *testCluster) follower() *env {
	leader := c.leader()

	for _, e := range c.nodes {
		if e != nil && e != leader {
			return e
		}
	}

	c.t.Fatal("no follower")

	return nil
}

func TestJoinReplicatesAndForwards(t *testing.T) {
	c := newCluster(t, 3)
	ctx := timeout(t)

	follower := c.follower()

	id, err := follower.db.CreateUser(ctx, db.NewUser{Email: "a@example.com", Role: db.RoleAdmin, PasswordHash: "h"}, 10)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range c.nodes {
		eventually(t, "the user on "+e.node.ID(), func() error {
			_, err := e.db.GetUserByID(ctx, id)
			return err
		})
	}

	hashes := map[string]bool{}

	eventually(t, "equal hashes", func() error {
		clear(hashes)

		index := c.leader().db.AppliedIndex()

		for _, e := range c.nodes {
			h, at, err := e.db.Hash(ctx)
			if err != nil {
				return err
			}

			if at != index {
				return fmt.Errorf("%s at %d, leader at %d", e.node.ID(), at, index)
			}

			hashes[string(h)] = true
		}

		if len(hashes) != 1 {
			return fmt.Errorf("%d different hashes", len(hashes))
		}

		return nil
	})

	members, err := follower.db.Members(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(members) != 3 {
		t.Fatalf("members = %+v", members)
	}
}

func TestTokenIsSingleUse(t *testing.T) {
	c := newCluster(t, 2)

	token, err := c.leader().node.MintToken(timeout(t), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	joiner := openClustered(t, t.TempDir(), freeAddress(t))
	t.Cleanup(func() { joiner.close(t) })
	waitLeader(t, joiner.node)

	if err := joiner.node.Join(timeout(t), token.Token); err != nil {
		t.Fatal(err)
	}

	again := openClustered(t, t.TempDir(), freeAddress(t))
	t.Cleanup(func() { again.close(t) })
	waitLeader(t, again.node)

	if err := again.node.Join(timeout(t), token.Token); !errors.Is(err, db.ErrTokenUsed) {
		t.Fatalf("second join = %v, want %v", err, db.ErrTokenUsed)
	}
}

func TestPromotesToThreeVoters(t *testing.T) {
	waitVoters(t, newCluster(t, 3), 3)
}

func TestLeaderFailover(t *testing.T) {
	c := newCluster(t, 3)

	waitVoters(t, c, 3)

	leader := c.leader()

	for i, e := range c.nodes {
		if e == leader {
			_ = e.node.Close()
			_ = e.db.Close()
			c.nodes[i] = nil
		}
	}

	survivor := c.leader()

	if _, err := survivor.db.CreateUser(timeout(t), db.NewUser{Email: "b@example.com", Role: db.RoleAdmin}, 10); err != nil {
		t.Fatal(err)
	}

	for _, e := range c.nodes {
		if e != nil && e != survivor {
			if _, err := e.db.CreateUser(timeout(t), db.NewUser{
				Email: "c" + e.node.ID() + "@example.com",
				Role:  db.RoleAdmin,
			}, 10); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func waitVoters(t testing.TB, c *testCluster, want int) {
	t.Helper()

	eventually(t, fmt.Sprintf("%d voters", want), func() error {
		r, _ := c.leader().node.current()

		future := r.GetConfiguration()
		if err := future.Error(); err != nil {
			return err
		}

		voters := 0

		for _, s := range future.Configuration().Servers {
			if s.Suffrage == raft.Voter {
				voters++
			}
		}

		if voters != want {
			return fmt.Errorf("%d voters", voters)
		}

		return nil
	})
}

func TestRemoveMember(t *testing.T) {
	c := newCluster(t, 3)
	follower := c.follower()

	if err := c.leader().node.RemoveMember(timeout(t), follower.node.ID(), true); err != nil {
		t.Fatal(err)
	}

	select {
	case <-follower.node.Removed():
	case <-time.After(10 * time.Second):
		t.Fatal("the removed node did not notice")
	}

	if _, err := follower.db.CreateUser(timeout(t), db.NewUser{Email: "x@example.com", Role: db.RoleAdmin}, 10); !errors.Is(err, ErrRemoved) {
		t.Fatalf("write on a removed node = %v, want %v", err, ErrRemoved)
	}

	for _, e := range c.nodes {
		if e == follower {
			continue
		}

		eventually(t, "removal on "+e.node.ID(), func() error {
			if _, err := e.db.Member(context.Background(), follower.node.ID()); !errors.Is(err, db.ErrNotFound) {
				return fmt.Errorf("still a member: %v", err)
			}

			return nil
		})
	}
}

func TestUnreachableMember(t *testing.T) {
	c := newCluster(t, 2)
	leader, follower := c.leader(), c.follower()

	eventually(t, "the follower's report", func() error {
		if leader.node.Unreachable(follower.node.ID()) {
			return errors.New("unreachable")
		}

		return nil
	})

	_ = follower.node.Close()
	_ = follower.db.Close()

	for i, e := range c.nodes {
		if e == follower {
			c.nodes[i] = nil
		}
	}

	deadline := time.Now().Add(lastContactLimit + 10*time.Second)

	for !leader.node.Unreachable(follower.node.ID()) {
		if time.Now().After(deadline) {
			t.Fatal("a stopped member was never unreachable")
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func corruptingChecks(t *testing.T, size int) (*testCluster, *atomic.Bool) {
	var corrupt atomic.Bool

	c := newClusterWith(t, size, func(_ int, dir, address string) *env {
		return openWith(t, dir, address, func(cfg *Config) {
			cfg.hashCheck = func(check func() *db.HashCheck) func() *db.HashCheck {
				return func() *db.HashCheck {
					h := check()
					if h == nil || !corrupt.Load() {
						return h
					}

					return &db.HashCheck{Index: h.Index, Hash: []byte("not the leader's hash")}
				}
			}
		})
	})

	return c, &corrupt
}

func TestFollowerNoticesItDiverged(t *testing.T) {
	c, corrupt := corruptingChecks(t, 2)
	leader, follower := c.leader(), c.follower()

	write(t, leader, "before")

	if follower.db.Diverged() {
		t.Fatal("the follower diverged before any corruption")
	}

	corrupt.Store(true)
	write(t, leader, "first")
	write(t, leader, "second")

	eventually(t, "the follower to notice it diverged", func() error {
		if !follower.db.Diverged() {
			return errors.New("not diverged yet")
		}

		return nil
	})
}

func TestJoinRetriesAfterAHalfCompletedAdmission(t *testing.T) {
	c := newCluster(t, 1)
	leader := c.leader()
	ctx := timeout(t)

	joiner := openClustered(t, t.TempDir(), freeAddress(t))
	t.Cleanup(func() { joiner.close(t) })
	waitLeader(t, joiner.node)

	first, err := leader.node.MintToken(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	claims, err := pki.ReadToken(first.Token)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := joiner.node.certs.pending(claims.ClusterID, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	certPEM, _, err := pki.EncodeCert(cert)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := leader.node.admit(ctx, joinRequest{
		Token: first.Token, NodeID: joiner.node.ID(), Address: joiner.node.cfg.Advertise, Certificate: string(certPEM),
		Schema: db.SchemaVersion(), Protocol: db.ProtocolVersion, Version: "test",
	}); err != nil {
		t.Fatal(err)
	}

	second, err := leader.node.MintToken(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if err := joiner.node.Join(ctx, second.Token); err != nil {
		t.Fatalf("retrying a half-completed join: %v", err)
	}

	waitMember(t, joiner, joiner.node.ID())
	waitMember(t, joiner, leader.node.ID())
}

func write(t testing.TB, e *env, name string) {
	t.Helper()

	if _, err := e.db.CreateUser(timeout(t), db.NewUser{Email: name + "@example.com", Role: db.RoleAdmin}, 100); err != nil {
		t.Fatal(err)
	}
}
