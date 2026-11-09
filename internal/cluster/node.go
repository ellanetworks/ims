package cluster

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/listener"
	"github.com/ellanetworks/ims/internal/cluster/pki"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"
	raftwal "github.com/hashicorp/raft-wal"
)

const (
	logCacheSize     = 512
	logStoreTimeout  = 10 * time.Second
	snapshotInterval = 2 * time.Minute
	trailingLogs     = 10240
	snapshotEntries  = 8192
	soleVoterTimeout = 100 * time.Millisecond
	commitTimeout    = 5 * time.Millisecond
	maxPool          = 3
	transportTimeout = 10 * time.Second
	retryEstablish   = 5 * time.Second

	idFile      = "node-id"
	joinedFile  = "joined"
	joiningFile = "joining"
	removedFile = "removed"
	logDir      = "wal"
)

var (
	ErrNotLeader         = errors.New("not the leader")
	ErrDatabaseBehindLog = errors.New("the database is missing entries that the raft log no longer holds; restore a backup, or remove this node from the cluster, wipe its data directory and join it again")
	ErrRemoved           = errors.New("this node was removed from the cluster: delete its data directory to join it again")
	ErrNotClustered      = errors.New("this node has no cluster address: set cluster in its configuration file")
)

type Config struct {
	Dir             string
	Address         string
	Advertise       string
	Version         string
	DiameterLabel   string
	Join            []string
	BootstrapExpect int
	SecretFile      string
	Now             func() time.Time
	Logger          *slog.Logger

	transport       func(raft.Transport) raft.Transport
	logStore        func(raft.LogStore) raft.LogStore
	logStoreTimeout time.Duration
	readyLag        uint64
	removalAfter    time.Duration
}

func (c Config) clustered() bool {
	return c.Address != ""
}

type Node struct {
	cfg       Config
	id        string
	label     string
	generated bool
	db        *db.DB
	certs     *certs

	metrics *metrics
	ln      *listener.Listener
	rpc     *http.Server
	peers   *peerClient
	health  *health

	raftMu    sync.RWMutex
	raft      *raft.Raft
	store     *raftwal.WAL
	transport raft.Transport
	batcher   *batcher
	notify    chan bool

	mu      sync.Mutex
	term    uint64
	since   time.Time
	ready   chan struct{}
	lead    []func(ctx context.Context, term uint64)
	leading context.Context
	cancel  context.CancelFunc
	runs    sync.WaitGroup

	joinMu       sync.Mutex
	checkpointed bool
	formation    *formation
	auth         *joinAuth
	removed      chan struct{}
	removedOnce  sync.Once
	done         chan struct{}
	stopped      chan struct{}
	background   sync.WaitGroup
}

func Open(ctx context.Context, cfg Config, d *db.DB) (*Node, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	if cfg.Advertise == "" {
		cfg.Advertise = cfg.Address
	}

	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the raft directory: %w", err)
	}

	id, err := nodeID(cfg.Dir)
	if err != nil {
		return nil, err
	}

	label, generated := cfg.DiameterLabel, false
	if label == "" {
		label, generated = settings.NodeLabel(id), true
	}

	c, err := loadCerts(cfg.Dir, id)
	if err != nil {
		return nil, err
	}

	n := &Node{
		cfg: cfg, id: id, label: label, generated: generated, db: d, certs: c, notify: make(chan bool, 16), ready: make(chan struct{}),
		removed: make(chan struct{}), done: make(chan struct{}), stopped: make(chan struct{}),
	}

	d.SetProposer(n)

	n.health = newHealth(n)
	n.metrics = newMetrics()
	n.formation = newFormation(n)
	n.auth = newJoinAuth(cfg.SecretFile, cfg.Now, cfg.Logger)

	if _, err := os.Stat(filepath.Join(cfg.Dir, removedFile)); err == nil {
		n.removedOnce.Do(func() { close(n.removed) })
		n.formation.set(FormationRemoved, ErrRemoved.Error())
		n.cfg.Logger.WarnContext(ctx, "this node was removed from the cluster: it stays fenced until its data directory is deleted")

		go n.watch()

		return n, nil
	}

	if _, err := os.Stat(filepath.Join(cfg.Dir, joiningFile)); err == nil {
		if err := n.finishJoin(); err != nil {
			return nil, fmt.Errorf("finish an interrupted join: %w", err)
		}
	}

	d.OnEvents(n.onEvents)

	if err := n.certs.refresh(ctx, d); err != nil {
		return nil, err
	}

	if cfg.clustered() {
		n.ln = listener.New(listener.Config{
			Address: cfg.Address, Cert: n.certs.presented, Pins: n.certs.pin, Removed: n.certs.fenced, Logger: cfg.Logger,
		})
		n.peers = newPeerClient(n)
		n.serveRPC()

		if err := n.ln.Start(ctx); err != nil {
			n.abandon()
			return nil, err
		}
	}

	if err := n.startRaft(ctx); err != nil {
		n.abandon()
		return nil, err
	}

	go n.watch()

	n.background.Go(n.maintain)

	if n.cfg.clustered() {
		n.background.Go(n.discover)
	}

	return n, nil
}

func nodeID(dir string) (string, error) {
	path := filepath.Join(dir, idFile)

	raw, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(raw))
		if !pki.ValidID(id) {
			return "", fmt.Errorf("node ID %q in %s is not a UUID: delete the raft directory", id, path)
		}

		return id, nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read the node ID: %w", err)
	}

	id := pki.NewID()

	if err := writeFile(path, []byte(id+"\n")); err != nil {
		return "", fmt.Errorf("write the node ID: %w", err)
	}

	return id, nil
}

func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}

	if err := os.Rename(tmp, path); err != nil {
		return err
	}

	return syncDir(filepath.Dir(path))
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}

	defer func() { _ = dir.Close() }()

	return dir.Sync()
}

func (n *Node) electionTimeout() time.Duration {
	if !n.cfg.clustered() {
		return soleVoterTimeout
	}

	return raft.DefaultConfig().ElectionTimeout
}

func (n *Node) startRaft(ctx context.Context) error {
	store, err := openLogStore(filepath.Join(n.cfg.Dir, logDir))
	if err != nil {
		return fmt.Errorf("open the raft log: %w", err)
	}

	snapshots, err := newSnapshotStore(n.db, filepath.Join(n.cfg.Dir, "snapshots"))
	if err != nil {
		_ = store.Close()
		return err
	}

	var inner raft.LogStore = store
	if n.cfg.logStore != nil {
		inner = n.cfg.logStore(store)
	}

	logs, err := raft.NewLogCache(logCacheSize, timeoutLogStore{LogStore: inner, timeout: cmp.Or(n.cfg.logStoreTimeout, logStoreTimeout)})
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("open the raft log: %w", err)
	}

	existing, err := raft.HasExistingState(logs, store, raft.NewInmemSnapshotStore())
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("read the raft state: %w", err)
	}

	if existing {
		first, err := logs.FirstIndex()
		if err != nil {
			_ = store.Close()
			return fmt.Errorf("read the raft log: %w", err)
		}

		if applied := n.db.AppliedIndex(); first > applied+1 {
			_ = store.Close()
			return fmt.Errorf("%w: the database is at index %d but the raft log starts at %d", ErrDatabaseBehindLog, applied, first)
		}
	} else {
		reset := n.db.ResetPosition
		if n.joined() {
			reset = func(ctx context.Context) error { return n.db.Wipe(ctx, mkdirAll(n.scratch())) }
		}

		if err := reset(ctx); err != nil {
			_ = store.Close()
			return err
		}
	}

	conf := raft.DefaultConfig()
	conf.LocalID = raft.ServerID(n.id)

	if !n.cfg.clustered() {
		conf.HeartbeatTimeout, conf.ElectionTimeout, conf.LeaderLeaseTimeout = soleVoterTimeout, soleVoterTimeout,
			soleVoterTimeout/2
	}

	conf.CommitTimeout = commitTimeout
	conf.BatchApplyCh = true
	conf.MaxAppendEntries = 64
	conf.NoSnapshotRestoreOnStart = true
	conf.SnapshotInterval = snapshotInterval
	conf.SnapshotThreshold = snapshotEntries
	conf.TrailingLogs = trailingLogs
	conf.NotifyCh = n.notify
	conf.Logger = hclog.FromStandardLogger(slog.NewLogLogger(n.cfg.Logger.Handler(), slog.LevelWarn),
		&hclog.LoggerOptions{Name: "raft", Level: hclog.Warn})

	var (
		transport raft.Transport
		addr      raft.ServerAddress
	)

	if n.cfg.clustered() {
		transport = raft.NewNetworkTransportWithConfig(&raft.NetworkTransportConfig{
			Stream:          n.ln.Stream(listener.ALPNRaft, n.cfg.Advertise),
			MaxPool:         maxPool,
			MaxRPCsInFlight: 1,
			Timeout:         transportTimeout,
			Logger:          conf.Logger,
		})
		addr = raft.ServerAddress(n.cfg.Advertise)

		if n.cfg.transport != nil {
			transport = n.cfg.transport(transport)
		}
	} else {
		addr, transport = raft.NewInmemTransport(raft.ServerAddress(n.id))
	}

	r, err := raft.NewRaft(conf, &fsm{db: n.db, dir: filepath.Join(n.cfg.Dir, "snapshots")}, logs, store, snapshots,
		transport)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("start raft: %w", err)
	}

	forming := !existing && !n.joined() && n.cfg.clustered() && len(n.cfg.Join) > 0

	if forming {
		n.formation.set(FormationForming, "looking for the other nodes")
	} else {
		n.formation.set(FormationMember, "")
	}

	if !existing && !n.joined() && !forming {
		err := r.BootstrapCluster(raft.Configuration{Servers: []raft.Server{
			{Suffrage: raft.Voter, ID: conf.LocalID, Address: addr},
		}}).Error()
		if err != nil {
			_ = r.Shutdown().Error()
			_ = store.Close()

			return fmt.Errorf("bootstrap: %w", err)
		}
	}

	n.raftMu.Lock()
	defer n.raftMu.Unlock()

	select {
	case <-n.done:
		_ = r.Shutdown().Error()
		_ = store.Close()

		if c, ok := transport.(raft.WithClose); ok {
			_ = c.Close()
		}

		return raft.ErrRaftShutdown
	default:
	}

	n.raft, n.store, n.transport, n.batcher = r, store, transport, newBatcher(r, n.cfg.Now, n.db.LeaderTime)

	return nil
}

func (n *Node) stopRaft() error {
	n.raftMu.Lock()
	defer n.raftMu.Unlock()

	if n.batcher == nil {
		return nil
	}

	n.batcher.close()
	n.batcher = nil

	err := errors.Join(n.raft.Shutdown().Error(), n.store.Close())

	if c, ok := n.transport.(raft.WithClose); ok {
		err = errors.Join(err, c.Close())
	}

	return err
}

func (n *Node) joined() bool {
	_, err := os.Stat(filepath.Join(n.cfg.Dir, joinedFile))
	return err == nil
}

func (n *Node) current() (*raft.Raft, *batcher) {
	n.raftMu.RLock()
	defer n.raftMu.RUnlock()

	return n.raft, n.batcher
}

type batcherProposer struct{ b *batcher }

func (p batcherProposer) Propose(ctx context.Context, cmds []db.Command) ([]db.Result, error) {
	results, _, err := p.b.propose(ctx, cmds)

	return results, err
}

func (n *Node) ID() string {
	return n.id
}

func (n *Node) DiameterLabel() string {
	return n.label
}

func (n *Node) LabelGenerated() bool {
	return n.generated
}

func (n *Node) Clustered() bool {
	return n.cfg.clustered()
}

func (n *Node) Lead(f func(ctx context.Context, term uint64)) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.lead = append(n.lead, f)

	select {
	case <-n.ready:
		n.run(n.leading, n.term, f)
	default:
	}
}

func (n *Node) run(ctx context.Context, term uint64, f func(ctx context.Context, term uint64)) {
	n.runs.Add(1)

	go func() {
		defer n.runs.Done()

		f(ctx, term)
	}()
}

func (n *Node) watch() {
	defer close(n.stopped)

	for {
		select {
		case <-n.done:
			n.stepDown()
			return
		case leader := <-n.notify:
			n.stepDown()

			if leader {
				n.becomeLeader()
			}
		}
	}
}

func (n *Node) becomeLeader() {
	ctx, cancel := context.WithCancel(context.Background())

	n.mu.Lock()
	n.cancel = cancel
	n.mu.Unlock()

	n.runs.Add(1)

	go func() {
		defer n.runs.Done()

		term, err := n.establish(ctx)
		if err != nil {
			if ctx.Err() == nil {
				n.cfg.Logger.ErrorContext(ctx, "failed to establish leadership, handing it over", slog.Any("error", err))

				r, _ := n.current()
				if err := r.LeadershipTransfer().Error(); err != nil {
					n.retryLeadership(ctx)
				}
			}

			return
		}

		n.mu.Lock()
		defer n.mu.Unlock()

		if ctx.Err() != nil {
			return
		}

		n.term, n.leading, n.since = term, ctx, n.cfg.Now()
		close(n.ready)

		for _, f := range n.lead {
			n.run(ctx, term, f)
		}

		n.run(ctx, term, n.runAutopilot)
		n.run(ctx, term, n.runUpgrades)
	}()
}

func (n *Node) retryLeadership(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(retryEstablish):
		r, _ := n.current()

		select {
		case n.notify <- r.State() == raft.Leader:
		default:
		}
	}
}

func (n *Node) establish(ctx context.Context) (uint64, error) {
	_, b := n.current()
	if b == nil {
		return 0, raft.ErrRaftShutdown
	}

	for {
		term, err := n.db.LeaderStart(ctx, batcherProposer{b}, n.id)
		if err == nil {
			if err := n.checkpoint(); err != nil {
				return 0, err
			}

			if err := n.registerSelf(ctx, batcherProposer{b}); err != nil {
				return 0, err
			}

			return term, nil
		}

		if ctx.Err() != nil || errors.Is(err, raft.ErrNotLeader) || errors.Is(err, raft.ErrLeadershipLost) {
			return 0, err
		}

		n.cfg.Logger.WarnContext(ctx, "failed to start the leader term, retrying", slog.Any("error", err))

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (n *Node) checkpoint() error {
	if n.checkpointed {
		return nil
	}

	r, _ := n.current()

	future := r.GetConfiguration()
	if err := future.Error(); err != nil {
		return fmt.Errorf("read the raft configuration: %w", err)
	}

	if servers := future.Configuration().Servers; len(servers) != 1 || servers[0].ID != raft.ServerID(n.id) {
		n.checkpointed = true
		return nil
	}

	retention := r.ReloadableConfig()
	compacting := retention
	compacting.TrailingLogs = 0

	if err := r.ReloadConfig(compacting); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}

	snapErr := r.Snapshot().Error()

	if err := r.ReloadConfig(retention); err != nil {
		return errors.Join(fmt.Errorf("checkpoint: %w", err), snapErr)
	}

	if snapErr != nil {
		return fmt.Errorf("checkpoint: %w", snapErr)
	}

	n.checkpointed = true

	return nil
}

func (n *Node) stepDown() {
	n.mu.Lock()
	cancel := n.cancel
	n.cancel = nil

	select {
	case <-n.ready:
		n.ready = make(chan struct{})
	default:
	}

	n.term = 0
	n.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	n.runs.Wait()
}

func (n *Node) Leader() (bool, uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()

	select {
	case <-n.ready:
		return true, n.term
	default:
		return false, 0
	}
}

func (n *Node) readyCh() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()

	return n.ready
}

func (n *Node) Snapshot() error {
	r, _ := n.current()

	return r.Snapshot().Error()
}

func (n *Node) Removed() <-chan struct{} {
	return n.removed
}

func (n *Node) fence(ctx context.Context, peer string) {
	select {
	case <-n.removed:
		return
	default:
	}

	n.cfg.Logger.WarnContext(ctx, "a cluster member reports that this node was removed from the cluster",
		slog.String("member", peer))

	n.markRemoved()
}

func (n *Node) markRemoved() {
	n.removedOnce.Do(func() {
		if err := writeFile(filepath.Join(n.cfg.Dir, removedFile), nil); err != nil {
			n.cfg.Logger.Error("failed to record the removal", slog.Any("error", err))
		}

		n.cfg.Logger.Warn("this node was removed from the cluster")

		n.formation.set(FormationRemoved, ErrRemoved.Error())

		close(n.removed)
	})
}

func (n *Node) closeListener() {
	if n.rpc != nil {
		_ = n.rpc.Close()
	}

	if n.peers != nil {
		n.peers.close()
	}

	if n.ln != nil {
		n.ln.Close()
	}
}

func (n *Node) abandon() {
	close(n.done)
	n.closeListener()
	n.background.Wait()
}

func (n *Node) Close() error {
	close(n.done)
	<-n.stopped

	n.background.Wait()

	err := n.stopRaft()

	n.closeListener()

	return err
}
