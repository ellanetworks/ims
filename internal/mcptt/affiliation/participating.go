// Package affiliation is the affiliation state of the MCPTT server (TS 24.379 §9.2.2), in the two views the
// specification has: the participating function's, by served user, client and group (§9.2.2.2.2), and the
// controlling function's, by group, user and client (§9.2.2.3.2). They are two types: co-located here [PM 11], the
// participating function's PUBLISH towards the server owning a group (§9.2.2.2.6), and the NOTIFY of its outcome
// (§9.2.2.2.7), are a call of an Owner, which the Controlling is and a SIP binding can be in its place.
//
// Time is the caller's, of the agent's clock: each operation takes it. The state is safe for concurrent use, each
// change atomic; the publications towards the owner a change makes follow it, outside the lock, and one whose
// outcome another change made stale is published again, so that the owner ends with the clients of the last. S1
// notifies no subscriber: the notifications of §9.2.2.2.5 and §9.2.2.3.5, and the refreshes of
// the publications towards a server owning a group before they expire, come with S2.
package affiliation

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/pidfmc"
	"github.com/ellanetworks/ims/sip/transaction"
)

// Status is an affiliation status (TS 24.379 §9.2.2.2.2).
type Status string

const (
	Affiliating          = Status(pidfmc.Affiliating)
	Affiliated           = Status(pidfmc.Affiliated)
	Deaffiliating        = Status(pidfmc.Deaffiliating)
	Deaffiliated  Status = "deaffiliated"
)

// MaxExpires is the expiration interval of an affiliation the clients and the participating function ask for, 2^32-1
// seconds (TS 24.379 §9.2.1.2 5a, §9.2.2.2.6 step 4), and that the servers require (§9.2.2.2.3 step 5, §9.2.2.3.3
// step 3).
const MaxExpires = 4294967295 * time.Second

// deaffiliating is how long a de-affiliating entry waits for the outcome: twice timer F (TS 24.379 §9.2.2.2.3 step 14
// a ii B, step 15), of the default T1 (RFC 3261 Table 4).
const deaffiliating = 2 * 64 * transaction.DefaultT1

// ErrTooManyAffiliations refuses an implicit affiliation beyond N2 (TS 24.379 §9.2.2.2.12 step 10), which a request
// gets a 486 for, with the warning text identity.WarnTooManyAffiliations.
var ErrTooManyAffiliations = errors.New("affiliation: N2 affiliations reached")

// Owner is the server owning the groups (TS 24.379 §9.2.2.3). Publish is the participating function's affiliation
// of user to group (§9.2.2.2.6): the clients the user is affiliated at, none to de-affiliate it. It returns what the
// owner's notification would carry (§9.2.2.2.7): the clients it holds the user affiliated at; an error is a refusal,
// as a final response other than 2xx, or timer F.
//
// Publish runs on the strand of the change of the affiliations, outside the state's lock, once the request of the
// change, if any, is answered, and must not block: the strand is an IMPU's, whose next requests wait for it.
// The in-process Controlling returns at once; a SIP binding [PM 11] waits for no response on the strand, and applies
// the outcome when it comes, which needs an asynchronous Owner.
type Owner interface {
	Publish(now time.Time, group, user sip.URI, clients []string) ([]string, error)
}

// Participating is the affiliation state of the participating function: its MCPTT user information entries, each of
// MCPTT client information entries, each of MCPTT group information entries (TS 24.379 §9.2.2.2.2). An entry whose
// expiration time passed is as one absent, and goes at the next change of its user.
type Participating struct {
	owner Owner
	log   *slog.Logger
	store Store

	// saving serializes the writes to the store, each of the state after the one before it.
	saving sync.Mutex

	mu sync.Mutex
	// dirty are the clients changed since the last write to the store, with their user.
	dirty map[clientRef]sip.URI
	users map[string]*user
	// etags are the clients of the entity-tags of their last publications (RFC 3903 §6).
	etags map[string]clientRef
	// seq numbers the publications towards the owner, and last is the number of the last of each user and group:
	// an outcome is of the current affiliations if no publication followed its own.
	seq  uint64
	last map[groupRef]uint64
}

type groupRef struct{ user, group string }

// job is a publication towards the owner (TS 24.379 §9.2.2.2.6): of the user's clients affiliating or affiliated to
// the group, numbered seq.
type job struct {
	ref     groupRef
	user    sip.URI
	group   sip.URI
	clients []string
	seq     uint64
}

type clientRef struct{ user, client string }

type user struct {
	id      sip.URI
	clients map[string]*client
}

type client struct {
	id     string
	groups []*entry
	etag   string
}

// entry is an MCPTT group information entry. The affiliating p-id and the next publishing time are of the
// notifications and refreshes of S2.
type entry struct {
	group   sip.URI
	key     string
	status  Status
	expires time.Time
	pid     string
	// implicit marks the entry an implicit affiliation by a request added (§9.2.2.2.12), until its outcome.
	implicit bool
}

// Group is the affiliation status of a client to a group.
type Group struct {
	ID      sip.URI
	Status  Status
	Expires time.Time
}

// NewParticipating is the participating function's state, publishing to owner, logging to log (slog.Default() if
// nil).
func NewParticipating(owner Owner, log *slog.Logger) *Participating {
	if log == nil {
		log = slog.Default()
	}

	return &Participating{
		owner: owner, log: log, users: map[string]*user{}, etags: map[string]clientRef{}, last: map[groupRef]uint64{},
		dirty: map[clientRef]sip.URI{},
	}
}

// Store keeps the affiliations of the clients across restarts. SaveAffiliations replaces the affiliations of the
// clients of records, and removes those of a record of no groups and no entity-tag.
type Store interface {
	SaveAffiliations(ctx context.Context, records []Record) error
}

// Record is what is kept of the affiliations of a client of a user: the groups it is affiliating or affiliated to, in
// order, of the explicit affiliations and the configured implicit ones; and the entity-tag of its last publication.
// The de-affiliating entries, and the implicit affiliations of a request (§9.2.2.2.12), last no longer than a restart.
type Record struct {
	MCPTTID  sip.URI
	ClientID string
	ETag     string
	Groups   []Group
	// PIDs are the affiliating p-ids of Groups, by index.
	PIDs []string
}

// SetStore makes the state kept in store, from its next change; before the state is used.
func (p *Participating) SetStore(store Store) { p.store = store }

// Restore puts the affiliations of records not expired, of the clients bound reports bound, and publishes them to
// the owner, as the changes that made them did: its outcome makes them affiliated, or removes those it refuses, of a
// group no longer configured or of a user no longer a member of it. The records of the others are removed. Before the
// state is used.
func (p *Participating) Restore(now time.Time, records []Record, bound func(id sip.URI, client string) bool) {
	var jobs []job

	p.mu.Lock()

	for _, r := range records {
		uk := settings.Key(r.MCPTTID)
		ref := clientRef{user: uk, client: r.ClientID}

		if uk == "" || !bound(r.MCPTTID, r.ClientID) {
			if p.store != nil {
				p.dirty[ref] = r.MCPTTID
			}

			continue
		}

		u := p.user(r.MCPTTID)
		c := u.client(r.ClientID)

		for i, g := range r.Groups {
			k := settings.Key(g.ID)
			if k == "" || !now.Before(g.Expires) || find(c.groups, k, now) != nil ||
				g.Status != Affiliating && g.Status != Affiliated {
				continue
			}

			e := &entry{group: g.ID, key: k, status: g.Status, expires: g.Expires}
			if i < len(r.PIDs) {
				e.pid = r.PIDs[i]
			}

			c.groups = append(c.groups, e)
		}

		p.setETag(u, c, r.ETag)
		p.touch(u, r.ClientID)
	}

	for _, u := range p.users {
		groups := map[string]sip.URI{}

		for _, c := range u.clients {
			for _, e := range c.groups {
				groups[e.key] = e.group
			}
		}

		for _, g := range groups {
			jobs = append(jobs, p.job(now, u, g))
		}
	}

	p.mu.Unlock()

	p.flush(now, jobs)
	p.persist(now)
}

// Revoke removes the entries of the groups eligible no longer reports eligible for their users (TS 24.379 §9.2.2.3.8),
// of a change of the settings: a group deleted, or a member removed from it. The server owning the group removed them
// already (Controlling.Revoke).
func (p *Participating) Revoke(now time.Time, eligible func(group, user sip.URI) bool) {
	p.mu.Lock()

	for _, u := range p.users {
		for _, c := range u.clients {
			n := len(c.groups)

			c.groups = slices.DeleteFunc(c.groups, func(e *entry) bool { return !eligible(e.group, u.id) })
			if len(c.groups) != n {
				p.touch(u, c.id)
			}
		}
	}

	p.mu.Unlock()

	p.persist(now)
}

// touch marks client of u changed, for the store, if any. Called with p.mu held.
func (p *Participating) touch(u *user, client string) {
	if p.store != nil {
		p.dirty[clientRef{user: settings.Key(u.id), client: client}] = u.id
	}
}

// persist writes the clients changed to the store, if any: their affiliations of a Record, outside the lock. A write
// that fails is logged, and its clients written with the next change.
func (p *Participating) persist(now time.Time) {
	p.mu.Lock()
	clean := len(p.dirty) == 0
	p.mu.Unlock()

	if p.store == nil || clean {
		return
	}

	p.saving.Lock()
	defer p.saving.Unlock()

	p.mu.Lock()

	records := make([]Record, 0, len(p.dirty))

	for ref, id := range p.dirty {
		r := Record{MCPTTID: id, ClientID: ref.client}

		if u := p.users[ref.user]; u != nil && u.clients[ref.client] != nil {
			c := u.clients[ref.client]
			r.ETag = c.etag

			for _, e := range c.groups {
				if now.Before(e.expires) && !e.implicit && (e.status == Affiliating || e.status == Affiliated) {
					r.Groups = append(r.Groups, Group{ID: e.group, Status: e.status, Expires: e.expires})
					r.PIDs = append(r.PIDs, e.pid)
				}
			}
		}

		records = append(records, r)
	}

	clear(p.dirty)
	p.mu.Unlock()

	if len(records) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
	defer cancel()

	if err := p.store.SaveAffiliations(ctx, records); err != nil {
		p.log.Warn("affiliations not saved", slog.Int("clients", len(records)), slog.Any("error", err))

		p.mu.Lock()

		for _, r := range records {
			p.dirty[clientRef{user: settings.Key(r.MCPTTID), client: r.ClientID}] = r.MCPTTID
		}

		p.mu.Unlock()
	}
}

// saveTimeout bounds a write to the store, of the strand of an IMPU.
const saveTimeout = 5 * time.Second

// Groups are the entries of client of user not expired, in order.
func (p *Participating) Groups(now time.Time, id sip.URI, clientID string) []Group {
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []Group

	if u := p.users[settings.Key(id)]; u != nil && u.clients[clientID] != nil {
		for _, e := range u.clients[clientID].groups {
			if now.Before(e.expires) {
				out = append(out, Group{ID: e.group, Status: e.status, Expires: e.expires})
			}
		}
	}

	return out
}

// Affiliated is the affiliation status determination (TS 24.379 §9.2.2.2.11): whether the user is affiliated to
// group at client, its entry affiliating or affiliated.
func (p *Participating) Affiliated(now time.Time, id sip.URI, clientID string, group sip.URI) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	u := p.users[settings.Key(id)]
	if u == nil || u.clients[clientID] == nil {
		return false
	}

	e := u.clients[clientID].find(settings.Key(group), now)

	return e != nil && (e.status == Affiliating || e.status == Affiliated)
}

// Publication is an affiliation status change of a client (TS 24.379 §9.2.1.2): the groups of its per-user
// affiliation information, in order, its p-id, and the candidate expiration interval, zero to de-affiliate all.
type Publication struct {
	User    *settings.User
	Client  string
	Groups  []sip.URI
	PID     string
	Expires time.Duration
}

// Publish applies a publication (TS 24.379 §9.2.2.2.3 steps 11 to 17), and records etag as the entity-tag of the
// client's publication, none of an expiration of zero (RFC 3903 §6 step 5: no state is stored). N2 is enforced by
// the policy of step 14 c: the groups the user is affiliated to already stay, the new ones go, the last of the
// publication first.
func (p *Participating) Publish(now time.Time, pub Publication, etag string) {
	p.flush(now, p.apply(now, pub, etag))
	p.persist(now)
}

func (p *Participating) apply(now time.Time, pub Publication, etag string) []job {
	p.mu.Lock()
	defer p.mu.Unlock()

	u := p.user(pub.User.ID())
	c := u.client(pub.Client)
	served := c.groups

	var cand []*entry

	if pub.Expires > 0 {
		in := map[string]bool{}

		var groups []*entry

		for _, g := range pub.Groups {
			if k := settings.Key(g); k != "" && !in[k] {
				in[k] = true
				groups = append(groups, &entry{group: g, key: k})
			}
		}

		// Step 14 a i, ii.
		for _, e := range served {
			if !now.Before(e.expires) {
				continue
			}

			n := *e

			switch {
			case in[e.key]:
				if e.status == Deaffiliating || e.status == Deaffiliated {
					n.status, n.pid = Affiliating, pub.PID
				}

				n.expires = now.Add(pub.Expires)
			case e.status == Affiliated || e.status == Affiliating:
				n.status, n.expires = Deaffiliating, now.Add(deaffiliating)
			}

			cand = append(cand, &n)
		}

		// Step 14 a iii.
		for _, g := range groups {
			if find(cand, g.key, now) == nil {
				g.status, g.expires, g.pid = Affiliating, now.Add(pub.Expires), pub.PID
				cand = append(cand, g)
			}
		}

		// Steps 14 b, c.
		cand = u.limit(pub.User, c.id, cand, served, now)
	} else {
		// Step 15.
		for _, e := range served {
			n := *e
			n.status, n.expires = Deaffiliating, now.Add(deaffiliating)
			cand = append(cand, &n)
		}

		etag = ""
	}

	// Step 16.
	c.groups = cand
	p.setETag(u, c, etag)
	p.touch(u, c.id)

	// Step 17.
	var jobs []job

	for _, n := range cand {
		o := find(served, n.key, now)

		switch {
		case n.status == Affiliating && (o == nil || o.status == Deaffiliating || o.status == Deaffiliated),
			n.status == Deaffiliating && o != nil && o.status == Affiliated:
			jobs = append(jobs, p.job(now, u, n.group))
		}
	}

	u.prune(now)

	return jobs
}

// ETag is the client of user an entity-tag of its last publication designates (RFC 3903 §6 step 3).
func (p *Participating) ETag(id sip.URI, etag string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ref, ok := p.etags[etag]
	if !ok || ref.user != settings.Key(id) {
		return "", false
	}

	return ref.client, true
}

func (p *Participating) setETag(u *user, c *client, etag string) {
	delete(p.etags, c.etag)

	c.etag = etag
	if etag != "" {
		p.etags[etag] = clientRef{user: settings.Key(u.id), client: c.id}
	}
}

// ImplicitConfigured is the implicit affiliation to configured groups (TS 24.379 §9.2.2.2.15) of client of a user
// once service authorisation succeeded: to its <ImplicitAffiliations>, which it is not affiliated to at the client
// already, within N2 by the policy of step 9 c, the last groups of the configuration going first. The candidate
// expiration interval, which the clause leaves open, is MaxExpires, as of an explicit affiliation: the affiliations
// last until the client logs off (TS 23.280 §5.2.5).
func (p *Participating) ImplicitConfigured(now time.Time, cfg *settings.User, clientID string) {
	p.flush(now, p.implicitConfigured(now, cfg, clientID))
	p.persist(now)
}

func (p *Participating) implicitConfigured(now time.Time, cfg *settings.User, clientID string) []job {
	groups := cfg.ImplicitGroups()
	if len(groups) == 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	u := p.user(cfg.ID())
	c := u.client(clientID)

	// Step 8.
	var cand []*entry

	for _, e := range c.groups {
		n := *e
		cand = append(cand, &n)
	}

	added := false

	for _, g := range groups {
		k := settings.Key(g.ID())
		if find(cand, k, now) == nil {
			cand = slices.DeleteFunc(cand, func(e *entry) bool { return e.key == k })
			cand = append(cand, &entry{group: g.ID(), key: k, status: Affiliating, expires: now.Add(MaxExpires)})
			added = true
		}
	}

	if !added {
		return nil
	}

	// Steps 9, 10.
	served := c.groups
	c.groups = u.limit(cfg, c.id, cand, served, now)
	p.touch(u, c.id)

	// Step 11.
	var jobs []job

	for _, g := range groups {
		if e := c.find(settings.Key(g.ID()), now); e != nil && e.status == Affiliating {
			jobs = append(jobs, p.job(now, u, e.group))
		}
	}

	u.prune(now)

	return jobs
}

// ImplicitAffiliate is the affiliation status change by implicit affiliation (TS 24.379 §9.2.2.2.12) of client of a
// user to group, for a request that requires it: an affiliating entry, unless one is there. It reports whether it
// added one, and fails with ErrTooManyAffiliations beyond N2 (step 10). CompleteImplicit and CancelImplicit end it.
func (p *Participating) ImplicitAffiliate(now time.Time, cfg *settings.User, clientID string, group sip.URI) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	u := p.user(cfg.ID())
	c := u.client(clientID)
	k := settings.Key(group)

	// Step 7.
	if c.find(k, now) != nil {
		return false, nil
	}

	cand := slices.DeleteFunc(slices.Clone(c.groups), func(e *entry) bool { return e.key == k })
	cand = append(cand, &entry{group: group, key: k, status: Affiliating, expires: now.Add(MaxExpires), implicit: true})

	// Steps 8 to 10.
	if limited := u.limit(cfg, c.id, cand, c.groups, now); find(limited, k, now) == nil {
		return false, ErrTooManyAffiliations
	}

	// Step 11.
	c.groups = cand

	return true, nil
}

// CompleteImplicit is the implicit affiliation status change completion (TS 24.379 §9.2.2.2.13): the entry
// ImplicitAffiliate added affiliated, on the 2xx of the controlling function.
func (p *Participating) CompleteImplicit(now time.Time, id sip.URI, clientID string, group sip.URI) {
	p.mu.Lock()

	if e := p.implicit(id, clientID, group, now); e != nil {
		e.status, e.implicit = Affiliated, false

		p.touch(p.users[settings.Key(id)], clientID)
	}

	p.mu.Unlock()

	p.persist(now)
}

// CancelImplicit is the implicit affiliation status change cancellation (TS 24.379 §9.2.2.2.14): the entry
// ImplicitAffiliate added removed, of a request rejected, or of a 4xx to 6xx of the controlling function.
func (p *Participating) CancelImplicit(now time.Time, id sip.URI, clientID string, group sip.URI) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if e := p.implicit(id, clientID, group, now); e != nil {
		c := p.users[settings.Key(id)].clients[clientID]
		c.groups = slices.DeleteFunc(c.groups, func(x *entry) bool { return x == e })
	}
}

func (p *Participating) implicit(id sip.URI, clientID string, group sip.URI, now time.Time) *entry {
	u := p.users[settings.Key(id)]
	if u == nil || u.clients[clientID] == nil {
		return nil
	}

	if e := u.clients[clientID].find(settings.Key(group), now); e != nil && e.implicit {
		return e
	}

	return nil
}

// LogOff removes the client of a user, its service authorisation gone: all its affiliations are revoked (TS 23.280
// §5.2.5; TS 24.379 §7.3.5 step 3 NOTE), and the server owning each group it was affiliated to is told
// (§9.2.2.2.6).
func (p *Participating) LogOff(now time.Time, id sip.URI, clientID string) {
	p.flush(now, p.logOff(now, id, clientID))
	p.persist(now)
}

func (p *Participating) logOff(now time.Time, id sip.URI, clientID string) []job {
	p.mu.Lock()
	defer p.mu.Unlock()

	uk := settings.Key(id)

	u := p.users[uk]
	if u == nil || u.clients[clientID] == nil {
		return nil
	}

	c := u.clients[clientID]
	delete(u.clients, clientID)
	delete(p.etags, c.etag)
	p.touch(u, clientID)

	var jobs []job

	for _, e := range c.groups {
		if now.Before(e.expires) && (e.status == Affiliating || e.status == Affiliated) {
			jobs = append(jobs, p.job(now, u, e.group))
		}
	}

	u.prune(now)

	if len(u.clients) == 0 {
		delete(p.users, uk)
	}

	return jobs
}

// job is the affiliation of the user to group towards its owner (TS 24.379 §9.2.2.2.6), of step 10: every client of
// the user affiliating or affiliated to the group, those of the implicit affiliations of requests (§9.2.2.2.12)
// included, since the owner replaces the clients of the user with those it receives (§9.2.2.3.3 step 11). Called with
// p.mu held.
func (p *Participating) job(now time.Time, u *user, group sip.URI) job {
	k := settings.Key(group)

	var clients []string

	for _, c := range u.clients {
		if e := c.find(k, now); e != nil && (e.status == Affiliating || e.status == Affiliated) {
			clients = append(clients, c.id)
		}
	}

	slices.Sort(clients)

	p.seq++
	ref := groupRef{user: settings.Key(u.id), group: k}
	p.last[ref] = p.seq

	return job{ref: ref, user: u.id, group: group, clients: clients, seq: p.seq}
}

// flush sends the publications of a change to the owner, outside the lock, and applies their outcomes; one made
// stale by a publication of the user and group that followed it, which may have reached the owner first, is sent
// again with the current clients.
func (p *Participating) flush(now time.Time, jobs []job) {
	for len(jobs) > 0 {
		j := jobs[0]
		jobs = jobs[1:]

		held, err := p.owner.Publish(now, j.group, j.user, j.clients)
		if err != nil {
			p.log.Debug("affiliation refused by the server owning the group", slog.String("mcptt_id", j.user.String()),
				slog.String("group", j.group.String()), slog.Any("error", err))
		} else {
			p.log.Debug("affiliation published", slog.String("mcptt_id", j.user.String()), slog.String("group", j.group.String()),
				slog.Any("clients", held))
		}

		if next, again := p.outcome(now, j, held, err); again {
			jobs = append(jobs, next)
		}
	}
}

// outcome applies the outcome of a publication (TS 24.379 §9.2.2.2.7): of the clients the owner holds, the
// affiliating entries affiliated; the affiliated and de-affiliating entries of the others de-affiliated. A refusal
// removes the entries of the group (§9.2.2.2.6, last paragraph). The entries of the implicit affiliations of requests
// are left to their requests (§9.2.2.2.13, §9.2.2.2.14). Of a stale publication, it returns the one to send again.
func (p *Participating) outcome(now time.Time, j job, held []string, err error) (job, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	u := p.users[j.ref.user]

	switch {
	case p.last[j.ref] != j.seq && u != nil:
		return p.job(now, u, j.group), true
	case p.last[j.ref] != j.seq:
		// The user's clients are all gone since: their last publication, of none, follows.
		u = &user{id: j.user}
		return p.job(now, u, j.group), true
	case u == nil:
		delete(p.last, j.ref)
		return job{}, false
	}

	for _, c := range u.clients {
		e := c.find(j.ref.group, now)
		if e != nil && !e.implicit {
			p.touch(u, c.id)
		}

		switch {
		case e == nil || e.implicit:
		case err != nil:
			c.groups = slices.DeleteFunc(c.groups, func(x *entry) bool { return x.key == j.ref.group })
		case slices.Contains(held, c.id):
			if e.status == Affiliating {
				e.status = Affiliated
			}
		case e.status == Affiliated || e.status == Deaffiliating:
			e.status, e.expires = Deaffiliated, now
		}
	}

	if len(j.clients) == 0 {
		delete(p.last, j.ref)
	}

	return job{}, false
}

func (p *Participating) user(id sip.URI) *user {
	k := settings.Key(id)

	u := p.users[k]
	if u == nil {
		u = &user{id: id, clients: map[string]*client{}}
		p.users[k] = u
	}

	return u
}

func (u *user) client(id string) *client {
	c := u.clients[id]
	if c == nil {
		c = &client{id: id}
		u.clients[id] = c
	}

	return c
}

// limit reduces the candidate list of a client to N2 (TS 24.379 §9.2.2.2.3 step 14 b, c; §9.2.2.2.12 steps 8, 9;
// §9.2.2.2.15 step 9): the groups the user is affiliated to already, at the client or at its others, stay; of those
// the candidates add, the last go first. A change that lowered N2 limits the affiliations to come.
func (u *user) limit(cfg *settings.User, clientID string, cand, served []*entry, now time.Time) []*entry {
	n2, limited := cfg.N2()
	if !limited {
		return cand
	}

	active := func(e *entry) bool {
		return e != nil && now.Before(e.expires) && (e.status == Affiliating || e.status == Affiliated)
	}

	counted := map[string]bool{}

	for id, c := range u.clients {
		for _, e := range c.groups {
			if id != clientID && active(e) {
				counted[e.key] = true
			}
		}
	}

	var added []int

	for i, e := range cand {
		if !active(e) || counted[e.key] {
			continue
		}

		counted[e.key] = true

		if !active(find(served, e.key, now)) {
			added = append(added, i)
		}
	}

	out := slices.Clone(cand)

	for i := len(added) - 1; i >= 0 && len(counted) > n2; i-- {
		delete(counted, cand[added[i]].key)
		out = slices.Delete(out, added[i], added[i]+1)
	}

	return out
}

// prune removes the entries of u whose expiration time passed.
func (u *user) prune(now time.Time) {
	for _, c := range u.clients {
		c.groups = slices.DeleteFunc(c.groups, func(e *entry) bool { return !now.Before(e.expires) })
	}
}

func (c *client) find(k string, now time.Time) *entry { return find(c.groups, k, now) }

// find is the entry of group key k of es not expired, nil if none.
func find(es []*entry, k string, now time.Time) *entry {
	for _, e := range es {
		if e.key == k && now.Before(e.expires) {
			return e
		}
	}

	return nil
}
