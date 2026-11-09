package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

type env struct {
	dir  string
	db   *db.DB
	node *Node
	stop func()
}

func open(t testing.TB, dir string) *env {
	t.Helper()

	ctx := context.Background()

	d, err := db.Open(ctx, filepath.Join(dir, "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	n, err := Open(ctx, Config{Dir: filepath.Join(dir, "raft")}, d)
	if err != nil {
		_ = d.Close()

		t.Fatal(err)
	}

	d.SetProposer(n)

	return &env{dir: dir, db: d, node: n}
}

func (e *env) close(t *testing.T) {
	t.Helper()

	if err := errors.Join(e.node.Close(), e.db.Close()); err != nil {
		t.Fatal(err)
	}
}

func waitLeader(t testing.TB, n *Node) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		if ok, _ := n.Leader(); ok {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("no leader")
}

func timeout(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	return ctx
}

func TestStandaloneSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	e := open(t, dir)

	id, err := e.db.CreateUser(timeout(t), db.NewUser{Email: "a@example.com", Role: db.RoleAdmin, PasswordHash: "h"}, 10)
	if err != nil {
		t.Fatal(err)
	}

	before := e.db.AppliedIndex()
	e.close(t)

	e = open(t, dir)
	defer e.close(t)

	waitLeader(t, e.node)

	if u, err := e.db.GetUserByID(context.Background(), id); err != nil || u.Email != "a@example.com" {
		t.Fatalf("user = %+v, %v", u, err)
	}

	if _, err := e.db.CreateUser(timeout(t), db.NewUser{Email: "b@example.com", Role: db.RoleAdmin}, 10); err != nil {
		t.Fatal(err)
	}

	if after := e.db.AppliedIndex(); after <= before {
		t.Fatalf("applied index %d after restart, %d before", after, before)
	}
}

func TestRaftOverAnExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	d, err := db.Open(ctx, filepath.Join(dir, "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	for _, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		if _, err := d.CreateUser(ctx, db.NewUser{Email: email, Role: db.RoleAdmin}, 10); err != nil {
			t.Fatal(err)
		}
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	e := open(t, dir)
	defer e.close(t)

	waitLeader(t, e.node)

	if _, total, err := e.db.ListUsers(ctx, 1, 10); err != nil || total != 3 {
		t.Fatalf("%d users, %v; want the 3 from before raft", total, err)
	}

	if _, err := e.db.CreateUser(timeout(t), db.NewUser{Email: "d@example.com", Role: db.RoleAdmin}, 10); err != nil {
		t.Fatal(err)
	}

	snapshots, err := e.node.raft.GetConfiguration().Configuration(), error(nil)
	if err != nil || len(snapshots.Servers) != 1 {
		t.Fatalf("configuration = %+v", snapshots)
	}

	first, err := e.storeFirstIndex()
	if err != nil {
		t.Fatal(err)
	}

	p, err := e.db.Position(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if first <= 1 || p.Index < first-1 {
		t.Fatalf("log starts at %d, database at %d: the sole-node checkpoint did not compact the log", first, p.Index)
	}
}

func (e *env) storeFirstIndex() (uint64, error) { return e.node.store.FirstIndex() }

func TestRefusesADatabaseBehindTheLog(t *testing.T) {
	dir := t.TempDir()
	e := open(t, dir)

	if _, err := e.db.CreateUser(timeout(t), db.NewUser{Email: "a@example.com", Role: db.RoleAdmin}, 10); err != nil {
		t.Fatal(err)
	}

	if first, err := e.storeFirstIndex(); err != nil || first <= 1 {
		t.Fatalf("log starts at %d, %v: want a compacted log", first, err)
	}

	e.close(t)

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(dir, "ims.db"+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}

	d, err := db.Open(context.Background(), filepath.Join(dir, "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	n, err := Open(context.Background(), Config{Dir: filepath.Join(dir, "raft")}, d)
	if err == nil {
		_ = n.Close()
	}

	if !errors.Is(err, ErrDatabaseBehindLog) {
		t.Fatalf("Open over an empty database = %v, want %v", err, ErrDatabaseBehindLog)
	}
}

func TestSnapshotInstallsOnAnotherDatabase(t *testing.T) {
	ctx := context.Background()

	e := open(t, t.TempDir())
	defer e.close(t)

	waitLeader(t, e.node)

	if _, err := e.db.CreateUser(timeout(t), db.NewUser{Email: "a@example.com", Role: db.RoleAdmin}, 10); err != nil {
		t.Fatal(err)
	}

	store, err := newSnapshotStore(e.db, filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}

	m, rc, err := store.Open(databaseSnapshot)
	if err != nil {
		t.Fatal(err)
	}

	otherDir := t.TempDir()

	other, err := db.Open(ctx, filepath.Join(otherDir, "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = other.Close() }()

	target, err := newSnapshotStore(other, filepath.Join(otherDir, "in"))
	if err != nil {
		t.Fatal(err)
	}

	s, err := target.Create(raft.SnapshotVersionMax, m.Index, m.Term, m.Configuration, m.ConfigurationIndex, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := copyAll(s, rc); err != nil {
		t.Fatal(err)
	}

	_ = rc.Close()

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	_, installed, err := target.Open(s.ID())
	if err != nil {
		t.Fatal(err)
	}

	f := &fsm{db: other, dir: filepath.Join(otherDir, "in")}
	if err := f.Restore(installed); err != nil {
		t.Fatal(err)
	}

	if _, total, err := other.ListUsers(ctx, 1, 10); err != nil || total != 1 {
		t.Fatalf("%d users after the install, %v", total, err)
	}

	want, _, _ := e.db.Hash(ctx)
	got, index, _ := other.Hash(ctx)

	if string(want) != string(got) || index != m.Index || other.AppliedIndex() != m.Index {
		t.Fatalf("installed at %d (applied %d) hash %x, want %d %x", index, other.AppliedIndex(), got, m.Index, want)
	}
}

func TestLeadRunsWhileLeader(t *testing.T) {
	e := open(t, t.TempDir())

	started := make(chan uint64, 1)
	stopped := make(chan struct{})

	e.node.Lead(func(ctx context.Context, term uint64) {
		started <- term

		<-ctx.Done()

		close(stopped)
	})

	select {
	case term := <-started:
		if term == 0 {
			t.Fatal("term 0")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("never led")
	}

	e.close(t)

	select {
	case <-stopped:
	default:
		t.Fatal("still leading after close")
	}
}
