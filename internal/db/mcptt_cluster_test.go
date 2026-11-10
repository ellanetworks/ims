package db

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
)

const (
	nodeA = "0192a000-0000-7000-8000-0000000000aa"
	nodeB = "0192a000-0000-7000-8000-0000000000bb"
)

func twoMembers(t *testing.T) *DB {
	t.Helper()

	d := openTestDB(t)

	for i, id := range []string{nodeA, nodeB} {
		if err := d.RegisterMember(t.Context(), Member{
			NodeID: id, Address: []string{"10.0.0.1:7000", "10.0.0.2:7000"}[i], Version: "test", SchemaVersion: 1,
			DiameterLabel: []string{"ims-a", "ims-b"}[i], MCPTTAddresses: []string{[]string{"10.0.0.1:5070", "10.0.0.2:5070"}[i]},
		}); err != nil {
			t.Fatal(err)
		}
	}

	return d
}

func TestMemberMCPTTAddresses(t *testing.T) {
	d := twoMembers(t)

	m, err := d.Member(t.Context(), nodeB)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(m.MCPTTAddresses, []string{"10.0.0.2:5070"}) {
		t.Fatalf("MCPTT addresses %v", m.MCPTTAddresses)
	}

	m.MCPTTAddresses = []string{"10.0.0.2:5070", "[2001:db8::2]:5070"}
	if err := d.RegisterMember(t.Context(), m); err != nil {
		t.Fatal(err)
	}

	if m, _ = d.Member(t.Context(), nodeB); len(m.MCPTTAddresses) != 2 {
		t.Fatalf("MCPTT addresses after the update %v", m.MCPTTAddresses)
	}
}

// TS 24.379 clause 9.2.2.3.2
func TestMCPTTGroupAffiliations(t *testing.T) {
	d := twoMembers(t)
	ctx := t.Context()
	fire, police := mustURI(t, "sip:fire@mcptt.example.com"), mustURI(t, "sip:police@mcptt.example.com")
	alice, bob := mustURI(t, "sip:alice@mcptt.example.com"), mustURI(t, "sip:bob@mcptt.example.com")
	expires := time.Unix(5000, 0)

	for _, p := range []struct {
		node string
		r    affiliation.Row
	}{
		{nodeA, affiliation.Row{Group: fire, User: alice, Clients: []string{"c1"}, Expires: expires}},
		{nodeB, affiliation.Row{Group: fire, User: alice, Clients: []string{"c2"}, Expires: expires}},
		{nodeA, affiliation.Row{Group: police, User: alice, Clients: []string{"c1"}, Expires: expires}},
		{nodeA, affiliation.Row{Group: fire, User: bob, Clients: []string{"c1", "c3"}, Expires: expires}},
	} {
		if err := d.PutMCPTTAffiliation(ctx, p.node, p.r); err != nil {
			t.Fatal(err)
		}
	}

	rs, err := d.MCPTTGroupAffiliations(ctx, mustURI(t, "sip:fire@MCPTT.example.com"))
	if err != nil {
		t.Fatal(err)
	}

	if len(rs) != 3 || rs[0].Node != nodeA || rs[1].Node != nodeB || !slices.Equal(rs[2].Clients, []string{"c1", "c3"}) ||
		!rs[0].Expires.Equal(expires) {
		t.Fatalf("rows of fire %+v", rs)
	}

	if err := d.PutMCPTTAffiliation(ctx, nodeA, affiliation.Row{Group: fire, User: alice}); err != nil {
		t.Fatal(err)
	}

	if rs, _ := d.MCPTTGroupAffiliations(ctx, fire); len(rs) != 2 || rs[0].Node != nodeB || rs[1].User.String() != bob.String() {
		t.Fatalf("rows of fire after the deaffiliation %+v", rs)
	}

	if rs, _ := d.MCPTTNodeAffiliations(ctx, nodeA); len(rs) != 2 || rs[1].Group.String() != police.String() {
		t.Fatalf("rows of node A %+v", rs)
	}

	err = d.PutMCPTTAffiliation(ctx, "0192a000-0000-7000-8000-0000000000cc", affiliation.Row{
		Group: fire, User: alice, Clients: []string{"c9"}, Expires: expires,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("an affiliation of a node not in the cluster = %v", err)
	}

	if err := d.RemoveMember(ctx, nodeB, RemovedForced); err != nil {
		t.Fatal(err)
	}

	if rs, _ := d.MCPTTGroupAffiliations(ctx, fire); len(rs) != 1 || rs[0].Node != nodeA {
		t.Fatalf("rows after the removal of node B %+v", rs)
	}
}

// TS 23.379 clause 7.4.2.3.2
func TestMCPTTGroupCallClaims(t *testing.T) {
	d := twoMembers(t)
	ctx := t.Context()
	fire := mustURI(t, "sip:fire@mcptt.example.com")

	c, err := d.ClaimMCPTTGroupCall(ctx, fire, nodeA, "a1")
	if err != nil || c.NodeID != nodeA || c.Claim != "a1" {
		t.Fatalf("first claim = %+v, %v", c, err)
	}

	if c, err = d.ClaimMCPTTGroupCall(ctx, mustURI(t, "sip:fire@Mcptt.Example.com"), nodeB, "b1"); err != nil || c.NodeID != nodeA {
		t.Fatalf("claim of a controlled group = %+v, %v", c, err)
	}

	if c, err = d.ClaimMCPTTGroupCall(ctx, fire, nodeA, "a2"); err != nil || c.Claim != "a2" {
		t.Fatalf("claim again by the owner = %+v, %v", c, err)
	}

	if err := d.ReleaseMCPTTGroupCall(ctx, fire, nodeA, "a1"); err != nil {
		t.Fatal(err)
	}

	if c, err = d.MCPTTGroupCall(ctx, fire); err != nil || c.Claim != "a2" {
		t.Fatalf("a stale release removed the call: %+v, %v", c, err)
	}

	if err := d.ReleaseMCPTTGroupCall(ctx, fire, nodeA, "a2"); err != nil {
		t.Fatal(err)
	}

	if _, err := d.MCPTTGroupCall(ctx, fire); !errors.Is(err, ErrNotFound) {
		t.Fatalf("released call = %v", err)
	}

	if c, err = d.ClaimMCPTTGroupCall(ctx, fire, nodeB, "b2"); err != nil || c.NodeID != nodeB {
		t.Fatalf("claim of a released group = %+v, %v", c, err)
	}

	if _, err := d.ClaimMCPTTGroupCall(ctx, mustURI(t, "sip:police@mcptt.example.com"), nodeB, "b3"); err != nil {
		t.Fatal(err)
	}

	if n, err := d.ReleaseMCPTTGroupCallsOf(ctx, nodeB); err != nil || n != 2 {
		t.Fatalf("release of node B's calls = %d, %v", n, err)
	}

	if _, err := d.ClaimMCPTTGroupCall(ctx, fire, nodeB, "b4"); err != nil {
		t.Fatal(err)
	}

	if err := d.RemoveMember(ctx, nodeB, RemovedForced); err != nil {
		t.Fatal(err)
	}

	if calls, err := d.MCPTTGroupCalls(ctx); err != nil || len(calls) != 0 {
		t.Fatalf("calls of a removed node %+v, %v", calls, err)
	}

	if _, err := d.ClaimMCPTTGroupCall(ctx, fire, nodeB, "b5"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim by a removed node = %v", err)
	}
}
