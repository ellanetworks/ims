package affiliation

import (
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
)

// ErrForbidden is the refusal of an affiliation by the server owning the group: of a group it does not have, or of a
// user not a member of it, which it answers 403 (TS 24.379 §9.2.2.3.3 steps 4, 5).
var ErrForbidden = errors.New("affiliation: not a group of the server, or not a member of it")

// Controlling is the affiliation state of the controlling function, the server owning the groups: its MCPTT group
// information entries, each of MCPTT user information entries, each of MCPTT client IDs and an expiration time (TS
// 24.379 §9.2.2.3.2). Each configured group has its entry; an entry of no users is not kept.
type Controlling struct {
	source settings.Source

	mu     sync.Mutex
	groups map[string]map[string]*member
}

// member is an MCPTT user information entry of a group.
type member struct {
	group   sip.URI
	id      sip.URI
	clients []string
	expires time.Time
}

// Member is a user affiliated to a group, at its clients.
type Member struct {
	ID      sip.URI
	Clients []string
}

// NewControlling is the affiliation state of the groups of the configuration of source.
func NewControlling(source settings.Source) *Controlling {
	return &Controlling{source: source, groups: map[string]map[string]*member{}}
}

// Publish is the receipt of the affiliation of user to group, at clients, none to de-affiliate it (TS 24.379
// §9.2.2.3.3), in process: its expiration time is that of the participating function's PUBLISH, MaxExpires, zero
// without clients (§9.2.2.2.6 steps 4, 5). It returns the clients the group holds the user affiliated at.
func (c *Controlling) Publish(now time.Time, group, user sip.URI, clients []string) ([]string, error) {
	// Steps 4, 5.
	if !c.Eligible(group, user) {
		return nil, ErrForbidden
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	gk, uk := settings.Key(group), settings.Key(user)

	// Step 10 a.
	if len(clients) == 0 {
		c.remove(gk, uk)
		return nil, nil
	}

	// Step 11.
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

// Eligible is the affiliation eligibility check (TS 24.379 §9.2.2.3.8), and the implicit one (§9.2.2.3.6): group is
// a configured group, and user a member of it.
func (c *Controlling) Eligible(group, user sip.URI) bool {
	g := c.source.Current().Group(group)
	return g != nil && g.Entry(user) != nil
}

// Revoke removes the users no longer eligible to the groups they are affiliated to (§9.2.2.3.8), of a change of the
// settings: a group deleted, or a member removed from it.
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

// Affiliated is the affiliation check (TS 24.379 §6.3.6): whether user is affiliated to group at client.
func (c *Controlling) Affiliated(now time.Time, group, user sip.URI, client string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Steps 1 to 4.
	m := c.groups[settings.Key(group)][settings.Key(user)]

	return m != nil && slices.Contains(m.clients, client) && now.Before(m.expires)
}

// Members are the users affiliated to group, at the clients they are affiliated at, in no order.
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

// ImplicitAffiliate is the affiliation of user to group at client by implicit affiliation (TS 24.379 §9.2.2.3.7),
// of an eligible user (§9.2.2.3.6): the client added to the user's entry, made if none, its expiration MaxExpires by
// local policy. It reports whether it added the client, which CancelImplicit removes.
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

// CancelImplicit is the implicit affiliation status change cancellation (TS 24.379 §9.2.2.3.11): client removed from
// the entry of user in group that ImplicitAffiliate added it to, and the entry with its last client.
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
