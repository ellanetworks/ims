package controlling

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/participating"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/internal/tracing"
	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/mcptt/media"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/ua"
)

// TS 24.379 clause 9.2.2.3.2
type Affiliations interface {
	Affiliated(now time.Time, group, user sip.URI, client string) bool
	Members(now time.Time, group sip.URI) []affiliation.Member
}

type Participating interface {
	Terminate(req *sip.Request) participating.Termination
}

// TS 24.379 clause 10.1.1.4.1.1
type Peers interface {
	Self() string
	Route(node string) (sip.URI, netip.AddrPort, bool)
}

type Config struct {
	Agent         *ua.Agent
	Source        settings.Source
	Affiliations  Affiliations
	Media         *media.Relay
	Participating Participating
	Peers         Peers
	Released      func(group sip.URI)
	Logger        *slog.Logger
}

type Controlling struct {
	cfg      Config
	detached tracing.Detached

	mu       sync.Mutex
	groups   map[string]*group
	sessions map[string]*session
	users    map[string]map[*session]int
}

const tracerName = "github.com/ellanetworks/ims/internal/mcptt/controlling"

type group struct {
	st   *ua.Strand
	sess *session
}

func New(cfg Config) *Controlling {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Controlling{
		cfg: cfg, detached: tracing.NewDetached(cfg.Agent.Layer().TracerProvider(), tracerName, "mcptt"), groups: map[string]*group{}, sessions: map[string]*session{}, users: map[string]map[*session]int{},
	}
}

func (c *Controlling) Strand(id sip.URI) *ua.Strand {
	if c.cfg.Source.Current().Group(id) == nil {
		return nil
	}

	return c.group(id).st
}

func (c *Controlling) Active(id sip.URI) bool {
	if c.cfg.Source.Current().Group(id) == nil {
		return false
	}

	g := c.group(id)

	return g.st.Confined() && g.sess != nil
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

func (c *Controlling) Revoke(ctx context.Context) {
	c.mu.Lock()

	groups := make([]*group, 0, len(c.groups))
	for _, g := range c.groups {
		groups = append(groups, g)
	}

	c.mu.Unlock()

	for _, g := range groups {
		g.st.Post(func() {
			if s := g.sess; s != nil {
				s.revoke(ctx)
			}
		})
	}
}

// RFC 5627 section 3.1
// TS 24.379 clause 4.5
// TS 24.379 clause 4.8
const SessionParam = "gr"

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

// TS 24.379 clause 4.5
// TS 24.379 clause 10.1.1.4.2
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

// TS 24.379 clause 10.1.1.3.1.1
func (c *Controlling) GroupCalls(user sip.URI) []sip.URI {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []sip.URI
	for s := range c.users[settings.Key(user)] {
		out = append(out, s.g.ID())
	}

	return out
}

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

type strandClock struct {
	st *ua.Strand
	c  *Controlling
}

func (s strandClock) AfterFunc(d time.Duration, f func()) floor.Timer { return s.st.AfterFunc(d, f) }

func (s strandClock) Now() time.Time { return s.c.now() }
