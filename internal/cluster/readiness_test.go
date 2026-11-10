package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

var errLagging = errors.New("lagging")

type lag struct {
	on atomic.Bool
}

func (l *lag) wrap(t raft.Transport) raft.Transport {
	lt := &lagTransport{Transport: t, l: l, consumer: make(chan raft.RPC), done: make(chan struct{})}

	go lt.filter()

	return lt
}

type lagTransport struct {
	raft.Transport
	l        *lag
	consumer chan raft.RPC
	done     chan struct{}
	once     sync.Once
}

func (t *lagTransport) dropped(rpc raft.RPC) bool {
	if !t.l.on.Load() {
		return false
	}

	switch cmd := rpc.Command.(type) {
	case *raft.AppendEntriesRequest:
		if len(cmd.Entries) == 0 {
			return false
		}
	case *raft.InstallSnapshotRequest:
	default:
		return false
	}

	if rpc.Reader != nil {
		_, _ = io.Copy(io.Discard, rpc.Reader)
	}

	rpc.Respond(nil, errLagging)

	return true
}

func (t *lagTransport) filter() {
	for {
		select {
		case <-t.done:
			return
		case rpc := <-t.Transport.Consumer():
			if t.dropped(rpc) {
				continue
			}

			select {
			case t.consumer <- rpc:
			case <-t.done:
				return
			}
		}
	}
}

func (t *lagTransport) Consumer() <-chan raft.RPC { return t.consumer }

func (t *lagTransport) SetHeartbeatHandler(cb func(rpc raft.RPC)) {
	if cb == nil {
		t.Transport.SetHeartbeatHandler(nil)
		return
	}

	t.Transport.SetHeartbeatHandler(func(rpc raft.RPC) {
		if !t.dropped(rpc) {
			cb(rpc)
		}
	})
}

func (t *lagTransport) RequestPreVote(id raft.ServerID, target raft.ServerAddress, args *raft.RequestPreVoteRequest,
	resp *raft.RequestPreVoteResponse,
) error {
	return t.Transport.(raft.WithPreVote).RequestPreVote(id, target, args, resp)
}

func (t *lagTransport) Close() error {
	t.once.Do(func() { close(t.done) })

	if c, ok := t.Transport.(raft.WithClose); ok {
		return c.Close()
	}

	return nil
}

func openWith(t testing.TB, dir, address string, edit func(*Config)) *env {
	t.Helper()

	ctx := context.Background()

	d, err := db.Open(ctx, filepath.Join(dir, "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	cfg := Config{Dir: filepath.Join(dir, "raft"), Address: address, Version: "test", removalAfter: time.Second}
	edit(&cfg)

	n, err := Open(ctx, cfg, d)
	if err != nil {
		_ = d.Close()

		t.Fatal(err)
	}

	d.SetProposer(n)

	return &env{dir: dir, db: d, node: n}
}

func checkOf(checks []Check, name string) Check {
	if i := slices.IndexFunc(checks, func(c Check) bool { return c.Name == name }); i >= 0 {
		return checks[i]
	}

	return Check{Name: name, Detail: "missing"}
}

func failing(checks []Check) []string {
	var out []string

	for _, c := range checks {
		if !c.OK {
			out = append(out, c.Name+": "+c.Detail)
		}
	}

	return out
}

func waitReady(t testing.TB, e *env, withCluster bool) {
	t.Helper()

	eventually(t, "readiness of "+e.node.ID(), func() error {
		if f := failing(e.node.Readiness(context.Background(), withCluster)); len(f) > 0 {
			return errors.New(strings.Join(f, "; "))
		}

		return nil
	})
}

func waitFailing(t testing.TB, e *env, name, detail string) {
	t.Helper()

	eventually(t, fmt.Sprintf("check %s failing on %s", name, e.node.ID()), func() error {
		c := checkOf(e.node.Readiness(context.Background(), name == CheckCluster), name)
		if c.OK || !strings.Contains(c.Detail, detail) {
			return fmt.Errorf("%+v", c)
		}

		return nil
	})
}

func (c *testCluster) initialize() {
	c.t.Helper()

	id, err := c.leader().db.CreateUser(timeout(c.t), db.NewUser{Email: "admin@example.com", Role: db.RoleAdmin, PasswordHash: "h"}, 10)
	if err != nil {
		c.t.Fatal(err)
	}

	for _, e := range c.nodes {
		eventually(c.t, "the first user on "+e.node.ID(), func() error {
			_, err := e.db.GetUserByID(context.Background(), id)
			return err
		})
	}
}

func TestClusteredNodeIsUnreadyUntilInitialized(t *testing.T) {
	e := openClustered(t, t.TempDir(), freeAddress(t))
	defer e.close(t)

	waitLeader(t, e.node)
	waitFailing(t, e, CheckInitialized, "not initialized")

	eventually(t, "only the initialized check failing", func() error {
		if f := failing(e.node.Readiness(context.Background(), false)); len(f) != 1 {
			return errors.New(strings.Join(f, "; "))
		}

		return nil
	})

	if _, err := e.db.CreateUser(timeout(t), db.NewUser{Email: "admin@example.com", Role: db.RoleAdmin, PasswordHash: "h"}, 10); err != nil {
		t.Fatal(err)
	}

	waitReady(t, e, true)
}

func TestJoinedNodeIsReadyOnceTheUsersReplicate(t *testing.T) {
	c := newCluster(t, 1)
	c.initialize()

	leader := c.leader()
	waitReady(t, leader, false)

	joiner := openClustered(t, t.TempDir(), freeAddress(t))
	t.Cleanup(func() { joiner.close(t) })

	waitLeader(t, joiner.node)
	waitFailing(t, joiner, CheckInitialized, "not initialized")

	token, err := leader.node.MintToken(timeout(t), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if err := joiner.node.Join(timeout(t), token.Token); err != nil {
		t.Fatal(err)
	}

	waitReady(t, joiner, false)
}

func TestReadinessOfASingleNode(t *testing.T) {
	e := open(t, t.TempDir())
	defer e.close(t)

	waitLeader(t, e.node)

	if users, err := e.db.CountUsers(context.Background()); err != nil || users != 0 {
		t.Fatalf("users = %d, %v", users, err)
	}

	waitReady(t, e, true)

	var names []string
	for _, c := range e.node.Readiness(context.Background(), true) {
		names = append(names, c.Name)
	}

	want := []string{CheckMember, CheckInitialized, CheckLabel, CheckDrain, CheckDatabase, CheckLeader, CheckCluster}
	if !slices.Equal(names, want) {
		t.Fatalf("checks = %v, want %v", names, want)
	}

	if checks := e.node.Readiness(context.Background(), false); slices.ContainsFunc(checks, func(c Check) bool {
		return c.Name == CheckCluster
	}) {
		t.Fatalf("checks without cluster = %+v", checks)
	}

	waitMember(t, e, e.node.ID())

	if err := e.db.SetDrainState(timeout(t), e.node.ID(), db.DrainDraining); err != nil {
		t.Fatal(err)
	}

	waitFailing(t, e, CheckDrain, "draining")

	if err := e.db.SetDrainState(timeout(t), e.node.ID(), db.DrainActive); err != nil {
		t.Fatal(err)
	}

	waitReady(t, e, false)

	e.node.markRemoved()

	waitFailing(t, e, CheckMember, "removed")
}

func TestReadinessOfAFollower(t *testing.T) {
	c := newCluster(t, 2)
	c.initialize()
	leader, follower := c.leader(), c.follower()

	waitReady(t, leader, true)
	waitReady(t, follower, false)
}

func TestReadinessOfALeaderlessMinority(t *testing.T) {
	parts := []*partition{{}, {}, {}}
	c := newClusterWith(t, 3, func(i int, dir, address string) *env {
		return openWith(t, dir, address, func(cfg *Config) { cfg.transport = parts[i].wrap })
	})

	c.initialize()

	waitVoters(t, c, 3)

	follower := c.follower()
	i := slices.Index(c.nodes, follower)

	waitReady(t, follower, false)

	parts[i].isolate()

	waitFailing(t, follower, CheckLeader, "no contact with the cluster leader")

	for _, e := range c.nodes {
		if e != follower {
			waitReady(t, e, false)
		}
	}

	parts[i].heal()

	waitReady(t, follower, false)
}

func TestReadinessWhileTheTermIsStarting(t *testing.T) {
	lags := []*lag{{}, {}, {}}
	c := newClusterWith(t, 3, func(i int, dir, address string) *env {
		return openWith(t, dir, address, func(cfg *Config) { cfg.transport = lags[i].wrap })
	})

	c.initialize()

	waitVoters(t, c, 3)

	leader, target := c.leader(), c.follower()
	waitReady(t, target, false)

	for i, e := range c.nodes {
		if e != target {
			lags[i].on.Store(true)
		}
	}

	r, _ := leader.node.current()
	if err := r.LeadershipTransferToServer(raft.ServerID(target.node.ID()), raft.ServerAddress(target.node.cfg.Advertise)).Error(); err != nil {
		t.Fatal(err)
	}

	waitFailing(t, target, CheckLeader, "not established")

	for i := range lags {
		lags[i].on.Store(false)
	}

	waitReady(t, target, false)
}

func TestClusterCheckWithAVoterDown(t *testing.T) {
	c := newCluster(t, 3)
	c.initialize()
	waitVoters(t, c, 3)

	leader, follower := c.leader(), c.follower()

	waitReady(t, leader, true)

	for _, e := range c.nodes {
		if e != leader {
			waitReady(t, e, false)
			waitFailing(t, e, CheckCluster, "only the leader")
		}
	}

	_ = follower.node.Close()
	_ = follower.db.Close()

	for i, e := range c.nodes {
		if e == follower {
			c.nodes[i] = nil
		}
	}

	waitFailing(t, leader, CheckCluster, follower.node.ID())

	for _, e := range c.nodes {
		if e != nil && e != leader {
			waitReady(t, e, false)
		}
	}

	waitReady(t, leader, false)
}
