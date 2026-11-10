package server_test

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt"
	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/server"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/client"
	"github.com/ellanetworks/ims/mcptt/media/mediatest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/ellanetworks/ims/sip/ua"
)

type shared struct {
	mu       sync.Mutex
	rows     map[[3]string]affiliation.Row
	calls    map[string][2]string
	addrs    map[string]netip.AddrPort
	dead     map[string]bool
	refuse   bool
	claims   int
	released int
}

func (s *shared) crashed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dead[id]
}

func (s *shared) crash(t *testing.T, w *imsWorld, id string) {
	t.Helper()

	s.mu.Lock()
	if s.dead == nil {
		s.dead = map[string]bool{}
	}

	s.dead[id] = true
	s.mu.Unlock()

	if err := w.s.Agent().Layer().Close(); err != nil {
		t.Fatal(err)
	}
}

type node struct {
	*shared

	id string
}

func (n *node) GroupAffiliations(_ context.Context, group sip.URI) ([]affiliation.Row, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	var out []affiliation.Row

	for k, r := range n.rows {
		if k[0] == settings.Key(group) {
			out = append(out, r)
		}
	}

	slices.SortFunc(out, func(a, b affiliation.Row) int { return compare(a.Node, b.Node) })

	return out, nil
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}

	return 0
}

func (n *node) NodeAffiliations(context.Context) ([]affiliation.Row, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	var out []affiliation.Row

	for k, r := range n.rows {
		if k[2] == n.id {
			out = append(out, r)
		}
	}

	return out, nil
}

func (n *node) PutAffiliation(_ context.Context, r affiliation.Row) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	k := [3]string{settings.Key(r.Group), settings.Key(r.User), n.id}

	if len(r.Clients) == 0 {
		delete(n.rows, k)
		return nil
	}

	r.Node, r.Clients = n.id, slices.Clone(r.Clients)
	n.rows[k] = r

	return nil
}

func (n *node) Self() string { return n.id }

func (n *node) Route(id string) (sip.URI, netip.AddrPort, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	a, ok := n.addrs[id]
	if !ok || id == n.id {
		return sip.URI{}, netip.AddrPort{}, false
	}

	u := sip.URI{Scheme: "sip", Host: a.Addr().String(), Port: a.Port()}
	u.Params.Set("lr", "")

	return u, n.addrs[n.id], true
}

func (n *node) Peer(a netip.Addr) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	for id, p := range n.addrs {
		if id != n.id && p.Addr() == a {
			return true
		}
	}

	return false
}

func (n *node) GroupCall(_ context.Context, group sip.URI) (string, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	c, ok := n.calls[settings.Key(group)]

	return c[0], ok, nil
}

func (n *node) Claim(_ context.Context, group sip.URI, claim string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if _, ok := n.addrs[n.id]; !ok || n.refuse {
		return "", errors.New("no leader")
	}

	n.claims++

	k := settings.Key(group)
	if c, ok := n.calls[k]; ok && c[0] != n.id {
		return c[0], nil
	}

	n.calls[k] = [2]string{n.id, claim}

	return n.id, nil
}

func (n *node) Release(group sip.URI, claim string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.dead[n.id] {
		return
	}

	k := settings.Key(group)
	if c, ok := n.calls[k]; ok && c == [2]string{n.id, claim} {
		delete(n.calls, k)
		n.released++
	}
}

func (s *shared) lose(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k, c := range s.calls {
		if c[0] == id {
			delete(s.calls, k)
		}
	}
}

func (s *shared) owner(group string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.calls[group][0]
}

func (s *shared) counts() (claims, released int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.claims, s.released
}

type twoNodeWorld struct {
	n      *siptest.Net
	shared *shared
	a, b   *imsWorld
}

func newTwoNodeWorld(t *testing.T) *twoNodeWorld {
	t.Helper()

	sh := &shared{rows: map[[3]string]affiliation.Row{}, calls: map[string][2]string{}, addrs: map[string]netip.AddrPort{}}
	w := newTwoNodeWorldOf(t, sh, &node{shared: sh, id: "node-a"}, &node{shared: sh, id: "node-b"})
	w.shared = sh

	return w
}

func newTwoNodeWorldOf(t *testing.T, sh *shared, na, nb server.Cluster) *twoNodeWorld {
	t.Helper()

	n := siptest.NewNet(t)
	m := mediatest.New(n.Scheduler())

	a, b := newIMSNode(t, n, m, 100, na), newIMSNode(t, n, m, 101, nb)

	sh.mu.Lock()
	sh.addrs[na.Self()], sh.addrs[nb.Self()] = a.addr, b.addr
	sh.mu.Unlock()

	a.user("alice", 1, "", false)
	b.user("bob", 2, "", false)
	b.user("carol", 3, "", false)

	return &twoNodeWorld{n: n, a: a, b: b}
}

// TS 23.379 clause 7.4.2.3.2
// TS 24.379 clause 9.2.2.3.2
func TestGroupAffiliationsAreShared(t *testing.T) {
	w := newTwoNodeWorld(t)
	now := w.a.s.Now()

	members := w.a.s.Controlling().Members(now, mustURI(t, fire))
	nodes := map[string][]string{}

	for _, m := range members {
		nodes[m.ID.String()] = m.Nodes
	}

	if len(members) != 3 || !slices.Equal(nodes["sip:alice@"+domain], []string{"node-a"}) ||
		!slices.Equal(nodes["sip:bob@"+domain], []string{"node-b"}) {
		t.Fatalf("members of fire seen from node A: %v", nodes)
	}

	if !w.a.s.Controlling().Affiliated(now, mustURI(t, fire), mustURI(t, "sip:bob@"+domain), "urn:uuid:00000000-0000-1000-8000-000000000002") {
		t.Fatal("bob, of node B, not affiliated for node A")
	}
}

// TS 23.379 clause 7.4.2.3.2
// TS 24.379 clause 6.3.2.1.3
// TS 24.379 clause 10.1.1.4.1.1
// TS 24.379 clause 10.1.1.3.2
// TS 24.380 clause 6.3.4
func TestGroupCallAcrossNodes(t *testing.T) {
	w := newTwoNodeWorld(t)
	alice, bob, carol := w.a.users["alice"], w.b.users["bob"], w.b.users["carol"]

	cb, err := bob.Call(context.Background(), mustURI(t, police))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	if ok := event(bob.expect(t, "established"), client.EventEstablished).Response; ok == nil || ok.StatusCode != 200 {
		t.Fatalf("200 of bob %+v", ok)
	}

	if owner := w.shared.owner(settings.Key(mustURI(t, police))); owner != "node-b" {
		t.Fatalf("police controlled by %q, want node B, of the initiator", owner)
	}

	ca := event(alice.expect(t, "incoming", "established"), client.EventIncoming).Call
	carol.expect(t, "incoming", "established")

	if invites, _ := w.b.core.requests("INVITE"); len(invites) != 1 || invites[0].URI.String() != carol.impu {
		t.Fatalf("INVITEs of node B to its S-CSCF %v", invites)
	}

	if invites, _ := w.a.core.requests("INVITE"); len(invites) != 1 || invites[0].URI.String() != alice.impu {
		t.Fatalf("INVITEs of node A to its S-CSCF %v", invites)
	} else if cs, _ := invites[0].Header.Contacts(); len(cs) != 1 || cs[0].URI.Host != "mcptt.example.com" || !cs[0].URI.Params.Has("gr") {
		t.Errorf("Contact toward alice %v", invites[0].Header.Values("Contact"))
	}

	ca.PressPTT()
	w.n.Advance(time.Second)
	alice.expect(t, "floor:granted")
	bob.expectAll(t, "floor:taken", "heard")
	ca.ReleasePTT()
	w.n.Advance(time.Second)

	ca.Leave(context.Background())
	w.n.Quiesce()
	alice.expect(t, "ended")
	bob.expectNot(t, "ended")

	ca, err = alice.Call(context.Background(), mustURI(t, police))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	if ok := event(alice.expect(t, "established"), client.EventEstablished).Response; ok == nil || ok.StatusCode != 200 {
		t.Fatalf("200 of alice joining the call of node B: %+v", ok)
	} else if cs, _ := ok.Header.Contacts(); len(cs) != 1 || cs[0].URI.Host != "mcptt.example.com" || !cs[0].URI.Params.Has("gr") {
		t.Errorf("Contact of the 200 to alice %v", ok.Header.Values("Contact"))
	}

	if owner := w.shared.owner(settings.Key(mustURI(t, police))); owner != "node-b" {
		t.Fatalf("police controlled by %q after alice joined", owner)
	}

	ca.Leave(context.Background())
	w.n.Quiesce()
	alice.expect(t, "ended")

	cb.Leave(context.Background())
	w.n.Quiesce()
	bob.expect(t, "ended")
	carol.expect(t, "ended")

	if owner := w.shared.owner(settings.Key(mustURI(t, police))); owner != "" {
		t.Fatalf("police still controlled by %q after the call", owner)
	}

	if claims, released := w.shared.counts(); claims != 1 || released != 1 {
		t.Fatalf("%d claims and %d releases, want one of each", claims, released)
	}
}

// TS 23.379 clause 7.4.2.3.2
// TS 24.379 clause 6.3.2.1.6
func TestGroupCallOfAnotherNode(t *testing.T) {
	w := newTwoNodeWorld(t)
	alice, bob, carol := w.a.users["alice"], w.b.users["bob"], w.b.users["carol"]

	ca, err := alice.Call(context.Background(), mustURI(t, police))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()
	alice.expect(t, "established")

	cb := event(bob.expect(t, "incoming", "established"), client.EventIncoming).Call
	cc := event(carol.expect(t, "incoming", "established"), client.EventIncoming).Call

	if owner := w.shared.owner(settings.Key(mustURI(t, police))); owner != "node-a" {
		t.Fatalf("police controlled by %q, want node A", owner)
	}

	cb.PressPTT()
	w.n.Advance(time.Second)
	bob.expect(t, "floor:granted")
	alice.expectAll(t, "floor:taken", "heard")
	cb.ReleasePTT()
	w.n.Advance(time.Second)

	cc.Leave(context.Background())
	w.n.Quiesce()
	carol.expect(t, "ended")
	alice.expectNot(t, "ended")

	ca.Leave(context.Background())
	w.n.Quiesce()
	alice.expect(t, "ended")
	bob.expect(t, "ended")
}

// TS 23.379 clause 7.4.2.3.2
// TS 23.380 clause 1
func TestGroupCallOwnerLost(t *testing.T) {
	w := newTwoNodeWorld(t)
	alice, bob, carol := w.a.users["alice"], w.b.users["bob"], w.b.users["carol"]

	if _, err := alice.Call(context.Background(), mustURI(t, police)); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()
	alice.expect(t, "established")
	bob.expect(t, "incoming", "established")
	carol.expect(t, "incoming", "established")

	w.shared.crash(t, w.a, "node-a")

	for range 4 {
		w.n.Advance(10 * time.Minute)
	}

	bob.expect(t, "ended")
	carol.expect(t, "ended")

	if owner := w.shared.owner(settings.Key(mustURI(t, police))); owner != "node-a" {
		t.Fatalf("police controlled by %q, want the lost node A until the leader releases it", owner)
	}

	if _, err := bob.Call(context.Background(), mustURI(t, police)); err != nil {
		t.Fatal(err)
	}

	w.n.Advance(time.Minute)
	bob.expect(t, "failed")

	w.shared.lose("node-a")

	cb, err := bob.Call(context.Background(), mustURI(t, police))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	if es := bob.take(); event(es, client.EventEstablished) == nil {
		if f := event(es, client.EventFailed); f != nil && f.Response != nil {
			t.Fatalf("bob's new call: %s %v", f.Response.StartLine(), f.Response.Header.Values("Warning"))
		}

		t.Fatalf("bob's new call: %v", kinds(es))
	}

	carol.expect(t, "incoming", "established")

	if owner := w.shared.owner(settings.Key(mustURI(t, police))); owner != "node-b" {
		t.Fatalf("police controlled by %q after node A was lost, want node B", owner)
	}

	cb.Leave(context.Background())
	w.n.Advance(time.Minute)
	bob.expect(t, "ended")
}

// RFC 3261 section 9.2
func TestGroupCallCancelledAcrossNodes(t *testing.T) {
	for _, owner := range []string{"node-a", "node-b"} {
		t.Run(owner, func(t *testing.T) { cancelledAcrossNodes(t, owner) })
	}
}

func cancelledAcrossNodes(t *testing.T, owner string) {
	w := newTwoNodeWorld(t)
	alice := w.a.users["alice"]

	var (
		mu        sync.Mutex
		cancelled []string
	)

	for _, u := range []*imsUser{w.b.users["bob"], w.b.users["carol"]} {
		u.onInvite = func(r *ua.Request) ua.Handler {
			_ = r.Progress(ua.Reply{Code: 180})

			return ua.Handler{Ended: func(_ context.Context, _ *ua.Session, e ua.End) {
				if e.Cause != ua.EndCancelled {
					return
				}

				mu.Lock()
				defer mu.Unlock()

				cancelled = append(cancelled, u.name)
			}}
		}
	}

	police := settings.Key(mustURI(t, police))

	if owner == "node-b" {
		w.shared.mu.Lock()
		w.shared.calls[police] = [2]string{owner, "stale"}
		w.shared.mu.Unlock()
	}

	ca, err := alice.Call(context.Background(), mustURI(t, police))
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()
	alice.expectNot(t, "established")

	if invites, _ := w.b.core.requests("INVITE"); len(invites) != 2 {
		t.Fatalf("INVITEs of node B to its S-CSCF %v", invites)
	}

	if got := w.shared.owner(police); got != owner {
		t.Fatalf("police controlled by %q, want %s", got, owner)
	}

	ca.Leave(context.Background())
	w.n.Quiesce()

	mu.Lock()
	slices.Sort(cancelled)
	got := slices.Clone(cancelled)
	mu.Unlock()

	if !slices.Equal(got, []string{"bob", "carol"}) {
		t.Fatalf("INVITEs cancelled at %v, want bob and carol", got)
	}

	if got := w.shared.owner(police); got != "" {
		t.Fatalf("police still controlled by %q", got)
	}
}

type dbNode struct {
	*mcptt.Shared

	addrs *shared
}

func (n *dbNode) Route(id string) (sip.URI, netip.AddrPort, bool) {
	return (&node{shared: n.addrs, id: n.Node}).Route(id)
}

func (n *dbNode) Peer(a netip.Addr) bool { return (&node{shared: n.addrs, id: n.Node}).Peer(a) }

func (n *dbNode) Release(group sip.URI, claim string) {
	if !n.addrs.crashed(n.Node) {
		n.Shared.Release(group, claim)
	}
}

// TS 23.379 clause 7.4.2.3.2
// TS 24.379 clause 9.2.2.3.2
// TS 24.379 clause 10.1.1.3.2
func TestGroupCallAcrossNodesThroughTheDatabase(t *testing.T) {
	const (
		nodeA = "0192a000-0000-7000-8000-0000000000aa"
		nodeB = "0192a000-0000-7000-8000-0000000000bb"
	)

	d, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	for i, id := range []string{nodeA, nodeB} {
		if err := d.RegisterMember(t.Context(), db.Member{
			NodeID: id, Address: []string{"10.0.0.1:7000", "10.0.0.2:7000"}[i], Version: "test", SchemaVersion: 1,
			DiameterLabel: []string{"ims-a", "ims-b"}[i],
		}); err != nil {
			t.Fatal(err)
		}
	}

	sh := &shared{addrs: map[string]netip.AddrPort{}}
	sa, sb := &mcptt.Shared{DB: d, Node: nodeA}, &mcptt.Shared{DB: d, Node: nodeB}
	w := newTwoNodeWorldOf(t, sh, &dbNode{Shared: sa, addrs: sh}, &dbNode{Shared: sb, addrs: sh})
	alice, bob, carol := w.a.users["alice"], w.b.users["bob"], w.b.users["carol"]
	police := mustURI(t, police)

	owner := func() string {
		sa.Wait()
		sb.Wait()

		c, err := d.MCPTTGroupCall(t.Context(), police)
		if errors.Is(err, db.ErrNotFound) {
			return ""
		}

		if err != nil {
			t.Fatal(err)
		}

		return c.NodeID
	}

	if rows, err := d.MCPTTGroupAffiliations(t.Context(), police); err != nil || len(rows) != 3 {
		t.Fatalf("affiliations to police %+v, %v", rows, err)
	}

	if _, err := alice.Call(context.Background(), police); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()
	alice.expect(t, "established")
	cb := event(bob.expect(t, "incoming", "established"), client.EventIncoming).Call
	carol.expect(t, "incoming", "established")

	if got := owner(); got != nodeA {
		t.Fatalf("police controlled by %q, want node A", got)
	}

	cb.PressPTT()
	w.n.Advance(time.Second)
	bob.expect(t, "floor:granted")
	alice.expectAll(t, "floor:taken", "heard")
	cb.ReleasePTT()
	w.n.Advance(time.Second)

	sh.crash(t, w.a, nodeA)

	for range 4 {
		w.n.Advance(10 * time.Minute)
	}

	bob.expect(t, "ended")
	carol.expect(t, "ended")

	if got := owner(); got != nodeA {
		t.Fatalf("police controlled by %q, want the lost node A until the leader releases it", got)
	}

	if n, err := d.ReleaseMCPTTGroupCallsOf(t.Context(), nodeA); err != nil || n != 1 {
		t.Fatalf("release of the calls of node A = %d, %v", n, err)
	}

	cb, err = bob.Call(context.Background(), police)
	if err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()
	bob.expect(t, "established")
	carol.expect(t, "incoming", "established")

	if got := owner(); got != nodeB {
		t.Fatalf("police controlled by %q after node A was lost, want node B", got)
	}

	cb.Leave(context.Background())
	w.n.Advance(time.Minute)
	bob.expect(t, "ended")

	if got := owner(); got != "" {
		t.Fatalf("police still controlled by %q after the call", got)
	}
}

// TS 23.379 clause 7.4.2.3.2
func TestGroupCallNotClaimed(t *testing.T) {
	w := newTwoNodeWorld(t)
	alice, bob := w.a.users["alice"], w.b.users["bob"]

	w.shared.mu.Lock()
	w.shared.refuse = true
	w.shared.mu.Unlock()

	if _, err := alice.Call(context.Background(), mustURI(t, police)); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	if res := event(alice.expect(t, "failed"), client.EventFailed).Response; res == nil || res.StatusCode != 500 {
		t.Fatalf("alice's call while the claim fails: %v", res)
	}

	bob.expectNot(t, "incoming")

	w.shared.mu.Lock()
	w.shared.refuse = false
	w.shared.calls[settings.Key(mustURI(t, police))] = [2]string{"node-a", "stale"}
	w.shared.mu.Unlock()

	if _, err := bob.Call(context.Background(), mustURI(t, police)); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()
	bob.expect(t, "established")
	alice.expect(t, "incoming", "established")

	if owner := w.shared.owner(settings.Key(mustURI(t, police))); owner != "node-a" {
		t.Fatalf("police controlled by %q, want node A, which had claimed it", owner)
	}
}
