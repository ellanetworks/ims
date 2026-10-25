// Package controlling is the controlling MCPTT function (TS 24.379 §6.3.3) of the groups of the server: their
// on-demand prearranged group calls (§10.1.1.4), each a group session with a floor control server (TS 24.380 §6.3)
// and a media distributor, the members it invites (§10.1.1.4.1.1) and those that join it (§10.1.1.4.2 step 15), and
// its release (§6.3.8.1, §10.1.1.4.4).
//
// Each session is a b2bua.Call, one to many: the function answers its participants and offers itself to them, with
// the media it anchors. A group's sessions, one at a time, run on the strand of the group (Strand), with everything of
// them: SIP handlers, the floor control server, TNG1, TNG3, the timers of the floor, the media indications. The
// function reaches the clients of the members through the participating function serving them, in process [PM 11].
//
// The server does not buffer media (TS 24.380 §6.3.2.2): the originator's 200 waits for a confirmed indication, an
// invited member's 200, and an unconfirmed one starts nothing (TS 24.379 §10.1.1.4.2).
package controlling

import (
	"crypto/rand"
	"log/slog"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/participating"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// Affiliations are the controlling function's affiliation state (TS 24.379 §9.2.2.3.2).
type Affiliations interface {
	Affiliated(now time.Time, group, user sip.URI, client string) bool
	Members(now time.Time, group sip.URI) []affiliation.Member
}

// Participating is the participating function serving the members, as the controlling function reaches it.
type Participating interface {
	Terminate(req *sip.Request) participating.Termination
}

// Config configures the function.
type Config struct {
	Agent         *ua.Agent
	Source        settings.Source
	Affiliations  Affiliations
	Media         *media.Relay
	Participating Participating
	// Logger is the log of the function; slog.Default() if nil. The records of a session carry its group, its MCPTT
	// session identity and the Call-ID of its originator's INVITE; those of a member, its MCPTT ID and the Call-ID of
	// its leg. No media packet or floor control message is logged.
	Logger *slog.Logger
}

// Controlling is the controlling function.
type Controlling struct {
	cfg Config

	mu     sync.Mutex
	groups map[string]*group
	// sessions are the sessions by the "gr" of their MCPTT session identity; users the sessions each user, by key,
	// takes part in or is invited to, with how many members it has in each.
	sessions map[string]*session
	users    map[string]map[*session]int
}

// group is a group of the function, its strand made on first use.
type group struct {
	st *ua.Strand
	// sess is the group's session, on st.
	sess *session
}

// New is the controlling function.
func New(cfg Config) *Controlling {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Controlling{cfg: cfg, groups: map[string]*group{}, sessions: map[string]*session{}, users: map[string]map[*session]int{}}
}

// Strand is the strand of the calls of group id, a configured group: the INVITEs that start a session of the group
// or join it (TS 24.379 §10.1.1.4.2 step 9) go on it (ua.Config.StrandFor). Nil for another identity.
func (c *Controlling) Strand(id sip.URI) *ua.Strand {
	if c.cfg.Source.Current().Group(id) == nil {
		return nil
	}

	return c.group(id).st
}

func (c *Controlling) group(id sip.URI) *group {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := settings.Key(id)

	g := c.groups[k]
	if g == nil {
		g = &group{st: c.cfg.Agent.NewStrand()}
		c.groups[k] = g
	}

	return g
}

// Revoke applies a change of the settings to the sessions, without waiting for them: on the strand of each group, its
// session, if any, checked against the current settings (session.revoke). A session keeps the settings it started
// with otherwise.
func (c *Controlling) Revoke() {
	c.mu.Lock()

	groups := make([]*group, 0, len(c.groups))
	for _, g := range c.groups {
		groups = append(groups, g)
	}

	c.mu.Unlock()

	for _, g := range groups {
		g.st.Post(func() {
			if s := g.sess; s != nil {
				s.revoke()
			}
		})
	}
}

// SessionParam is the URI parameter of the MCPTT session identity that tells the sessions apart: the "gr" of a GRUU
// (RFC 5627 §3.1), of the controlling PSI, which routes it to the function and names no group or user (TS 24.379
// §4.5, §4.8).
const SessionParam = "gr"

// SessionStrand is the strand of the session of MCPTT session identity id (TS 24.379 §4.5), and whether there is
// one.
func (c *Controlling) SessionStrand(id sip.URI) (*ua.Strand, bool) {
	s := c.session(id)
	if s == nil {
		return nil, false
	}

	return s.grp.st, true
}

func (c *Controlling) session(id sip.URI) *session {
	gr, ok := id.Params.Get(SessionParam)
	if !ok || settings.Key(id) != settings.Key(c.cfg.Source.Current().ControllingPSI()) {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.sessions[gr]
}

// allocate allocates the MCPTT session identity of s (TS 24.379 §4.5, §10.1.1.4.2 step 14e).
func (c *Controlling) allocate(s *session, psi sip.URI) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for {
		gr := rand.Text()
		if c.sessions[gr] == nil {
			c.sessions[gr] = s
			s.gr = gr
			s.id = psi.Clone()
			s.id.Params.Set(SessionParam, gr)

			return
		}
	}
}

func (c *Controlling) free(s *session) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sessions[s.gr] == s {
		delete(c.sessions, s.gr)
	}

	for _, m := range s.members {
		c.drop(settings.Key(m.id), s)
	}
}

// GroupCalls are the groups of the calls user takes part in or is invited to, the originator of one being set up
// among them: the MCPTT group calls of the user the participating function counts against <MaxSimultaneousCallsN6>
// (TS 24.379 §10.1.1.3.1.1 step 5), which with the hop elided [PM 11] are the controlling function's.
func (c *Controlling) GroupCalls(user sip.URI) []sip.URI {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []sip.URI
	for s := range c.users[settings.Key(user)] {
		out = append(out, s.g.ID())
	}

	return out
}

// enter and exit count a member of user id in s, as GroupCalls reads them.
func (c *Controlling) enter(s *session, id sip.URI) {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := settings.Key(id)
	if c.users[k] == nil {
		c.users[k] = map[*session]int{}
	}

	c.users[k][s]++
}

func (c *Controlling) exit(s *session, id sip.URI) {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := settings.Key(id)
	if n := c.users[k][s]; n > 1 {
		c.users[k][s] = n - 1
		return
	}

	c.drop(k, s)
}

func (c *Controlling) drop(k string, s *session) {
	delete(c.users[k], s)

	if len(c.users[k]) == 0 {
		delete(c.users, k)
	}
}

func (c *Controlling) now() time.Time { return c.cfg.Agent.Layer().Clock().Now() }

// strandClock is the clock of the floor control server: the timers of the call's strand, on the agent's clock.
type strandClock struct {
	st *ua.Strand
	c  *Controlling
}

func (s strandClock) AfterFunc(d time.Duration, f func()) floor.Timer { return s.st.AfterFunc(d, f) }

func (s strandClock) Now() time.Time { return s.c.now() }
