package server

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

// memState is a State in memory, as the database keeps it.
type memState struct {
	mu   sync.Mutex
	regs map[string]registrar.Record
	affs map[[2]string]affiliation.Record
	// fail fails the writes.
	fail bool
}

func newMemState() *memState {
	return &memState{regs: map[string]registrar.Record{}, affs: map[[2]string]affiliation.Record{}}
}

func (m *memState) SaveRegistrations(_ context.Context, records []registrar.Record, deleted []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.fail {
		return errors.New("disk full")
	}

	for _, c := range deleted {
		delete(m.regs, c)
	}

	for _, r := range records {
		m.regs[r.ClientID] = r
	}

	return nil
}

func (m *memState) SaveAffiliations(_ context.Context, records []affiliation.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range records {
		k := [2]string{settings.Key(r.MCPTTID), r.ClientID}
		if len(r.Groups) == 0 && r.ETag == "" {
			delete(m.affs, k)
		} else {
			m.affs[k] = r
		}
	}

	return nil
}

func (m *memState) records() ([]registrar.Record, []affiliation.Record) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var (
		regs []registrar.Record
		affs []affiliation.Record
	)

	for _, r := range m.regs {
		regs = append(regs, r)
	}

	for _, r := range m.affs {
		affs = append(affs, r)
	}

	return regs, affs
}

// groups are the groups of the affiliations kept of client of user.
func (m *memState) groups(user, client string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []string
	for _, g := range m.affs[[2]string{user, client}].Groups {
		out = append(out, g.ID.String())
	}

	return out
}

// restart shuts the server down and starts another, of the same state, on another address, which restores its
// registrations and affiliations and subscribes again; the S-CSCF answers the SUBSCRIBEs.
func (h *harness) restart(state *memState) {
	h.t.Helper()

	old := h.s
	h.n.Await(func() { _ = old.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1)
	h.scscf.pump()

	h.addr = h.n.Addr()
	h.start(state)

	regs, affs := state.records()
	h.s.Restore(regs, affs)
	h.s.Resubscribe()

	h.n.Advance(time.Second)
	h.scscf.pump()
}

// activeContact is a <contact> of a reg event document, active, of the registration token id.
func activeContact(id string) string {
	return `<contact id="` + id + `" state="active" event="registered"><uri>sip:ue@192.0.2.9</uri></contact>`
}

// The bindings and affiliations through the IMS outlive a restart: a client registered, and affiliated explicitly
// and implicitly, is served after it as before; one that deregistered meanwhile, absent from the first full document
// of the reg event subscription made again, is logged off; and what is restored expires as it would have.
func TestRestore(t *testing.T) {
	st := newMemState()
	h := newHarnessOf(t, registrar.ThroughIMS, st)
	s := h.scscf
	s.regEvent = true

	ue1, ue2 := h.client(aliceIM), h.client(bobIM)

	if res := s.send(s.register(aliceIM, "t1", 600, ue1.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("alice: %d", res.StatusCode)
	}

	if res := s.send(s.register(bobIM, "t2", 600, ue2.register("sip:ims.example.com", 600, info(token(bob), client2)))); res.StatusCode != 200 {
		t.Fatalf("bob: %d", res.StatusCode)
	}

	if res := s.send(s.forward(ue1.publish(alice, client1, 4294967295, fire, police), aliceIM)); res.StatusCode != 200 {
		t.Fatalf("PUBLISH: %d", res.StatusCode)
	}

	s.pump()

	if got := st.groups(settings.Key(uri(t, alice)), client1); !slices.Equal(got, []string{fire, police}) {
		t.Fatalf("alice's affiliations kept %v", got)
	}

	// The S-CSCF has alice's UE registered still, bob's no longer.
	s.initial[aliceIM] = `<registration aor="` + aliceIM + `" id="r1" state="active">` + activeContact("t1") + `</registration>`
	s.initial[bobIM] = `<registration aor="` + bobIM + `" id="r2" state="active"/>`

	h.restart(st)

	if s.subs[aliceIM] == nil || s.subs[bobIM] == nil {
		t.Fatalf("subscriptions %v", s.subs)
	}

	b, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1)
	if !ok || b.MCPTTID.String() != alice || b.Token != "t1" || !b.SCSCF.Equivalent(s.contact("").URI) {
		t.Fatalf("alice after the restart: %+v, %v", b, ok)
	}

	if !h.affiliated(alice, client1, fire) || !h.affiliated(alice, client1, police) {
		t.Fatal("alice not affiliated after the restart")
	}

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, bobIM)}, client2); ok || h.affiliated(bob, client2, fire) {
		t.Fatal("bob, deregistered meanwhile, served")
	}

	if regs, _ := st.records(); len(regs) != 1 || st.groups(settings.Key(uri(t, bob)), client2) != nil {
		t.Fatalf("kept %d registrations, bob's affiliations %v", len(regs), st.groups(settings.Key(uri(t, bob)), client2))
	}

	// A refresh of the third-party REGISTER, as before the restart: its Call-ID, a CSeq higher; past the expiry of
	// the registration restored, the refreshed one stays, until its own.
	h.n.Advance(500 * time.Second)

	if res := s.send(s.register(aliceIM, "t1", 600, ue1.register("sip:ims.example.com", 600))); res.StatusCode != 200 {
		t.Fatalf("refresh: %d", res.StatusCode)
	}

	h.n.Advance(200 * time.Second)

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok {
		t.Fatal("alice's refreshed registration expired")
	}

	h.n.Advance(10 * time.Minute)

	if regs, affs := st.records(); len(regs) != 0 || len(affs) != 0 || h.affiliated(alice, client1, fire) {
		t.Errorf("after the expiry: %d registrations, %d affiliations kept", len(regs), len(affs))
	}
}

// TS 24.229 §5.7.1.1 a; TS 23.003 §13.4: the bindings restored of a tel URI IMPU whose implicit registration set has
// no SIP URI, as of Ella Core's HSS, are reconciled by the subscription made again to the SIP URI of its number,
// "user=phone": the client of a UE the first full document reports registered, of the tel URI or of the SIP URI,
// stays; the other, deregistered meanwhile, is logged off.
func TestRestoreTelURI(t *testing.T) {
	for _, aor := range []string{bobTel, bobPhone} {
		t.Run(aor, func(t *testing.T) {
			st := newMemState()
			h := newHarnessOf(t, registrar.ThroughIMS, st)
			s := h.scscf
			s.regEvent = true
			s.pau = []string{bobTel}

			ue1, ue2 := h.client(bobTel), h.client(bobTel)

			for i, ue := range []*client{ue1, ue2} {
				c := []string{client1, client2}[i]
				if res := s.send(s.register(bobTel, []string{"t1", "t2"}[i], 600, ue.register("sip:ims.example.com", 600, info(token(bob), c)))); res.StatusCode != 200 {
					t.Fatalf("UE %d: %d", i, res.StatusCode)
				}
			}

			s.pump()

			if s.subs[bobPhone] == nil {
				t.Fatalf("subscriptions %v", s.subs)
			}

			// The S-CSCF has the first UE registered still, the second no longer.
			delete(s.subs, bobPhone)
			s.subscribes = 0
			s.initial[bobPhone] = `<registration aor="` + aor + `" id="r1" state="active">` + activeContact("t1") + `</registration>`

			h.restart(st)

			if s.subs[bobPhone] == nil || s.subscribes != 1 {
				t.Fatalf("subscriptions %v, %d SUBSCRIBEs", s.subs, s.subscribes)
			}

			if b, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, bobTel)}, client1); !ok || b.Token != "t1" {
				t.Fatalf("the client registered still: %+v, %v", b, ok)
			}

			if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, bobTel)}, client2); ok {
				t.Fatal("the client deregistered meanwhile served")
			}

			if regs, _ := st.records(); len(regs) != 1 || regs[0].ClientID != client1 {
				t.Fatalf("registrations kept %+v", regs)
			}
		})
	}
}

// restoredAlice is a harness of alice registered through the IMS, and her registration kept in its state, with one
// expired and one of a user no longer configured, which are not restored.
func restoredAlice(t *testing.T) (*harness, *memState) {
	t.Helper()

	st := newMemState()
	h := newHarnessOf(t, registrar.ThroughIMS, st)
	s := h.scscf
	s.regEvent = true

	ue := h.client(aliceIM)
	ue.instance = "urn:uuid:00000000-0000-1000-8000-000000000001"

	if res := s.send(s.register(aliceIM, "t1", 600, ue.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("alice: %d", res.StatusCode)
	}

	s.pump()

	regs, _ := st.records()
	stale := regs[0]
	stale.ClientID, stale.Expires = "expired", h.now()
	unknown := regs[0]
	unknown.ClientID, unknown.MCPTTID = "unknown", uri(t, "sip:carol@mcptt.example.com")

	if err := st.SaveRegistrations(t.Context(), []registrar.Record{stale, unknown}, nil); err != nil {
		t.Fatal(err)
	}

	return h, st
}

// A 480 to the reg event subscription made again, of an IMPU the S-CSCF has no binding of (TS 24.229 §5.4.2.1.1
// step 0), removes the bindings restored of it; the expired ones, and those of a user no longer configured, are not
// restored.
func TestRestoreUnregistered(t *testing.T) {
	h, st := restoredAlice(t)
	h.scscf.regEvent, h.scscf.refusal = false, 480

	h.restart(st)

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); ok || h.affiliated(alice, client1, fire) {
		t.Fatal("served after a 480")
	}

	if regs, affs := st.records(); len(regs) != 0 || len(affs) != 0 {
		t.Errorf("kept %d registrations, %d affiliations", len(regs), len(affs))
	}
}

// A 403, of the server not authorised to subscribe (step 1), leaves the registration state unknown: the bindings
// restored stay, and the SUBSCRIBE is not sent again (RFC 3261 §21.4.4).
func TestRestoreForbidden(t *testing.T) {
	h, st := restoredAlice(t)
	h.scscf.regEvent = false
	h.scscf.subscribes = 0

	h.restart(st)

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok || !h.affiliated(alice, client1, fire) {
		t.Error("not served after a 403")
	}

	h.n.Advance(time.Hour)
	h.scscf.pump()

	if h.scscf.subscribes != 1 {
		t.Errorf("%d SUBSCRIBEs", h.scscf.subscribes)
	}
}

// A 500, of an S-CSCF unavailable, subscribes again after its Retry-After or a backoff; the first full document then
// reconciles the bindings restored, matched by the instance ID of their UE, and taking the expiry of its contact.
func TestRestoreRetry(t *testing.T) {
	h, st := restoredAlice(t)
	s := h.scscf
	s.regEvent, s.refusal = false, 500
	s.subscribes = 0

	h.restart(st)

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok || !h.affiliated(alice, client1, fire) {
		t.Fatal("not served after a 500")
	}

	s.regEvent = true
	s.initial[aliceIM] = `<registration aor="` + aliceIM + `" id="r1" state="active"><contact id="c9" state="active" ` +
		`event="registered" expires="3000"><uri>sip:ue@192.0.2.9</uri><unknown-param name="+sip.instance">"&lt;` +
		`urn:uuid:00000000-0000-1000-8000-000000000001&gt;"</unknown-param></contact></registration>`

	h.n.Advance(time.Minute)
	s.pump()

	if s.subscribes != 2 {
		t.Fatalf("%d SUBSCRIBEs", s.subscribes)
	}

	// Past the expiry restored, within the contact's.
	h.n.Advance(30 * time.Minute)

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok {
		t.Error("alice expired at the expiry restored, not the contact's")
	}

	if regs, _ := st.records(); len(regs) != 1 || regs[0].Expires.Before(h.now().Add(10*time.Minute)) {
		t.Errorf("registrations kept %+v", regs)
	}
}

// A reg event subscription of no response, of an S-CSCF unreachable as while the core of the IMS restarts, is made
// again once it is reachable (Resubscribe), not only at the next third-party REGISTER, though its bindings were not
// restored.
func TestSubscribeUnanswered(t *testing.T) {
	st := newMemState()
	h := newHarnessOf(t, registrar.ThroughIMS, st)
	s := h.scscf
	s.regEvent, s.silent = true, true

	ue := h.client(aliceIM)
	ue.instance = "urn:uuid:00000000-0000-1000-8000-000000000001"

	if res := s.send(s.register(aliceIM, "t1", 600, ue.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("alice: %d", res.StatusCode)
	}

	s.pump()

	// The SUBSCRIBE times out (RFC 3261 §17.1.2.2), unanswered.
	h.n.Advance(64 * transaction.DefaultT1)
	s.pump()

	s.silent = false
	h.s.Resubscribe()

	h.n.Advance(time.Second)
	s.pump()

	if s.subs[aliceIM] == nil {
		t.Fatal("no reg event subscription once the S-CSCF answers")
	}

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok {
		t.Error("alice not served")
	}
}

// A reg event document the server cannot read is answered 400, which keeps the subscription (RFC 6665 §4.2.2), and a
// refresh asks for the full state again (RFC 3680 §5.2); the bindings stay.
func TestUnreadableDocument(t *testing.T) {
	h := newHarness(t, registrar.ThroughIMS)
	s := h.scscf
	s.regEvent = true
	ue := h.client(aliceIM)

	if res := s.send(s.register(aliceIM, "t1", 600, ue.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("alice: %d", res.StatusCode)
	}

	s.pump()
	refreshes := s.refreshes

	s.notify(aliceIM, 1, "full", `<registration aor="`+aliceIM+`" id="r1" state="gone"/>`)

	if res := s.notified[len(s.notified)-1]; res.StatusCode != 400 {
		t.Fatalf("NOTIFY of an unreadable document: %d", res.StatusCode)
	}

	h.n.Advance(time.Second)
	s.pump()

	if s.refreshes != refreshes+1 {
		t.Fatalf("%d refreshes, want one more than %d", s.refreshes, refreshes)
	}

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok {
		t.Error("alice not served")
	}
}

// A change of the settings takes effect at once (TS 23.280 §5.2.5): the clients of a user deleted are logged off, and
// a member removed from a group is de-affiliated from it, at both functions; the others stay.
func TestRevoke(t *testing.T) {
	st := newMemState()
	h := newHarnessOf(t, registrar.ThroughIMS, st)
	s := h.scscf
	s.regEvent = true

	ue1, ue2 := h.client(aliceIM), h.client(bobIM)

	if res := s.send(s.register(aliceIM, "t1", 600, ue1.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("alice: %d", res.StatusCode)
	}

	if res := s.send(s.register(bobIM, "t2", 600, ue2.register("sip:ims.example.com", 600, info(token(bob), client2)))); res.StatusCode != 200 {
		t.Fatalf("bob: %d", res.StatusCode)
	}

	if res := s.send(s.forward(ue1.publish(alice, client1, 4294967295, fire, police), aliceIM)); res.StatusCode != 200 {
		t.Fatalf("PUBLISH: %d", res.StatusCode)
	}

	s.pump()

	// Bob deleted; alice no longer a member of police.
	cfg := configuration()
	cfg.Users = cfg.Users[:1]
	cfg.Groups[0].List.Entries = cfg.Groups[0].List.Entries[:1]
	cfg.Groups[1].List.Entries = nil

	snap, err := settings.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	h.src.p.Store(snap)
	h.s.Revoke()
	h.n.Quiesce()
	s.pump()

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, bobIM)}, client2); ok || h.affiliated(bob, client2, fire) {
		t.Error("bob, deleted, still served")
	}

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok || !h.affiliated(alice, client1, fire) {
		t.Error("alice no longer served")
	}

	if h.affiliated(alice, client1, police) {
		t.Error("alice still affiliated to police")
	}

	if got := st.groups(settings.Key(uri(t, alice)), client1); !slices.Equal(got, []string{fire}) {
		t.Errorf("alice's affiliations kept %v", got)
	}

	if regs, _ := st.records(); len(regs) != 1 {
		t.Errorf("%d registrations kept", len(regs))
	}

	if !slices.Contains(s.unsubscribed, bobIM) {
		t.Errorf("unsubscribed %v", s.unsubscribed)
	}
}

// A refresh between the restore and the subscription made again is the IMS's own word: the binding refreshed is not
// reconciled by the first document, which may predate it.
func TestRefreshBeforeResubscribe(t *testing.T) {
	h, st := restoredAlice(t)
	s := h.scscf

	old := h.s
	h.n.Await(func() { _ = old.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1)
	s.pump()

	h.addr = h.n.Addr()
	h.start(st)

	regs, affs := st.records()
	h.s.Restore(regs, affs)

	ue := h.client(aliceIM)
	ue.instance = "urn:uuid:00000000-0000-1000-8000-000000000001"

	if res := s.send(s.register(aliceIM, "t1", 600, ue.register("sip:ims.example.com", 600))); res.StatusCode != 200 {
		t.Fatalf("refresh: %d", res.StatusCode)
	}

	// A first document of no contact.
	h.s.Resubscribe()
	h.n.Advance(time.Second)
	s.pump()

	if _, ok := h.s.Registrar().Resolve(nil, []sip.URI{uri(t, aliceIM)}, client1); !ok {
		t.Error("the binding refreshed removed by the first document")
	}
}

// A write to the store that fails is made with the next change.
func TestSaveRetried(t *testing.T) {
	st := newMemState()
	h := newHarnessOf(t, registrar.ThroughIMS, st)
	s := h.scscf

	st.fail = true

	ue1, ue2 := h.client(aliceIM), h.client(bobIM)
	if res := s.send(s.register(aliceIM, "t1", 600, ue1.register("sip:ims.example.com", 600, info(token(alice), client1)))); res.StatusCode != 200 {
		t.Fatalf("alice: %d", res.StatusCode)
	}

	st.fail = false

	if res := s.send(s.register(bobIM, "t2", 600, ue2.register("sip:ims.example.com", 600, info(token(bob), client2)))); res.StatusCode != 200 {
		t.Fatalf("bob: %d", res.StatusCode)
	}

	if regs, _ := st.records(); len(regs) != 2 {
		t.Errorf("%d registrations saved", len(regs))
	}
}

// A shutdown before the subscriptions of Resubscribe are made makes none.
func TestShutdownBeforeResubscribe(t *testing.T) {
	h, st := restoredAlice(t)
	s := h.scscf

	old := h.s
	h.n.Await(func() { _ = old.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1)
	s.pump()

	h.addr = h.n.Addr()
	h.start(st)

	regs, affs := st.records()
	h.s.Restore(regs, affs)
	h.s.Resubscribe()

	s.subscribes = 0
	srv := h.s
	h.n.Await(func() { _ = srv.Shutdown(context.Background()) }, 2*64*transaction.DefaultT1)
	h.n.Advance(time.Minute)
	s.pump()

	if s.subscribes != 0 {
		t.Errorf("%d SUBSCRIBEs after the shutdown", s.subscribes)
	}

	if regs, _ := st.records(); len(regs) != 1 {
		t.Errorf("%d registrations kept after the shutdown", len(regs))
	}
}
