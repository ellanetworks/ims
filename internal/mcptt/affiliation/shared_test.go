package affiliation

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip"
)

type flaky struct {
	*memory

	self   string
	others []Row
	down   bool
	writes int
}

func (f *flaky) Self() string { return f.self }

func (f *flaky) GroupAffiliations(ctx context.Context, group sip.URI) ([]Row, error) {
	rs, err := f.memory.GroupAffiliations(ctx, group)
	for i := range rs {
		rs[i].Node = f.self
	}

	for _, r := range f.others {
		if r.Group.String() == group.String() {
			rs = append(rs, r)
		}
	}

	return rs, err
}

func (f *flaky) PutAffiliation(ctx context.Context, r Row) error {
	if f.down {
		return errors.New("no leader")
	}

	f.writes++

	return f.memory.PutAffiliation(ctx, r)
}

// TS 24.379 clause 9.2.2.3.2
// TS 24.379 clause 9.2.2.3.3
func TestSharedControlling(t *testing.T) {
	snap, _ := setup(t)
	g1, a := u(t, "sip:g1@example.com"), u(t, "sip:a@example.com")
	now := time.Unix(1000, 0)

	table := &flaky{
		memory: &memory{groups: map[string]map[string]*Row{}}, self: "node-a",
		others: []Row{{Group: g1, User: a, Node: "node-b", Clients: []string{"c9"}, Expires: now.Add(time.Hour)}},
	}
	c := NewSharedControlling(fixed{snap}, table)

	table.down = true

	if held, err := c.Publish(t.Context(), now, g1, a, []string{"c2", "c1"}); err != nil || !slices.Equal(held, []string{"c1", "c2"}) {
		t.Fatalf("publication while the shared data cannot change = %v, %v", held, err)
	}

	if c.Unsaved() != 1 || c.Affiliated(now, g1, a, "c1") {
		t.Fatalf("%d unsaved, affiliated %v", c.Unsaved(), c.Affiliated(now, g1, a, "c1"))
	}

	table.down = false

	if err := c.Save(t.Context()); err != nil || c.Unsaved() != 0 {
		t.Fatalf("save = %v, %d unsaved", err, c.Unsaved())
	}

	if !c.Affiliated(now, g1, a, "c1") || !c.Affiliated(now, g1, a, "c9") {
		t.Fatal("clients of both nodes not affiliated")
	}

	members := c.Members(now, g1)
	if len(members) != 1 || !slices.Equal(members[0].Nodes, []string{"node-a", "node-b"}) ||
		!slices.Equal(members[0].Clients, []string{"c1", "c2", "c9"}) {
		t.Fatalf("members %+v", members)
	}

	writes := table.writes

	if _, err := c.Publish(t.Context(), now, g1, a, []string{"c1", "c2"}); err != nil || table.writes != writes {
		t.Fatalf("an unchanged publication wrote %d times, %v", table.writes-writes, err)
	}

	if err := c.Retain(t.Context(), now, nil); err != nil {
		t.Fatal(err)
	}

	if members := c.Members(now, g1); len(members) != 1 || !slices.Equal(members[0].Nodes, []string{"node-b"}) {
		t.Fatalf("members after this node retained nothing %+v", members)
	}
}
