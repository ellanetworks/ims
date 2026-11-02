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
	"github.com/ellanetworks/ims/sip/lifetime"
	"github.com/ellanetworks/ims/sip/pidfmc"
	"github.com/ellanetworks/ims/sip/transaction"
)

// TS 24.379 clause 9.2.2.2.2
type Status string

const (
	Affiliating          = Status(pidfmc.Affiliating)
	Affiliated           = Status(pidfmc.Affiliated)
	Deaffiliating        = Status(pidfmc.Deaffiliating)
	Deaffiliated  Status = "deaffiliated"
)

// TS 24.379 clause 9.2.1.2
// TS 24.379 clause 9.2.2.2.6
// TS 24.379 clause 9.2.2.2.3
// TS 24.379 clause 9.2.2.3.3
const MaxExpires = 4294967295 * time.Second

// TS 24.379 clause 9.2.2.2.3
const deaffiliating = 2 * 64 * transaction.DefaultT1

// TS 24.379 clause 9.2.2.2.12
var ErrTooManyAffiliations = errors.New("affiliation: N2 affiliations reached")

// TS 24.379 clause 9.2.2.3
// TS 24.379 clause 9.2.2.2.6
// TS 24.379 clause 9.2.2.2.7
type Owner interface {
	Publish(now time.Time, group, user sip.URI, clients []string) ([]string, error)
}

// TS 24.379 clause 9.2.2.2.2
type Participating struct {
	owner Owner
	log   *slog.Logger
	store Store
	life  context.Context

	saving sync.Mutex

	mu    sync.Mutex
	dirty map[clientRef]sip.URI
	users map[string]*user
	etags map[string]clientRef
	seq   uint64
	last  map[groupRef]uint64
}

type groupRef struct{ user, group string }

// TS 24.379 clause 9.2.2.2.6
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

type entry struct {
	group      sip.URI
	key        string
	status     Status
	expires    time.Time
	pid        string
	implicit   bool
	configured bool
}

type Group struct {
	ID         sip.URI
	Status     Status
	Expires    time.Time
	Configured bool
}

func NewParticipating(owner Owner, log *slog.Logger) *Participating {
	if log == nil {
		log = slog.Default()
	}

	return &Participating{
		owner: owner, log: log, life: context.Background(), users: map[string]*user{}, etags: map[string]clientRef{}, last: map[groupRef]uint64{},
		dirty: map[clientRef]sip.URI{},
	}
}

type Store interface {
	SaveAffiliations(ctx context.Context, records []Record) error
}

type Record struct {
	MCPTTID  sip.URI
	ClientID string
	ETag     string
	Groups   []Group
	PIDs     []string
}

func (p *Participating) SetStore(life context.Context, store Store) { p.store, p.life = store, life }

func (p *Participating) Restore(ctx context.Context, now time.Time, records []Record, bound func(id sip.URI, client string) bool) {
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

			e := &entry{group: g.ID, key: k, status: g.Status, expires: g.Expires, configured: g.Configured}
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

	p.flush(ctx, now, jobs)
	p.persist(ctx, now)
}

// TS 24.379 clause 9.2.2.3.8
func (p *Participating) Revoke(ctx context.Context, now time.Time, eligible func(group, user sip.URI) bool) {
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

	p.persist(ctx, now)
}

func (p *Participating) touch(u *user, client string) {
	if p.store != nil {
		p.dirty[clientRef{user: settings.Key(u.id), client: client}] = u.id
	}
}

func (p *Participating) persist(ctx context.Context, now time.Time) {
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
					r.Groups = append(r.Groups, e.view())
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

	ctx, cancel := context.WithTimeout(lifetime.With(p.life, ctx), saveTimeout)
	defer cancel()

	if err := p.store.SaveAffiliations(ctx, records); err != nil {
		p.log.WarnContext(ctx, "affiliations not saved", slog.Int("clients", len(records)), slog.Any("error", err))

		p.mu.Lock()

		for _, r := range records {
			p.dirty[clientRef{user: settings.Key(r.MCPTTID), client: r.ClientID}] = r.MCPTTID
		}

		p.mu.Unlock()
	}
}

const saveTimeout = 5 * time.Second

func (p *Participating) Groups(now time.Time, id sip.URI, clientID string) []Group {
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []Group

	if u := p.users[settings.Key(id)]; u != nil && u.clients[clientID] != nil {
		for _, e := range u.clients[clientID].groups {
			if now.Before(e.expires) {
				out = append(out, e.view())
			}
		}
	}

	return out
}

type Affiliations struct {
	User   sip.URI
	Client string
	Groups []Group
}

func (p *Participating) Snapshot(now time.Time) []Affiliations {
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []Affiliations

	for _, u := range p.users {
		for _, c := range u.clients {
			a := Affiliations{User: u.id, Client: c.id}

			for _, e := range c.groups {
				if now.Before(e.expires) {
					a.Groups = append(a.Groups, e.view())
				}
			}

			if len(a.Groups) > 0 {
				out = append(out, a)
			}
		}
	}

	return out
}

// TS 24.379 clause 9.2.2.2.11
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

// TS 24.379 clause 9.2.1.2
type Publication struct {
	User    *settings.User
	Client  string
	Groups  []sip.URI
	PID     string
	Expires time.Duration
}

// TS 24.379 clause 9.2.2.2.3
// RFC 3903 section 6
func (p *Participating) Publish(ctx context.Context, now time.Time, pub Publication, etag string) {
	p.flush(ctx, now, p.apply(now, pub, etag))
	p.persist(ctx, now)
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

				n.expires, n.configured = now.Add(pub.Expires), false
			case e.status == Affiliated || e.status == Affiliating:
				n.status, n.expires = Deaffiliating, now.Add(deaffiliating)
			}

			cand = append(cand, &n)
		}

		for _, g := range groups {
			if find(cand, g.key, now) == nil {
				g.status, g.expires, g.pid = Affiliating, now.Add(pub.Expires), pub.PID
				cand = append(cand, g)
			}
		}

		cand = u.limit(pub.User, c.id, cand, served, now)
	} else {
		for _, e := range served {
			n := *e
			n.status, n.expires = Deaffiliating, now.Add(deaffiliating)
			cand = append(cand, &n)
		}

		etag = ""
	}

	c.groups = cand
	p.setETag(u, c, etag)
	p.touch(u, c.id)

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

// RFC 3903 section 6
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

// TS 24.379 clause 9.2.2.2.15
func (p *Participating) ImplicitConfigured(ctx context.Context, now time.Time, cfg *settings.User, clientID string) {
	p.flush(ctx, now, p.implicitConfigured(now, cfg, clientID, nil))
	p.persist(ctx, now)
}

func (p *Participating) implicitConfigured(now time.Time, cfg *settings.User, clientID string, had map[string]bool) []job {
	groups := cfg.ImplicitGroups()
	if len(groups) == 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	u := p.user(cfg.ID())
	c := u.client(clientID)

	var cand []*entry

	for _, e := range c.groups {
		n := *e
		cand = append(cand, &n)
	}

	added := false

	for _, g := range groups {
		k := settings.Key(g.ID())
		if !had[k] && find(cand, k, now) == nil {
			cand = slices.DeleteFunc(cand, func(e *entry) bool { return e.key == k })
			cand = append(cand, &entry{
				group: g.ID(), key: k, status: Affiliating, expires: now.Add(MaxExpires), configured: true,
			})
			added = true
		}
	}

	if !added {
		return nil
	}

	served := c.groups
	c.groups = u.limit(cfg, c.id, cand, served, now)
	p.touch(u, c.id)

	var jobs []job

	for _, g := range groups {
		if e := c.find(settings.Key(g.ID()), now); e != nil && e.status == Affiliating {
			jobs = append(jobs, p.job(now, u, e.group))
		}
	}

	u.prune(now)

	return jobs
}

func (p *Participating) Reconfigure(ctx context.Context, now time.Time, before, cfg *settings.User, clientID string) {
	had := implicitKeys(before)

	jobs := p.unconfigure(now, had, cfg, clientID)
	p.flush(ctx, now, append(jobs, p.implicitConfigured(now, cfg, clientID, had)...))
	p.persist(ctx, now)
}

func implicitKeys(u *settings.User) map[string]bool {
	keys := map[string]bool{}
	for _, g := range u.ImplicitGroups() {
		keys[settings.Key(g.ID())] = true
	}

	return keys
}

func (p *Participating) unconfigure(now time.Time, had map[string]bool, cfg *settings.User, clientID string) []job {
	p.mu.Lock()
	defer p.mu.Unlock()

	u := p.users[settings.Key(cfg.ID())]
	if u == nil || u.clients[clientID] == nil {
		return nil
	}

	c := u.clients[clientID]
	configured := implicitKeys(cfg)

	var jobs []job

	for _, e := range c.groups {
		if !e.configured || configured[e.key] || !had[e.key] || !now.Before(e.expires) ||
			e.status != Affiliating && e.status != Affiliated {
			continue
		}

		e.status, e.expires = Deaffiliating, now.Add(deaffiliating)
		jobs = append(jobs, p.job(now, u, e.group))
	}

	if len(jobs) > 0 {
		p.touch(u, c.id)
	}

	return jobs
}

// TS 24.379 clause 9.2.2.2.12
func (p *Participating) ImplicitAffiliate(now time.Time, cfg *settings.User, clientID string, group sip.URI) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	u := p.user(cfg.ID())
	c := u.client(clientID)
	k := settings.Key(group)

	if c.find(k, now) != nil {
		return false, nil
	}

	cand := slices.DeleteFunc(slices.Clone(c.groups), func(e *entry) bool { return e.key == k })
	cand = append(cand, &entry{group: group, key: k, status: Affiliating, expires: now.Add(MaxExpires), implicit: true})

	if limited := u.limit(cfg, c.id, cand, c.groups, now); find(limited, k, now) == nil {
		return false, ErrTooManyAffiliations
	}

	c.groups = cand

	return true, nil
}

// TS 24.379 clause 9.2.2.2.13
func (p *Participating) CompleteImplicit(ctx context.Context, now time.Time, id sip.URI, clientID string, group sip.URI) {
	p.mu.Lock()

	if e := p.implicit(id, clientID, group, now); e != nil {
		e.status, e.implicit = Affiliated, false

		p.touch(p.users[settings.Key(id)], clientID)
	}

	p.mu.Unlock()

	p.persist(ctx, now)
}

// TS 24.379 clause 9.2.2.2.14
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

// TS 23.280 clause 5.2.5
// TS 24.379 clause 7.3.5
func (p *Participating) LogOff(ctx context.Context, now time.Time, id sip.URI, clientID string) {
	p.flush(ctx, now, p.logOff(now, id, clientID))
	p.persist(ctx, now)
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

// TS 24.379 clause 9.2.2.2.6
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

func (p *Participating) flush(ctx context.Context, now time.Time, jobs []job) {
	for len(jobs) > 0 {
		j := jobs[0]
		jobs = jobs[1:]

		held, err := p.owner.Publish(now, j.group, j.user, j.clients)
		if err != nil {
			p.log.DebugContext(ctx, "affiliation refused by the server owning the group", slog.String("mcptt_id", j.user.String()),
				slog.String("group", j.group.String()), slog.Any("error", err))
		} else {
			p.log.DebugContext(ctx, "affiliation published", slog.String("mcptt_id", j.user.String()), slog.String("group", j.group.String()),
				slog.Any("clients", held))
		}

		if next, again := p.outcome(now, j, held, err); again {
			jobs = append(jobs, next)
		}
	}
}

// TS 24.379 clause 9.2.2.2.7
func (p *Participating) outcome(now time.Time, j job, held []string, err error) (job, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	u := p.users[j.ref.user]

	switch {
	case p.last[j.ref] != j.seq && u != nil:
		return p.job(now, u, j.group), true
	case p.last[j.ref] != j.seq:
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

// TS 24.379 clause 9.2.2.2.3
// TS 24.379 clause 9.2.2.2.12
// TS 24.379 clause 9.2.2.2.15
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

func (u *user) prune(now time.Time) {
	for _, c := range u.clients {
		c.groups = slices.DeleteFunc(c.groups, func(e *entry) bool { return !now.Before(e.expires) })
	}
}

func (c *client) find(k string, now time.Time) *entry { return find(c.groups, k, now) }

func (e *entry) view() Group {
	return Group{ID: e.group, Status: e.status, Expires: e.expires, Configured: e.configured}
}

func find(es []*entry, k string, now time.Time) *entry {
	for _, e := range es {
		if e.key == k && now.Before(e.expires) {
			return e
		}
	}

	return nil
}
