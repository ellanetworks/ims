package affiliation

import (
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
)

// configuration is user a, a member of three groups, affiliated to two on registration, of N2 2.
func configuration() settings.Settings {
	group := func(uri string) settings.Group {
		return settings.Group{
			URI: uri, InviteMembers: true, TNG3: 10 * time.Minute,
			List: settings.List{Entries: []settings.Entry{{URI: "sip:a@example.com"}}},
		}
	}

	return settings.Settings{
		System: settings.System{ParticipatingPSI: "sip:pf@example.com", ControllingPSI: "sip:cf@example.com"},
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain:    "example.com",
			OnNetwork: settings.OnNetwork{FCTimersCounters: settings.FCTimersCounters{T7: new(2 * time.Second)}},
		},
		Users: []settings.User{{
			MCPTTUserID: "sip:a@example.com", IMPUs: []string{"sip:a@ims.example.com"},
			ImplicitAffiliations: []string{"sip:g1@example.com", "sip:g2@example.com"}, MaxAffiliationsN2: new(2),
		}},
		Groups: []settings.Group{group("sip:g1@example.com"), group("sip:g2@example.com"), group("sip:g3@example.com")},
	}
}

type fixed struct{ s *settings.Snapshot }

func (f fixed) Current() *settings.Snapshot { return f.s }

// pending is an owner whose outcome is still to come: it holds no client affiliated yet, or refuses.
type pending struct {
	refuse    bool
	published []string
}

func (o *pending) Publish(_ time.Time, group, _ sip.URI, clients []string) ([]string, error) {
	o.published = append(o.published, group.User)

	if o.refuse {
		return nil, errors.New("403")
	}

	return nil, nil
}

func setup(t *testing.T) (*settings.Snapshot, *settings.User) {
	t.Helper()

	s, err := settings.New(configuration())
	if err != nil {
		t.Fatal(err)
	}

	return s, s.User(u(t, "sip:a@example.com"))
}

func u(t *testing.T, s string) sip.URI {
	t.Helper()

	v, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func statuses(p *Participating, now time.Time, user *settings.User) map[string]Status {
	out := map[string]Status{}
	for _, g := range p.Groups(now, user.ID(), "c1") {
		out[g.ID.User] = g.Status
	}

	return out
}

// TS 24.379 §9.2.2.2.3 step 14, §9.2.2.2.6, §9.2.2.2.7: the groups of a publication are affiliating until the owner
// holds the client; a group left is de-affiliating, for twice timer F; a refusal removes the entries.
func TestPublishStatuses(t *testing.T) {
	_, a := setup(t)
	owner := &pending{}
	p := NewParticipating(owner, nil)
	now := time.Unix(1000, 0)

	p.Publish(now, Publication{User: a, Client: "c1", Groups: []sip.URI{u(t, "sip:g1@example.com"), u(t, "sip:g2@example.com")}, Expires: MaxExpires}, "e1")

	if got := statuses(p, now, a); got["g1"] != Affiliating || got["g2"] != Affiliating || len(owner.published) != 2 {
		t.Fatalf("statuses %v, published %v", got, owner.published)
	}

	if !p.Affiliated(now, a.ID(), "c1", u(t, "sip:g1@example.com")) {
		t.Error("affiliating is affiliated (§9.2.2.2.11 step 3 a)")
	}

	p.Publish(now, Publication{User: a, Client: "c1", Groups: []sip.URI{u(t, "sip:g2@example.com")}, Expires: MaxExpires}, "e2")

	if got := statuses(p, now, a); got["g1"] != Deaffiliating || got["g2"] != Affiliating {
		t.Fatalf("statuses %v", got)
	}

	if got := statuses(p, now.Add(deaffiliating), a); len(got) != 1 {
		t.Errorf("after twice timer F %v", got)
	}

	if c, ok := p.ETag(a.ID(), "e2"); !ok || c != "c1" {
		t.Errorf("entity-tag of c1: %q %v", c, ok)
	}

	if _, ok := p.ETag(a.ID(), "e1"); ok {
		t.Error("entity-tag replaced still known")
	}

	owner.refuse = true

	p.Publish(now, Publication{User: a, Client: "c1", Groups: []sip.URI{u(t, "sip:g2@example.com"), u(t, "sip:g3@example.com")}, Expires: MaxExpires}, "e3")

	if got := statuses(p, now, a); got["g3"] != "" || got["g2"] != Affiliating {
		t.Errorf("refused group kept: %v", got)
	}
}

// TS 24.379 §9.2.2.2.15, §9.2.2.3.3, §6.3.6: implicit affiliation to the configured groups, within N2, held by the
// controlling function; a group outside N2 refused; a log off removes all.
func TestImplicitConfigured(t *testing.T) {
	snap, a := setup(t)
	c := NewControlling(fixed{snap})
	p := NewParticipating(c, nil)
	now := time.Unix(1000, 0)

	p.ImplicitConfigured(now, a, "c1")

	if got := statuses(p, now, a); got["g1"] != Affiliated || got["g2"] != Affiliated {
		t.Fatalf("statuses %v", got)
	}

	if !c.Affiliated(now, u(t, "sip:g1@example.com"), a.ID(), "c1") || c.Affiliated(now, u(t, "sip:g1@example.com"), a.ID(), "c2") {
		t.Fatal("affiliation check")
	}

	// §9.2.2.2.12 step 10: N2 of 2 reached.
	if _, err := p.ImplicitAffiliate(now, a, "c1", u(t, "sip:g3@example.com")); !errors.Is(err, ErrTooManyAffiliations) {
		t.Errorf("implicit affiliation beyond N2: %v", err)
	}

	p.LogOff(now, a.ID(), "c1")

	if len(p.Groups(now, a.ID(), "c1")) != 0 || len(c.Members(now, u(t, "sip:g1@example.com"))) != 0 {
		t.Error("affiliated after the log off")
	}
}

// TS 24.379 §9.2.2.2.12 to §9.2.2.2.14, §9.2.2.3.6 to §9.2.2.3.8, §9.2.2.3.11: the implicit affiliation of a request,
// completed or cancelled.
func TestImplicitRequest(t *testing.T) {
	snap, a := setup(t)
	c := NewControlling(fixed{snap})
	p := NewParticipating(c, nil)
	now := time.Unix(1000, 0)
	g3 := u(t, "sip:g3@example.com")

	if added, err := p.ImplicitAffiliate(now, a, "c1", g3); !added || err != nil {
		t.Fatalf("implicit affiliation: %v %v", added, err)
	}

	if !c.Eligible(g3, a.ID()) || c.Eligible(g3, u(t, "sip:x@example.com")) || c.Eligible(u(t, "sip:g9@example.com"), a.ID()) {
		t.Error("eligibility")
	}

	if !c.ImplicitAffiliate(now, g3, a.ID(), "c1") {
		t.Fatal("controlling implicit affiliation")
	}

	p.CompleteImplicit(now, a.ID(), "c1", g3)

	if got := statuses(p, now, a); got["g3"] != Affiliated || !c.Affiliated(now, g3, a.ID(), "c1") {
		t.Fatalf("completed: %v", got)
	}

	g1 := u(t, "sip:g1@example.com")

	if added, _ := p.ImplicitAffiliate(now, a, "c1", g1); !added {
		t.Fatal("second implicit affiliation")
	}

	c.ImplicitAffiliate(now, g1, a.ID(), "c1")
	p.CancelImplicit(now, a.ID(), "c1", g1)
	c.CancelImplicit(g1, a.ID(), "c1")

	if p.Affiliated(now, a.ID(), "c1", g1) || c.Affiliated(now, g1, a.ID(), "c1") {
		t.Error("cancelled implicit affiliation kept")
	}
}

// TS 24.379 §6.3.6 step 4: an affiliation whose expiration time passed is none.
func TestControllingExpiry(t *testing.T) {
	snap, a := setup(t)
	c := NewControlling(fixed{snap})
	now := time.Unix(1000, 0)
	g := u(t, "sip:g1@example.com")

	if _, err := c.Publish(now, g, a.ID(), []string{"c1"}); err != nil {
		t.Fatal(err)
	}

	if !c.Affiliated(now, g, a.ID(), "c1") || c.Affiliated(now.Add(MaxExpires), g, a.ID(), "c1") {
		t.Error("expiration")
	}

	if _, err := c.Publish(now, g, u(t, "sip:x@example.com"), []string{"c1"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("non-member: %v", err)
	}
}

// TS 24.379 §9.2.2.2.6 step 10, §9.2.2.3.3 step 11: the clients published are all those affiliating or affiliated,
// those of the implicit affiliation of a request included, which the owner, replacing the user's clients, keeps.
func TestPublishImplicitPending(t *testing.T) {
	snap, a := setup(t)
	c := NewControlling(fixed{snap})
	p := NewParticipating(c, nil)
	now := time.Unix(1000, 0)
	g3 := u(t, "sip:g3@example.com")

	if added, err := p.ImplicitAffiliate(now, a, "c2", g3); !added || err != nil {
		t.Fatalf("implicit affiliation: %v %v", added, err)
	}

	c.ImplicitAffiliate(now, g3, a.ID(), "c2")

	p.Publish(now, Publication{User: a, Client: "c1", Groups: []sip.URI{g3}, Expires: MaxExpires}, "e1")

	if !c.Affiliated(now, g3, a.ID(), "c1") || !c.Affiliated(now, g3, a.ID(), "c2") {
		t.Fatal("the pending implicit affiliation dropped by the owner")
	}

	// Its outcome is its request's (§9.2.2.2.13).
	if p.Groups(now, a.ID(), "c2")[0].Status != Affiliating {
		t.Error("implicit affiliation completed by the publication")
	}

	p.CompleteImplicit(now, a.ID(), "c2", g3)

	if !affiliated(p, now, a, "c2", g3) {
		t.Error("not affiliated once completed")
	}
}

func affiliated(p *Participating, now time.Time, a *settings.User, client string, g sip.URI) bool {
	return p.Affiliated(now, a.ID(), client, g)
}

// interleaving is an owner whose first publication lets another change of the user run before it is applied, as
// another strand would: Publish runs outside the participating function's lock.
type interleaving struct {
	*Controlling
	during func()
	calls  [][]string
}

func (o *interleaving) Publish(now time.Time, group, user sip.URI, clients []string) ([]string, error) {
	o.calls = append(o.calls, clients)

	if f := o.during; f != nil {
		o.during = nil

		f()
	}

	return o.Controlling.Publish(now, group, user, clients)
}

// TS 24.379 §9.2.2.2.6, §9.2.2.2.7: a publication made stale by a change of the user that followed it, whose own
// publication reached the owner first, is sent again: the owner ends with the clients of the last change.
func TestPublishStale(t *testing.T) {
	snap, a := setup(t)
	o := &interleaving{Controlling: NewControlling(fixed{snap})}
	p := NewParticipating(o, nil)
	now := time.Unix(1000, 0)
	g3 := u(t, "sip:g3@example.com")

	p.Publish(now, Publication{User: a, Client: "c1", Groups: []sip.URI{g3}, Expires: MaxExpires}, "e1")

	o.during = func() { p.LogOff(now, a.ID(), "c1") }

	p.Publish(now, Publication{User: a, Client: "c2", Groups: []sip.URI{g3}, Expires: MaxExpires}, "e2")

	// c1 and c2, then c2 of the log-off, then c2 again, of the stale publication.
	if len(o.calls) != 4 || len(o.calls[3]) != 1 || o.calls[3][0] != "c2" {
		t.Fatalf("publications %v", o.calls)
	}

	if o.Affiliated(now, g3, a.ID(), "c1") || !o.Affiliated(now, g3, a.ID(), "c2") || !affiliated(p, now, a, "c2", g3) {
		t.Error("the owner holds the stale clients")
	}

	if p.Groups(now, a.ID(), "c2")[0].Status != Affiliated {
		t.Error("outcome not applied")
	}
}

// RFC 3903 §6 step 5: a publication of an expiration of zero stores no entity-tag.
func TestPublishExpiresZero(t *testing.T) {
	_, a := setup(t)
	p := NewParticipating(&pending{}, nil)
	now := time.Unix(1000, 0)

	p.Publish(now, Publication{User: a, Client: "c1", Groups: []sip.URI{u(t, "sip:g1@example.com")}, Expires: MaxExpires}, "e1")
	p.Publish(now, Publication{User: a, Client: "c1"}, "e2")

	if _, ok := p.ETag(a.ID(), "e2"); ok {
		t.Error("entity-tag of Expires 0 stored")
	}

	if _, ok := p.ETag(a.ID(), "e1"); ok {
		t.Error("entity-tag removed still known")
	}
}

// Without a store, nothing is kept of the changes to write.
func TestNoStoreNoDirty(t *testing.T) {
	snap, user := setup(t)
	p := NewParticipating(NewControlling(fixed{snap}), nil)
	now := time.Unix(1e9, 0)

	p.Publish(now, Publication{User: user, Client: "c1", Groups: []sip.URI{u(t, "sip:g1@example.com")}, Expires: MaxExpires}, "e1")
	p.LogOff(now, user.ID(), "c1")

	if len(p.dirty) != 0 {
		t.Errorf("%d clients to write", len(p.dirty))
	}
}
