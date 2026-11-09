package cluster

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
)

func waitTombstone(t testing.TB, e *env, id string) {
	t.Helper()

	eventually(t, "the tombstone of "+id, func() error {
		tombstones, err := e.db.Tombstones(context.Background())
		if err != nil {
			return err
		}

		if !slices.ContainsFunc(tombstones, func(ts db.Tombstone) bool { return ts.NodeID == id && ts.Fingerprint != "" }) {
			return fmt.Errorf("tombstones %+v", tombstones)
		}

		return nil
	})
}

func waitRemoved(t testing.TB, e *env) {
	t.Helper()

	select {
	case <-e.node.Removed():
	case <-time.After(20 * time.Second):
		t.Fatalf("node %s was not fenced", e.node.ID())
	}
}

func TestRemovedWhileDownIsFencedOnRestart(t *testing.T) {
	labels := []string{"ims-a", "ims-b", "ims-c"}
	c := newClusterWith(t, 3, func(i int, dir, address string) *env {
		return mustOpenLabeled(t, dir, address, labels[i])
	})

	c.initialize()

	waitVoters(t, c, 3)

	leader, victim := c.leader(), c.follower()
	i := slices.Index(c.nodes, victim)
	dir, address, id, label := victim.dir, victim.node.cfg.Address, victim.node.ID(), victim.node.DiameterLabel()

	_ = victim.node.Close()
	_ = victim.db.Close()
	c.nodes[i] = nil

	if err := leader.node.RemoveMember(timeout(t), id, true); err != nil {
		t.Fatal(err)
	}

	waitTombstone(t, leader, id)

	address2 := freeAddress(t)
	replacement := mustOpenLabeled(t, t.TempDir(), address2, label)
	c.nodes = append(c.nodes, replacement)
	c.addrs = append(c.addrs, address2)

	waitLeader(t, replacement.node)

	token, err := c.leader().node.MintToken(timeout(t), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if err := replacement.node.Join(timeout(t), token.Token); err != nil {
		t.Fatal(err)
	}

	waitLabel(t, c.leader(), replacement.node.ID(), label, false)

	restarted := mustOpenLabeled(t, dir, address, label)
	c.nodes[i] = restarted

	waitRemoved(t, restarted)
	waitFailing(t, restarted, CheckMember, "removed")

	if _, err := restarted.db.CreateUser(timeout(t), db.NewUser{Email: "x@example.com", Role: db.RoleAdmin}, 10); !errors.Is(err, ErrRemoved) {
		t.Fatalf("write on a fenced node = %v, want %v", err, ErrRemoved)
	}

	_ = restarted.node.Close()
	_ = restarted.db.Close()
	c.nodes[i] = nil

	d, err := db.Open(context.Background(), filepath.Join(dir, "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	fenced, err := Open(context.Background(), Config{Dir: filepath.Join(dir, "raft"), Address: address, Version: "test"}, d)
	if err != nil {
		t.Fatalf("Open after fencing = %v, want a fenced node", err)
	}

	select {
	case <-fenced.Removed():
	default:
		t.Fatal("the reopened node is not fenced")
	}

	if f := fenced.Formation(); f.State != FormationRemoved {
		t.Fatalf("formation = %+v, want %s", f, FormationRemoved)
	}

	checks := fenced.Readiness(context.Background(), true)
	if i := slices.IndexFunc(checks, func(c Check) bool { return c.Name == CheckMember }); i < 0 || checks[i].OK {
		t.Fatalf("checks of the reopened node = %+v, want member failing", checks)
	}

	if err := fenced.Close(); err != nil {
		t.Fatal(err)
	}

	for _, e := range c.nodes {
		if e != nil {
			waitReady(t, e, false)
		}
	}
}

func TestRemovedInARaftPartitionIsFenced(t *testing.T) {
	parts := []*partition{{}, {}, {}}
	c := newClusterWith(t, 3, func(i int, dir, address string) *env {
		return openWith(t, dir, address, func(cfg *Config) { cfg.transport = parts[i].wrap })
	})

	c.initialize()

	waitVoters(t, c, 3)

	leader, victim := c.leader(), c.follower()
	i := slices.Index(c.nodes, victim)

	parts[i].isolate()

	waitFailing(t, victim, CheckLeader, "no contact")

	if err := leader.node.RemoveMember(timeout(t), victim.node.ID(), true); err != nil {
		t.Fatal(err)
	}

	waitRemoved(t, victim)

	if _, err := victim.db.Member(context.Background(), victim.node.ID()); err != nil {
		t.Fatalf("the fenced node applied its own removal: %v", err)
	}

	parts[i].heal()
}

func TestRemovalFencesTheCertificate(t *testing.T) {
	c := newCluster(t, 2)
	c.initialize()
	waitVoters(t, c, 1)

	leader, follower := c.leader(), c.follower()

	fp := follower.node.certs.fingerprint()

	if leader.node.certs.fenced(fp) {
		t.Fatal("a member's certificate is fenced")
	}

	waitReady(t, follower, false)

	if err := leader.node.RemoveMember(timeout(t), follower.node.ID(), true); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the fenced fingerprint", func() error {
		if !leader.node.certs.fenced(fp) {
			return errors.New("not fenced")
		}

		return nil
	})

	waitRemoved(t, follower)
}
