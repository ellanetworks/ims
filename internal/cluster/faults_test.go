package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

var (
	errPartitioned = errors.New("partitioned")
	errReleased    = errors.New("the hung log store was released")
)

type partition struct {
	isolated atomic.Bool
}

func (p *partition) isolate() { p.isolated.Store(true) }

func (p *partition) heal() { p.isolated.Store(false) }

func (p *partition) wrap(t raft.Transport) raft.Transport {
	pt := &partitionedTransport{Transport: t, p: p, consumer: make(chan raft.RPC), done: make(chan struct{})}

	go pt.filter()

	return pt
}

type partitionedTransport struct {
	raft.Transport
	p        *partition
	consumer chan raft.RPC
	done     chan struct{}
	once     sync.Once
}

func (t *partitionedTransport) filter() {
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

func (t *partitionedTransport) dropped(rpc raft.RPC) bool {
	if !t.p.isolated.Load() {
		return false
	}

	if rpc.Reader != nil {
		_, _ = io.Copy(io.Discard, rpc.Reader)
	}

	rpc.Respond(nil, errPartitioned)

	return true
}

func (t *partitionedTransport) Consumer() <-chan raft.RPC { return t.consumer }

func (t *partitionedTransport) SetHeartbeatHandler(cb func(rpc raft.RPC)) {
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

func (t *partitionedTransport) AppendEntriesPipeline(id raft.ServerID, target raft.ServerAddress) (raft.AppendPipeline, error) {
	if t.p.isolated.Load() {
		return nil, errPartitioned
	}

	pipeline, err := t.Transport.AppendEntriesPipeline(id, target)
	if err != nil {
		return nil, err
	}

	return partitionedPipeline{AppendPipeline: pipeline, p: t.p}, nil
}

func (t *partitionedTransport) AppendEntries(id raft.ServerID, target raft.ServerAddress, args *raft.AppendEntriesRequest,
	resp *raft.AppendEntriesResponse,
) error {
	if t.p.isolated.Load() {
		return errPartitioned
	}

	return t.Transport.AppendEntries(id, target, args, resp)
}

func (t *partitionedTransport) RequestVote(id raft.ServerID, target raft.ServerAddress, args *raft.RequestVoteRequest,
	resp *raft.RequestVoteResponse,
) error {
	if t.p.isolated.Load() {
		return errPartitioned
	}

	return t.Transport.RequestVote(id, target, args, resp)
}

func (t *partitionedTransport) RequestPreVote(id raft.ServerID, target raft.ServerAddress, args *raft.RequestPreVoteRequest,
	resp *raft.RequestPreVoteResponse,
) error {
	if t.p.isolated.Load() {
		return errPartitioned
	}

	return t.Transport.(raft.WithPreVote).RequestPreVote(id, target, args, resp)
}

func (t *partitionedTransport) InstallSnapshot(id raft.ServerID, target raft.ServerAddress, args *raft.InstallSnapshotRequest,
	resp *raft.InstallSnapshotResponse, data io.Reader,
) error {
	if t.p.isolated.Load() {
		return errPartitioned
	}

	return t.Transport.InstallSnapshot(id, target, args, resp, data)
}

func (t *partitionedTransport) TimeoutNow(id raft.ServerID, target raft.ServerAddress, args *raft.TimeoutNowRequest,
	resp *raft.TimeoutNowResponse,
) error {
	if t.p.isolated.Load() {
		return errPartitioned
	}

	return t.Transport.TimeoutNow(id, target, args, resp)
}

func (t *partitionedTransport) Close() error {
	t.once.Do(func() { close(t.done) })

	if c, ok := t.Transport.(raft.WithClose); ok {
		return c.Close()
	}

	return nil
}

type partitionedPipeline struct {
	raft.AppendPipeline
	p *partition
}

func (p partitionedPipeline) AppendEntries(args *raft.AppendEntriesRequest, resp *raft.AppendEntriesResponse) (raft.AppendFuture, error) {
	if p.p.isolated.Load() {
		return nil, errPartitioned
	}

	return p.AppendPipeline.AppendEntries(args, resp)
}

type hangingLogStore struct {
	hung    atomic.Bool
	release chan struct{}
	once    sync.Once
}

func newHangingLogStore() *hangingLogStore {
	return &hangingLogStore{release: make(chan struct{})}
}

func (h *hangingLogStore) hang() { h.hung.Store(true) }

func (h *hangingLogStore) unhang() { h.once.Do(func() { close(h.release) }) }

func (h *hangingLogStore) wrap(s raft.LogStore) raft.LogStore { return hangingLogs{LogStore: s, h: h} }

type hangingLogs struct {
	raft.LogStore
	h *hangingLogStore
}

func (l hangingLogs) StoreLog(e *raft.Log) error { return l.StoreLogs([]*raft.Log{e}) }

func (l hangingLogs) StoreLogs(logs []*raft.Log) error {
	if l.h.hung.Load() {
		<-l.h.release
		return errReleased
	}

	return l.LogStore.StoreLogs(logs)
}

type skewedClock struct {
	offset atomic.Int64
}

func (c *skewedClock) now() time.Time {
	return time.Now().Add(time.Duration(c.offset.Load()))
}

type faultOptions struct {
	offsets         []time.Duration
	logStoreTimeout time.Duration
}

type faultNode struct {
	*env
	name    string
	address string
	clock   *skewedClock
	part    *partition
	logs    *hangingLogStore

	mu    sync.Mutex
	terms []uint64
}

func (n *faultNode) leading() bool {
	ok, _ := n.node.Leader()
	return ok
}

func (n *faultNode) ledTerms() []uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]uint64(nil), n.terms...)
}

type faultCluster struct {
	*testCluster
	nodes []*faultNode
}

func newFaultCluster(t *testing.T, opts faultOptions) *faultCluster {
	t.Helper()

	if testing.Short() {
		t.Skip("fault injection on a multi-node cluster")
	}

	fc := &faultCluster{}

	fc.testCluster = newClusterWith(t, 3, func(i int, dir, address string) *env {
		fn := &faultNode{
			name: fmt.Sprintf("node%d", i), address: address, clock: &skewedClock{}, part: &partition{},
			logs: newHangingLogStore(),
		}

		if i < len(opts.offsets) {
			fn.clock.offset.Store(int64(opts.offsets[i]))
		}

		ctx := context.Background()

		d, err := db.Open(ctx, filepath.Join(dir, "ims.db"))
		if err != nil {
			t.Fatal(err)
		}

		n, err := Open(ctx, Config{
			Dir: filepath.Join(dir, "raft"), Address: address, Version: "test", Now: fn.clock.now,
			Logger: slog.New(slog.DiscardHandler), transport: fn.part.wrap, logStore: fn.logs.wrap,
			logStoreTimeout: opts.logStoreTimeout,
		}, d)
		if err != nil {
			_ = d.Close()

			t.Fatal(err)
		}

		d.SetProposer(n)

		n.Lead(func(_ context.Context, term uint64) {
			fn.mu.Lock()
			fn.terms = append(fn.terms, term)
			fn.mu.Unlock()
		})

		fn.env = &env{dir: dir, db: d, node: n, stop: fn.logs.unhang}

		fc.nodes = append(fc.nodes, fn)

		return fn.env
	})

	t.Cleanup(func() {
		for _, n := range fc.nodes {
			n.logs.unhang()
		}
	})

	waitVoters(t, fc.testCluster, 3)

	return fc
}

func (c *faultCluster) leaderNode() *faultNode {
	leader := c.leader()

	for _, n := range c.nodes {
		if n.env == leader {
			return n
		}
	}

	c.t.Fatal("no leader")

	return nil
}

func (c *faultCluster) newLeader(old *faultNode) *faultNode {
	c.t.Helper()

	var leader *faultNode

	eventually(c.t, "a leader other than "+old.name, func() error {
		for _, n := range c.nodes {
			if n != old && n.leading() {
				leader = n
				return nil
			}
		}

		return errors.New("none yet")
	})

	return leader
}

func (c *faultCluster) transferTo(to *faultNode) {
	c.t.Helper()

	eventually(c.t, "leadership on "+to.name, func() error {
		if to.leading() {
			return nil
		}

		for _, n := range c.nodes {
			if r, _ := n.node.current(); r.State() == raft.Leader && n != to {
				return r.LeadershipTransferToServer(raft.ServerID(to.node.ID()), raft.ServerAddress(to.address)).Error()
			}
		}

		return errors.New("no leader to hand over from")
	})

	eventually(c.t, "leadership established on "+to.name, func() error {
		if !to.leading() {
			return errors.New("not yet")
		}

		return nil
	})
}

func faultUser(i int) db.NewUser {
	return db.NewUser{Email: fmt.Sprintf("user%d@example.org", i), Role: db.RoleReadOnly, PasswordHash: "hash"}
}

func createUser(on *faultNode, i int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := on.db.CreateUser(ctx, faultUser(i), 1000)
	if errors.Is(err, db.ErrDuplicate) {
		return nil
	}

	return err
}

func storeUser(t testing.TB, on *faultNode, i int) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)

	err := createUser(on, i)
	for err != nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)

		err = createUser(on, i)
	}

	if err != nil {
		t.Fatalf("create user %d on %s: %v", i, on.name, err)
	}
}

func waitUser(t testing.TB, on *faultNode, i int) {
	t.Helper()

	eventually(t, fmt.Sprintf("user %d on %s", i, on.name), func() error {
		_, err := on.db.GetUser(context.Background(), faultUser(i).Email)
		return err
	})
}

type leaderTimes struct {
	mu        sync.Mutex
	last      map[string]int64
	decreases []string
}

func watchLeaderTimes(t *testing.T, c *faultCluster) *leaderTimes {
	t.Helper()

	lt := &leaderTimes{last: map[string]int64{}}
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)

		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}

			lt.mu.Lock()

			for _, n := range c.nodes {
				now := n.db.LeaderTime()
				if now < lt.last[n.name] {
					lt.decreases = append(lt.decreases, fmt.Sprintf("%s from %d to %d", n.name, lt.last[n.name], now))
				}

				lt.last[n.name] = now
			}

			lt.mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		close(stop)
		<-done
	})

	return lt
}

func (lt *leaderTimes) check(t *testing.T) {
	t.Helper()

	lt.mu.Lock()
	defer lt.mu.Unlock()

	if len(lt.decreases) > 0 {
		t.Fatalf("leader time went backwards: %v", lt.decreases)
	}
}

func TestLeaderTimeAcrossSkewedClocks(t *testing.T) {
	c := newFaultCluster(t, faultOptions{offsets: []time.Duration{0, 30 * time.Second, -30 * time.Second}})
	base, ahead, behind := c.nodes[0], c.nodes[1], c.nodes[2]

	c.transferTo(base)

	times := watchLeaderTimes(t, c)

	storeUser(t, base, 1)

	c.transferTo(ahead)
	storeUser(t, ahead, 2)

	aheadTime := ahead.db.LeaderTime()

	c.transferTo(behind)
	storeUser(t, behind, 3)

	for _, n := range c.nodes {
		waitUser(t, n, 3)
	}

	times.check(t)

	if got := behind.db.LeaderTime(); got <= aheadTime {
		t.Fatalf("leader time %d under the slow clock, %d under the fast one", got, aheadTime)
	}
}

func TestHungLogStoreHandsLeadershipOver(t *testing.T) {
	const storeTimeout = time.Second

	c := newFaultCluster(t, faultOptions{logStoreTimeout: storeTimeout})
	stuck := c.leaderNode()

	var writer *faultNode

	for _, n := range c.nodes {
		if n != stuck {
			writer = n
			break
		}
	}

	stuck.logs.hang()

	hungAt := time.Now()

	storeUser(t, writer, 1)

	next := c.newLeader(stuck)

	if took := time.Since(hungAt); took > storeTimeout+15*time.Second {
		t.Fatalf("a new leader took %s after the log store hung", took)
	}

	t.Logf("%s led %s after the log store of %s hung", next.name, time.Since(hungAt), stuck.name)

	waitUser(t, next, 1)

	eventually(t, "the stuck node not leading", func() error {
		if stuck.leading() {
			return errors.New("still leading")
		}

		return nil
	})
}

func TestLeadershipLostAndRegained(t *testing.T) {
	c := newFaultCluster(t, faultOptions{})

	home := c.leaderNode()

	var away *faultNode

	for _, n := range c.nodes {
		if n != home {
			away = n
			break
		}
	}

	const writes = 32

	produced := make(chan error, 1)

	go func() {
		defer close(produced)

		for i := range writes {
			on := c.nodes[i%len(c.nodes)]
			deadline := time.Now().Add(10 * time.Second)

			err := createUser(on, i)
			for err != nil && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)

				err = createUser(on, i)
			}

			if err != nil {
				produced <- err
				return
			}

			time.Sleep(15 * time.Millisecond)
		}
	}()

	for range 3 {
		homeRaft, _ := home.node.current()
		awayRaft, _ := away.node.current()

		if err := homeRaft.LeadershipTransferToServer(raft.ServerID(away.node.ID()), raft.ServerAddress(away.address)).Error(); err != nil {
			t.Fatal(err)
		}

		eventually(t, "leadership back on "+home.name, func() error {
			if awayRaft.State() != raft.Leader {
				return errors.New(away.name + " not leading")
			}

			return awayRaft.LeadershipTransferToServer(raft.ServerID(home.node.ID()), raft.ServerAddress(home.address)).Error()
		})

		eventually(t, home.name+" leading again", func() error {
			if !home.leading() {
				return errors.New("not yet")
			}

			return nil
		})
	}

	if err := <-produced; err != nil {
		t.Fatal(err)
	}

	for _, n := range c.nodes {
		for i := range writes {
			waitUser(t, n, i)
		}

		total, err := n.db.CountUsers(context.Background())
		if err != nil || total != writes {
			t.Fatalf("%s has %d users, %v; want %d", n.name, total, err, writes)
		}
	}

	terms := home.ledTerms()
	for i := 1; i < len(terms); i++ {
		if terms[i] <= terms[i-1] {
			t.Fatalf("%s led terms %v", home.name, terms)
		}
	}

	if len(terms) < 4 {
		t.Fatalf("%s led terms %v, want at least 4", home.name, terms)
	}
}
