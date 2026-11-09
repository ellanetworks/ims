package cluster

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/settings"
)

func openLabeled(t testing.TB, dir, address, label string) (*env, error) {
	t.Helper()

	ctx := context.Background()

	d, err := db.Open(ctx, filepath.Join(dir, "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	n, err := Open(ctx, Config{Dir: filepath.Join(dir, "raft"), Address: address, Version: "test", DiameterLabel: label}, d)
	if err != nil {
		_ = d.Close()
		return nil, err
	}

	d.SetProposer(n)

	return &env{dir: dir, db: d, node: n}, nil
}

func mustOpenLabeled(t testing.TB, dir, address, label string) *env {
	t.Helper()

	e, err := openLabeled(t, dir, address, label)
	if err != nil {
		t.Fatal(err)
	}

	return e
}

func waitLabel(t testing.TB, e *env, id, label string, generated bool) {
	t.Helper()

	eventually(t, fmt.Sprintf("label %s of %s on %s", label, id, e.node.ID()), func() error {
		m, err := e.db.Member(context.Background(), id)
		if err != nil {
			return err
		}

		if m.DiameterLabel != label || m.LabelGenerated != generated {
			return fmt.Errorf("label %s, generated %t", m.DiameterLabel, m.LabelGenerated)
		}

		return nil
	})
}

func TestGeneratedLabel(t *testing.T) {
	e := open(t, t.TempDir())
	defer e.close(t)

	waitLeader(t, e.node)

	if e.node.DiameterLabel() != settings.NodeLabel(e.node.ID()) || !e.node.LabelGenerated() {
		t.Fatalf("label = %s, generated %t", e.node.DiameterLabel(), e.node.LabelGenerated())
	}

	waitLabel(t, e, e.node.ID(), settings.NodeLabel(e.node.ID()), true)
}

func TestReplacementTakesOverALabel(t *testing.T) {
	labels := []string{"smsc-a", "smsc-b", "smsc-c"}
	c := newClusterWith(t, 3, func(i int, dir, address string) *env {
		return mustOpenLabeled(t, dir, address, labels[i])
	})

	for i, e := range c.nodes {
		if e.node.DiameterLabel() != labels[i] || e.node.LabelGenerated() {
			t.Fatalf("node %d label = %s, generated %t", i, e.node.DiameterLabel(), e.node.LabelGenerated())
		}

		waitLabel(t, c.leader(), e.node.ID(), labels[i], false)
	}

	old := c.follower()
	oldID, oldLabel := old.node.ID(), old.node.DiameterLabel()

	address := freeAddress(t)
	replacement := mustOpenLabeled(t, t.TempDir(), address, oldLabel)
	c.nodes = append(c.nodes, replacement)
	c.addrs = append(c.addrs, address)

	waitLeader(t, replacement.node)

	token, err := c.leader().node.MintToken(timeout(t), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	err = replacement.node.Join(timeout(t), token.Token)
	if !errors.Is(err, settings.ErrConflict) || !strings.Contains(err.Error(), oldID) {
		t.Fatalf("join with a listed member's label = %v, want a conflict naming %s", err, oldID)
	}

	for i, e := range c.nodes {
		if e == old {
			_ = e.node.Close()
			_ = e.db.Close()
			c.nodes[i] = nil
		}
	}

	if err := c.leader().node.RemoveMember(timeout(t), oldID, true); err != nil {
		t.Fatal(err)
	}

	token, err = c.leader().node.MintToken(timeout(t), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if err := replacement.node.Join(timeout(t), token.Token); err != nil {
		t.Fatalf("join after removing the label's holder: %v", err)
	}

	if replacement.node.ID() == oldID {
		t.Fatal("the replacement reused the removed node's ID")
	}

	waitLabel(t, c.leader(), replacement.node.ID(), oldLabel, false)
	waitLabel(t, replacement, replacement.node.ID(), oldLabel, false)
}

func TestRestartWithAnotherLabel(t *testing.T) {
	c := newClusterWith(t, 3, func(i int, dir, address string) *env {
		return mustOpenLabeled(t, dir, address, fmt.Sprintf("smsc-%d", i))
	})

	idx := 0

	for i, e := range c.nodes {
		if e == c.follower() {
			idx = i
		}
	}

	follower := c.nodes[idx]
	id := follower.node.ID()

	if err := errors.Join(follower.node.Close(), follower.db.Close()); err != nil {
		t.Fatal(err)
	}

	c.nodes[idx] = nil

	taken := "smsc-0"
	if idx == 0 {
		taken = "smsc-1"
	}

	conflicting := mustOpenLabeled(t, follower.dir, c.addrs[idx], taken)
	c.nodes[idx] = conflicting

	eventually(t, "the label conflict", func() error {
		if _, err := conflicting.node.CommittedLabel(context.Background()); !errors.Is(err, settings.ErrConflict) {
			return fmt.Errorf("committed label = %w, want %w", err, settings.ErrConflict)
		}

		return nil
	})

	waitFailing(t, conflicting, CheckLabel, taken)
	waitLabel(t, c.leader(), id, fmt.Sprintf("smsc-%d", idx), false)

	if err := errors.Join(conflicting.node.Close(), conflicting.db.Close()); err != nil {
		t.Fatal(err)
	}

	c.nodes[idx] = nil

	restarted := mustOpenLabeled(t, follower.dir, c.addrs[idx], "")
	c.nodes[idx] = restarted

	if restarted.node.ID() != id {
		t.Fatalf("the restarted node has the ID %s, want %s", restarted.node.ID(), id)
	}

	waitLabel(t, c.leader(), id, settings.NodeLabel(id), true)

	if err := errors.Join(restarted.node.Close(), restarted.db.Close()); err != nil {
		t.Fatal(err)
	}

	c.nodes[idx] = mustOpenLabeled(t, follower.dir, c.addrs[idx], "smsc-new")

	waitLabel(t, c.leader(), id, "smsc-new", false)
}
