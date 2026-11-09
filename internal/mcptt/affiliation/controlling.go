package affiliation

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
)

// TS 24.379 clause 9.2.2.3.3
var ErrForbidden = errors.New("affiliation: not a group of the server, or not a member of it")

// TS 24.379 clause 9.2.2.3.2
type Row struct {
	Group   sip.URI
	User    sip.URI
	Node    string
	Clients []string
	Expires time.Time
}

// TS 24.379 clause 9.2.2.3.2
type Shared interface {
	GroupAffiliations(ctx context.Context, group sip.URI) ([]Row, error)
	NodeAffiliations(ctx context.Context) ([]Row, error)
	PutAffiliation(ctx context.Context, r Row) error
	Self() string
}

// TS 24.379 clause 9.2.2.3.2
type Controlling struct {
	source settings.Source
	table  Shared

	writing sync.Mutex
	unsaved map[[2]string]Row
}

type Member struct {
	ID      sip.URI
	Clients []string
	Nodes   []string
}

func NewControlling(source settings.Source) *Controlling {
	return NewSharedControlling(source, &memory{groups: map[string]map[string]*Row{}})
}

func NewSharedControlling(source settings.Source, table Shared) *Controlling {
	return &Controlling{source: source, table: table, unsaved: map[[2]string]Row{}}
}

const (
	readTimeout  = 5 * time.Second
	writeTimeout = 5 * time.Second
)

func (c *Controlling) put(ctx context.Context, r Row) error {
	c.writing.Lock()
	defer c.writing.Unlock()

	return c.write(ctx, r)
}

func (c *Controlling) write(ctx context.Context, r Row) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()

	k := [2]string{settings.Key(r.Group), settings.Key(r.User)}

	if err := c.table.PutAffiliation(ctx, r); err != nil {
		c.unsaved[k] = r
		return err
	}

	delete(c.unsaved, k)

	return nil
}

func (c *Controlling) Unsaved() int {
	c.writing.Lock()
	defer c.writing.Unlock()

	return len(c.unsaved)
}

func (c *Controlling) Save(ctx context.Context) error {
	c.writing.Lock()
	defer c.writing.Unlock()

	var err error

	for _, r := range c.unsaved {
		err = errors.Join(err, c.write(ctx, r))
	}

	return err
}

func (c *Controlling) rows(group sip.URI) []Row {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	rs, err := c.table.GroupAffiliations(ctx, group)
	if err != nil {
		return nil
	}

	return rs
}

// TS 24.379 clause 9.2.2.3.3
func (c *Controlling) Publish(ctx context.Context, now time.Time, group, user sip.URI, clients []string) ([]string, error) {
	if !c.Eligible(group, user) {
		return nil, ErrForbidden
	}

	clients = slices.Clone(clients)
	slices.Sort(clients)

	r := Row{Group: group, User: user, Clients: clients}
	if len(clients) > 0 {
		r.Expires = now.Add(MaxExpires)
	}

	if had, ok := c.mine(group, user); ok == (len(clients) > 0) && slices.Equal(had.Clients, clients) {
		return slices.Clone(clients), nil
	}

	_ = c.put(ctx, r)

	return slices.Clone(clients), nil
}

func (c *Controlling) mine(group, user sip.URI) (Row, bool) {
	uk, self := settings.Key(user), c.table.Self()

	for _, r := range c.rows(group) {
		if settings.Key(r.User) == uk && r.Node == self {
			r.Clients = slices.Sorted(slices.Values(r.Clients))
			return r, true
		}
	}

	return Row{}, false
}

// TS 24.379 clause 9.2.2.3.8
// TS 24.379 clause 9.2.2.3.6
func (c *Controlling) Eligible(group, user sip.URI) bool {
	g := c.source.Current().Group(group)
	return g != nil && g.Entry(user) != nil
}

// TS 24.379 clause 9.2.2.3.8
func (c *Controlling) Revoke(ctx context.Context) {
	rs, err := c.table.NodeAffiliations(ctx)
	if err != nil {
		return
	}

	for _, r := range rs {
		if !c.Eligible(r.Group, r.User) {
			_ = c.put(ctx, Row{Group: r.Group, User: r.User})
		}
	}
}

// TS 24.379 clause 9.2.2.3.2
func (c *Controlling) Retain(ctx context.Context, now time.Time, held []Affiliations) error {
	rs, err := c.table.NodeAffiliations(ctx)
	if err != nil {
		return err
	}

	keep := map[[2]string]bool{}

	for _, a := range held {
		for _, g := range a.Groups {
			if now.Before(g.Expires) && (g.Status == Affiliating || g.Status == Affiliated) {
				keep[[2]string{settings.Key(g.ID), settings.Key(a.User)}] = true
			}
		}
	}

	for _, r := range rs {
		if !keep[[2]string{settings.Key(r.Group), settings.Key(r.User)}] {
			err = errors.Join(err, c.put(ctx, Row{Group: r.Group, User: r.User}))
		}
	}

	return err
}

// TS 24.379 clause 6.3.6
func (c *Controlling) Affiliated(now time.Time, group, user sip.URI, client string) bool {
	uk := settings.Key(user)

	for _, r := range c.rows(group) {
		if settings.Key(r.User) == uk && slices.Contains(r.Clients, client) && now.Before(r.Expires) {
			return true
		}
	}

	return false
}

func (c *Controlling) Members(now time.Time, group sip.URI) []Member {
	var out []Member

	at := map[string]int{}

	for _, r := range c.rows(group) {
		if !now.Before(r.Expires) || len(r.Clients) == 0 {
			continue
		}

		uk := settings.Key(r.User)

		i, ok := at[uk]
		if !ok {
			i = len(out)
			at[uk] = i

			out = append(out, Member{ID: r.User})
		}

		m := &out[i]

		for _, cl := range r.Clients {
			if !slices.Contains(m.Clients, cl) {
				m.Clients = append(m.Clients, cl)
			}
		}

		if !slices.Contains(m.Nodes, r.Node) {
			m.Nodes = append(m.Nodes, r.Node)
		}
	}

	return out
}

// TS 24.379 clause 9.2.2.3.7
// TS 24.379 clause 9.2.2.3.6
func (c *Controlling) ImplicitAffiliate(now time.Time, group, user sip.URI, client string) bool {
	r, ok := c.mine(group, user)
	if !ok || !now.Before(r.Expires) {
		r = Row{Group: group, User: user}
	}

	if slices.Contains(r.Clients, client) {
		return false
	}

	r.Clients = append(r.Clients, client)
	r.Expires = now.Add(MaxExpires)

	return c.put(context.Background(), r) == nil
}

// TS 24.379 clause 9.2.2.3.11
func (c *Controlling) CancelImplicit(group, user sip.URI, client string) {
	r, ok := c.mine(group, user)
	if !ok {
		return
	}

	r.Clients = slices.DeleteFunc(r.Clients, func(id string) bool { return id == client })

	_ = c.put(context.Background(), r)
}

type memory struct {
	mu     sync.Mutex
	groups map[string]map[string]*Row
}

func (m *memory) GroupAffiliations(_ context.Context, group sip.URI) ([]Row, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []Row
	for _, r := range m.groups[settings.Key(group)] {
		out = append(out, clone(*r))
	}

	return out, nil
}

func (m *memory) NodeAffiliations(context.Context) ([]Row, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []Row

	for _, users := range m.groups {
		for _, r := range users {
			out = append(out, clone(*r))
		}
	}

	return out, nil
}

func (m *memory) Self() string { return "" }

func (m *memory) PutAffiliation(_ context.Context, r Row) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	gk, uk := settings.Key(r.Group), settings.Key(r.User)

	if len(r.Clients) == 0 {
		delete(m.groups[gk], uk)

		if len(m.groups[gk]) == 0 {
			delete(m.groups, gk)
		}

		return nil
	}

	users := m.groups[gk]
	if users == nil {
		users = map[string]*Row{}
		m.groups[gk] = users
	}

	r = clone(r)
	users[uk] = &r

	return nil
}

func clone(r Row) Row {
	r.Clients = slices.Clone(r.Clients)
	return r
}
