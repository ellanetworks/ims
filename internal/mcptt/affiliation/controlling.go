package affiliation

import (
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
type Controlling struct {
	source settings.Source

	mu     sync.Mutex
	groups map[string]map[string]*member
}

type member struct {
	group   sip.URI
	id      sip.URI
	clients []string
	expires time.Time
}

type Member struct {
	ID      sip.URI
	Clients []string
}

func NewControlling(source settings.Source) *Controlling {
	return &Controlling{source: source, groups: map[string]map[string]*member{}}
}

// TS 24.379 clause 9.2.2.3.3
func (c *Controlling) Publish(now time.Time, group, user sip.URI, clients []string) ([]string, error) {
	if !c.Eligible(group, user) {
		return nil, ErrForbidden
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	gk, uk := settings.Key(group), settings.Key(user)

	if len(clients) == 0 {
		c.remove(gk, uk)
		return nil, nil
	}

	users := c.groups[gk]
	if users == nil {
		users = map[string]*member{}
		c.groups[gk] = users
	}

	users[uk] = &member{group: group, id: user, clients: slices.Clone(clients), expires: now.Add(MaxExpires)}

	return slices.Clone(clients), nil
}

func (c *Controlling) remove(gk, uk string) {
	delete(c.groups[gk], uk)

	if len(c.groups[gk]) == 0 {
		delete(c.groups, gk)
	}
}

// TS 24.379 clause 9.2.2.3.8
// TS 24.379 clause 9.2.2.3.6
func (c *Controlling) Eligible(group, user sip.URI) bool {
	g := c.source.Current().Group(group)
	return g != nil && g.Entry(user) != nil
}

// TS 24.379 clause 9.2.2.3.8
func (c *Controlling) Revoke() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for gk, users := range c.groups {
		for uk, m := range users {
			if g := c.source.Current().Group(m.group); g == nil || g.Entry(m.id) == nil {
				c.remove(gk, uk)
			}
		}
	}
}

// TS 24.379 clause 6.3.6
func (c *Controlling) Affiliated(now time.Time, group, user sip.URI, client string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	m := c.groups[settings.Key(group)][settings.Key(user)]

	return m != nil && slices.Contains(m.clients, client) && now.Before(m.expires)
}

func (c *Controlling) Members(now time.Time, group sip.URI) []Member {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []Member

	for _, m := range c.groups[settings.Key(group)] {
		if now.Before(m.expires) {
			out = append(out, Member{ID: m.id, Clients: slices.Clone(m.clients)})
		}
	}

	return out
}

// TS 24.379 clause 9.2.2.3.7
// TS 24.379 clause 9.2.2.3.6
func (c *Controlling) ImplicitAffiliate(now time.Time, group, user sip.URI, client string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	gk, uk := settings.Key(group), settings.Key(user)

	users := c.groups[gk]
	if users == nil {
		users = map[string]*member{}
		c.groups[gk] = users
	}

	m := users[uk]
	if m == nil || !now.Before(m.expires) {
		m = &member{group: group, id: user}
		users[uk] = m
	}

	m.expires = now.Add(MaxExpires)

	if slices.Contains(m.clients, client) {
		return false
	}

	m.clients = append(m.clients, client)

	return true
}

// TS 24.379 clause 9.2.2.3.11
func (c *Controlling) CancelImplicit(group, user sip.URI, client string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	gk, uk := settings.Key(group), settings.Key(user)

	m := c.groups[gk][uk]
	if m == nil {
		return
	}

	m.clients = slices.DeleteFunc(m.clients, func(id string) bool { return id == client })
	if len(m.clients) == 0 {
		c.remove(gk, uk)
	}
}
