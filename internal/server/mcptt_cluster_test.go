package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api/apitest"
	"github.com/ellanetworks/ims/internal/config"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
)

func mcpttConfig(t *testing.T, media netip.Addr) *config.MCPTT {
	t.Helper()

	legs := unusedPort(t) &^ 1

	control := unusedPort(t)
	for control&^1 == legs {
		control = unusedPort(t)
	}

	return &config.MCPTT{
		Mode: config.MCPTTModeIMS, Port: unusedPort(t),
		Media: config.MCPTTMedia{Address: media, MinPort: legs, MaxPort: legs + 1, ControlPort: control},
	}
}

func optionsFrom(t *testing.T, from *siptest.Socket, to netip.AddrPort) int {
	t.Helper()

	req := siptest.NewRequest("OPTIONS", "sip:"+to.String(), sip.UDP, from.Addr())
	from.Send(sip.UDP, to, req)

	for {
		r := from.Recv()
		if res, ok := r.Msg.(*sip.Response); ok && res.StatusCode >= 200 {
			return res.StatusCode
		}
	}
}

// TS 23.379 clause 7.4.2.3.2
// TS 24.379 clause 9.2.2.3.2
// TS 24.229 clause 4.4.1
func TestMCPTTAcrossNodes(t *testing.T) {
	second := netip.MustParseAddr("127.0.0.2")

	cfgA := clusterConfig(t, "ims-a")
	cfgA.SIP.Addresses = []netip.Addr{loopback}
	cfgA.MCPTT = mcpttConfig(t, loopback)

	a := startNode(t, cfgA)
	a.waitStarted(t)

	token := a.mintToken(t)

	cfgB := joinerConfig(t, "ims-mcptt")
	cfgB.SIP.Addresses = []netip.Addr{second}
	cfgB.MCPTT = mcpttConfig(t, second)

	b := startNode(t, cfgB)
	b.waitStarted(t)

	if code := b.join(t, token); code != http.StatusOK {
		t.Fatalf("POST /api/v1/cluster/join = %d", code)
	}

	eventuallyFor(t, "a login on the joined node", func() bool {
		_, code := b.login(t, apitest.Email)
		return code == http.StatusOK
	})

	idA, idB := a.srv.node.ID(), b.srv.node.ID()
	mcpttA := netip.AddrPortFrom(loopback, uint16(cfgA.MCPTT.Port))
	mcpttB := netip.AddrPortFrom(second, uint16(cfgB.MCPTT.Port))

	if !udpInUse(mcpttB) {
		t.Fatal("the MCPTT AS of a cluster member has no socket for the other members")
	}

	eventuallyFor(t, "the MCPTT addresses of the members", func() bool {
		return slices.Equal(a.srv.peers.mcpttOf(idB), []netip.AddrPort{mcpttB}) &&
			slices.Equal(b.srv.peers.mcpttOf(idA), []netip.AddrPort{mcpttA})
	})

	route, local, ok := a.srv.mcpttCluster.Route(idB)
	if !ok || route.String() != "sip:"+mcpttB.String()+";lr" || local != mcpttA {
		t.Fatalf("route from A to the MCPTT AS of B = %s from %s, %v", route, local, ok)
	}

	if _, _, ok := a.srv.mcpttCluster.Route(idA); ok {
		t.Fatal("a route from A to itself")
	}

	outsider := siptest.NewSocket(t, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.3"), 0))
	if code := optionsFrom(t, outsider, mcpttB); code != 403 {
		t.Fatalf("OPTIONS from outside the cluster = %d, want 403", code)
	}

	member := siptest.NewSocket(t, netip.AddrPortFrom(loopback, 0))
	if code := optionsFrom(t, member, mcpttB); code != 200 {
		t.Fatalf("OPTIONS from the address of member A = %d, want 200", code)
	}

	fire, err := sip.ParseURI("sip:fire@mcptt.example.com")
	if err != nil {
		t.Fatal(err)
	}

	bob, err := sip.ParseURI("sip:bob@mcptt.example.com")
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()

	if err := b.srv.mcpttCluster.PutAffiliation(ctx, affiliation.Row{
		Group: fire, User: bob, Clients: []string{"c2"}, Expires: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	eventuallyFor(t, "bob's affiliation through node B on node A", func() bool {
		rows, err := a.srv.mcpttCluster.GroupAffiliations(ctx, fire)
		return err == nil && len(rows) == 1 && rows[0].Node == idB && slices.Equal(rows[0].Clients, []string{"c2"})
	})

	if owner, err := a.srv.mcpttCluster.Claim(ctx, fire, "a1"); err != nil || owner != idA {
		t.Fatalf("claim of A = %s, %v", owner, err)
	}

	if owner, err := b.srv.mcpttCluster.Claim(ctx, fire, "b1"); err != nil || owner != idA {
		t.Fatalf("claim of B = %s, %v, want the call of A", owner, err)
	}

	a.srv.mcpttCluster.Release(fire, "a1")
	a.srv.mcpttCluster.Wait()

	eventuallyFor(t, "the release of the call on node B", func() bool {
		_, ok, err := b.srv.mcpttCluster.GroupCall(ctx, fire)
		return err == nil && !ok
	})
}

// TS 23.379 clause 7.4.2.3.2
func TestMCPTTSweepReleasesCallsOfLostNodes(t *testing.T) {
	n := startNode(t, clusterConfig(t, "ims-a"))
	n.waitStarted(t)

	ctx := t.Context()
	lost := "0192a000-0000-7000-8000-0000000000ee"
	d := n.srv.database

	eventuallyFor(t, "the leadership", func() bool {
		leading, _ := n.srv.node.Leader()
		return leading
	})

	if err := d.RegisterMember(ctx, db.Member{
		NodeID: lost, Address: "127.0.0.9:7000", Version: "test", SchemaVersion: 1, DiameterLabel: "ims-lost",
	}); err != nil {
		t.Fatal(err)
	}

	fire, _ := sip.ParseURI("sip:fire@mcptt.example.com")
	police, _ := sip.ParseURI("sip:police@mcptt.example.com")

	if _, err := d.ClaimMCPTTGroupCall(ctx, fire, lost, "x"); err != nil {
		t.Fatal(err)
	}

	if _, err := d.ClaimMCPTTGroupCall(ctx, police, n.srv.node.ID(), "y"); err != nil {
		t.Fatal(err)
	}

	w := mcpttSweep{database: d, node: n.srv.node, after: time.Millisecond, logger: slog.New(slog.DiscardHandler)}

	eventuallyFor(t, "the release of the calls of the lost node", func() bool {
		w.sweep(ctx)

		calls, err := d.MCPTTGroupCalls(ctx)

		return err == nil && len(calls) == 1 && calls[0].NodeID == n.srv.node.ID()
	})

	w.after = time.Hour
	w.sweep(context.Background())

	if calls, _ := d.MCPTTGroupCalls(ctx); len(calls) != 1 {
		t.Fatalf("calls after a sweep %+v", calls)
	}
}
