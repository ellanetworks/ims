package server_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/controlling"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// change applies the settings edit makes of callConfiguration to w's server, as settings.Live does (its OnChange),
// and runs the strands it posts to.
func (w *world) change(edit func(cfg *settings.Settings)) {
	w.t.Helper()

	cfg := callConfiguration()
	edit(&cfg)

	snap, err := settings.New(cfg)
	if err != nil {
		w.t.Fatal(err)
	}

	w.s.Revoke(w.src.p.Swap(snap), snap)
	w.n.Quiesce()
}

// without removes the groups of drop from the implicit affiliations of the users of cfg, as settings.Live's cascades.
func without(cfg *settings.Settings, drop ...string) {
	for i := range cfg.Users {
		cfg.Users[i].ImplicitAffiliations = slices.DeleteFunc(slices.Clone(cfg.Users[i].ImplicitAffiliations),
			func(g string) bool { return slices.Contains(drop, g) })
	}
}

// TS 24.379 §10.1.1.4.4.3, §6.3.3.1.5, TS 24.380 §6.3: carol, removed from fire while talking in its call, leaves it
// at once: a BYE ends her call, the floor is no longer hers, and the call goes on without her. Fire deleted, its call
// is released (§10.1.1.4.4.2; TS 23.280 §10.2.8 NOTE 1).
func TestMemberRemovedLeavesCall(t *testing.T) {
	w := newWorld(t, nil)
	alice, bob, carol := w.users["alice"], w.users["bob"], w.users["carol"]

	_, cb, cc := w.call(fire)
	w.n.Quiesce()
	w.talk(cc, time.Second)

	if calls := w.calls(); len(calls) != 1 || calls[0].Talker.String() != carol.id {
		t.Fatalf("calls %+v", calls)
	}

	alice.take()
	bob.take()
	carol.take()

	w.change(func(cfg *settings.Settings) {
		cfg.Groups[0].List.Entries = slices.DeleteFunc(cfg.Groups[0].List.Entries,
			func(e settings.Entry) bool { return e.URI == carol.id })
		cfg.Users[2].ImplicitAffiliations = slices.DeleteFunc(slices.Clone(cfg.Users[2].ImplicitAffiliations),
			func(g string) bool { return g == fire })
	})

	carol.expect(t, "ended")
	alice.expectNot(t, "ended")
	bob.expectNot(t, "ended")

	calls := w.calls()
	if len(calls) != 1 {
		t.Fatalf("calls %+v", calls)
	}

	if c := calls[0]; c.Talker.Host != "" || len(c.Participants) != 2 ||
		slices.ContainsFunc(c.Participants, func(p controlling.Participant) bool { return p.ID.String() == carol.id }) {
		t.Errorf("call without carol %+v", c)
	}

	if rs := w.logs.records(t, "participant removed"); len(rs) != 1 || rs[0]["cause"] != "no longer a member" ||
		rs[0]["member"].(map[string]any)["mcptt_id"] != carol.id {
		t.Errorf("records %v", rs)
	}

	// The call goes on: bob takes the floor.
	w.talk(cb, time.Second)

	if calls := w.calls(); len(calls) != 1 || calls[0].Talker.String() != bob.id {
		t.Fatalf("calls %+v", calls)
	}

	w.change(func(cfg *settings.Settings) {
		cfg.Groups = slices.DeleteFunc(cfg.Groups, func(g settings.Group) bool { return g.URI == fire })
		without(cfg, fire)
	})

	alice.expect(t, "ended")
	bob.expect(t, "ended")

	if calls := w.calls(); len(calls) != 0 {
		t.Errorf("calls once fire deleted %+v", calls)
	}

	if rs := w.logs.records(t, "group call released"); len(rs) != 1 || rs[0]["cause"] != "group deleted" {
		t.Errorf("records %v", rs)
	}
}

// A member removed leaving fewer than two participants, the call is released (TS 24.379 §6.3.8.1 2).
func TestMemberRemovedReleasesCall(t *testing.T) {
	w := newWorld(t, nil)
	alice, bob, carol := w.users["alice"], w.users["bob"], w.users["carol"]

	_, _, cc := w.call(fire)
	w.n.Quiesce()
	cc.Leave()
	w.n.Quiesce()
	alice.take()
	bob.take()

	w.change(func(cfg *settings.Settings) {
		cfg.Groups[0].List.Entries = slices.DeleteFunc(cfg.Groups[0].List.Entries,
			func(e settings.Entry) bool { return e.URI == bob.id })
		cfg.Users[1].ImplicitAffiliations = slices.DeleteFunc(slices.Clone(cfg.Users[1].ImplicitAffiliations),
			func(g string) bool { return g == fire })
	})

	alice.expect(t, "ended")
	bob.expect(t, "ended")
	carol.expectNot(t, "incoming")

	if calls := w.calls(); len(calls) != 0 {
		t.Errorf("calls %+v", calls)
	}

	if rs := w.logs.records(t, "group call released"); len(rs) != 1 || rs[0]["cause"] != "too few participants" {
		t.Errorf("records %v", rs)
	}
}

// A change of the implicit affiliations applies to the registered clients at once (TS 24.379 §9.2.2.2.15 by local
// policy): dave, given fire, is affiliated to it implicitly, held by the controlling function, without registering
// again; fire taken back, that affiliation goes, while quorum, which he affiliated to himself (§9.2.2.2.3) and was
// given too, stays.
func TestImplicitAffiliationsChanged(t *testing.T) {
	w := newWorld(t, nil)
	dave := w.users["dave"]
	id := mustURI(t, dave.id)
	client := "urn:uuid:00000000-0000-1000-8000-000000000004"

	groups := func() map[string]affiliation.Group {
		out := map[string]affiliation.Group{}
		for _, g := range w.s.Participating().Groups(w.s.Now(), id, client) {
			out[g.ID.String()] = g
		}

		return out
	}

	var res *sip.Response

	if err := dave.Affiliate([]sip.URI{mustURI(t, quorum)}, func(r *sip.Response, _ error) { res = r }); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	if res == nil || !res.IsSuccess() {
		t.Fatalf("PUBLISH answered %v", res)
	}

	if g := groups(); len(g) != 1 || g[quorum].Status != affiliation.Affiliated || g[quorum].Configured {
		t.Fatalf("dave's affiliations %+v", g)
	}

	w.change(func(cfg *settings.Settings) { cfg.Users[3].ImplicitAffiliations = []string{fire, quorum} })

	g := groups()
	if len(g) != 2 || g[fire].Status != affiliation.Affiliated || !g[fire].Configured ||
		g[quorum].Status != affiliation.Affiliated || g[quorum].Configured {
		t.Fatalf("dave's affiliations once given fire and quorum %+v", g)
	}

	if !w.s.Controlling().Affiliated(w.s.Now(), mustURI(t, fire), id, client) {
		t.Error("fire's owner does not hold dave affiliated")
	}

	w.change(func(cfg *settings.Settings) { cfg.Users[3].ImplicitAffiliations = nil })

	if g := groups(); len(g) != 1 || g[quorum].Status != affiliation.Affiliated {
		t.Errorf("dave's affiliations once fire and quorum taken back %+v", g)
	}

	if w.s.Controlling().Affiliated(w.s.Now(), mustURI(t, fire), id, client) ||
		!w.s.Controlling().Affiliated(w.s.Now(), mustURI(t, quorum), id, client) {
		t.Error("the owners hold the affiliations taken back, or not dave's own")
	}
}

// TS 24.379 §9.2.2.2.3 step 14 a ii: dave, given fire, leaves it by his publication; a change of the settings that
// does not touch his implicit affiliations, of alice's alias, does not affiliate him to fire again. Fire taken back
// and given again, the operator's latest act, it does.
func TestImplicitAffiliationLeftStays(t *testing.T) {
	w := newWorld(t, nil)
	dave := w.users["dave"]
	id := mustURI(t, dave.id)
	client := "urn:uuid:00000000-0000-1000-8000-000000000004"
	given := func(cfg *settings.Settings) { cfg.Users[3].ImplicitAffiliations = []string{fire} }

	affiliated := func() bool {
		return w.s.Participating().Affiliated(w.s.Now(), id, client, mustURI(t, fire)) ||
			w.s.Controlling().Affiliated(w.s.Now(), mustURI(t, fire), id, client)
	}

	w.change(given)

	if !affiliated() {
		t.Fatal("dave not affiliated to fire once given it")
	}

	var res *sip.Response

	if err := dave.Affiliate([]sip.URI{mustURI(t, quorum)}, func(r *sip.Response, _ error) { res = r }); err != nil {
		t.Fatal(err)
	}

	w.n.Quiesce()

	if res == nil || !res.IsSuccess() {
		t.Fatalf("PUBLISH answered %v", res)
	}

	// The de-affiliating entry expires (§9.2.2.2.7).
	w.n.Advance(2 * 64 * transaction.DefaultT1)
	w.n.Quiesce()

	w.change(func(cfg *settings.Settings) {
		given(cfg)
		cfg.Users[0].Alias = "Alice"
	})

	if affiliated() {
		t.Fatal("dave affiliated again to fire, which he left, by a change not of it")
	}

	w.change(func(*settings.Settings) {})
	w.change(given)

	if !affiliated() {
		t.Error("dave not affiliated to fire once given it again")
	}
}
