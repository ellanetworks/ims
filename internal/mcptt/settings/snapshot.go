package settings

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ellanetworks/ims/mcptt/floor"
	"github.com/ellanetworks/ims/sip"
)

// Snapshot is validated settings, immutable: what it returns must not be modified.
type Snapshot struct {
	cfg                        Settings
	participating, controlling sip.URI
	users                      map[string]*User
	groups                     map[string]*Group
}

// defaultEncoding is the codec the clients must support (TS 26.179 §4.1.1).
const defaultEncoding = "AMR-WB"

// MaxUsers is the most users the server serves, as Ella Core the most subscribers.
const MaxUsers = 1000

// DefaultT7 is T7 (Floor Idle) when the service configuration has none: a local value, TS 24.380 Table 11.1.3-1
// having no default.
const DefaultT7 = 2 * time.Second

// minPriorityLevels is the lowest <num-levels-priority-hierarchy> of its schema (TS 24.484 §8.4.2.3).
const minPriorityLevels = 4

// New validates c and makes it a snapshot, of a copy of c with the defaults of TS 24.481, TS 24.484 and TS 24.380
// applied to what is absent. Its error joins a FieldError for every error of c, each after the path of its key, such
// as groups[2].list.entries[0].uri; one of an identity taken names what has it, such as the user sip:a@example.com.
//
// New rejects what settings should not have; a source of documents of a management server maps them to Settings first,
// as their specifications read them: a value of <on-network-action-upon-expiration-of-timeout-for-acknowledgement-
// of-required-members> other than "proceed", of a later release, is "abandon" (TS 24.481 §7.2.2 u).
func New(c Settings) (*Snapshot, error) {
	s := &Snapshot{cfg: clone(c), users: map[string]*User{}, groups: map[string]*Group{}}
	v := &validator{ids: map[string]string{}, services: map[string]bool{}, impus: map[string]string{}}

	s.participating = v.identity("system.participating-psi", c.System.ParticipatingPSI, "the participating PSI", true)
	s.controlling = v.identity("system.controlling-psi", c.System.ControllingPSI, "the controlling PSI", true)
	v.service(&s.cfg.ServiceConfiguration)

	if n := len(c.Users); n > MaxUsers {
		v.conflict("", "%d users: the server serves at most %d", n, MaxUsers)
	}

	ids := make([]sip.URI, len(s.cfg.Users))

	for i := range s.cfg.Users {
		u := &s.cfg.Users[i]

		ids[i] = v.user(fmt.Sprintf("users[%d]", i), u)
		if _, dup := s.users[key(ids[i])]; ids[i].IsSIP() && !dup {
			u.id = ids[i]
			s.users[key(ids[i])] = u
		}
	}

	for i := range s.cfg.Groups {
		g := &s.cfg.Groups[i]

		k := v.group(fmt.Sprintf("groups[%d]", i), g, s.users)
		if _, dup := s.groups[k]; k != "" && !dup {
			s.groups[k] = g
		}
	}

	// The users' references to the groups, once the groups are known.
	for i := range s.cfg.Users {
		v.affiliations(fmt.Sprintf("users[%d]", i), &s.cfg.Users[i], ids[i], s.groups)
	}

	if err := errors.Join(v.errs...); err != nil {
		return nil, err
	}

	return s, nil
}

// ServiceConfiguration is the service configuration of the organisation.
func (s *Snapshot) ServiceConfiguration() *ServiceConfiguration { return &s.cfg.ServiceConfiguration }

// ParticipatingPSI and ControllingPSI are the PSIs of the system, parsed.
func (s *Snapshot) ParticipatingPSI() sip.URI { return s.participating }

func (s *Snapshot) ControllingPSI() sip.URI { return s.controlling }

// User is the user of MCPTT ID id, nil if none.
func (s *Snapshot) User(id sip.URI) *User { return s.users[key(id)] }

// Group is the group of MCPTT group ID id, nil if none.
func (s *Snapshot) Group(id sip.URI) *Group { return s.groups[key(id)] }

// ServerTimers are the timers and counters of the floor control server of a call of g: from the service
// configuration and the group document, as TS 24.380 Table 11.1.3-1 and Table 11.2.3-1 obtain them. Those not
// configured are zero, the defaults of the tables, which floor applies.
func (s *Snapshot) ServerTimers(g *Group) floor.ServerTimers {
	on := s.cfg.ServiceConfiguration.OnNetwork
	fc := on.FCTimersCounters

	return floor.ServerTimers{
		T1: *fc.T1, T2: *on.TransmitTime.TimeLimit, T3: *fc.T3, T4: *g.T4, T7: *fc.T7, T8: *fc.T8, C7: *fc.C7,
	}
}

// ID is the MCPTT ID of u.
func (u *User) ID() sip.URI { return u.id }

// HasIMPU reports whether impu is an IMS public user identity of u, as RFC 3261 §19.1.4 and RFC 3966 §4 compare
// them.
func (u *User) HasIMPU(impu sip.URI) bool {
	k := key(impu)
	return k != "" && slices.Contains(u.impus, k)
}

// ImplicitGroups are the groups of <ImplicitAffiliations>, in order.
func (u *User) ImplicitGroups() []*Group { return u.implicit }

// N2 is <MaxAffiliationsN2>, and whether it limits the affiliations of u: not if absent.
func (u *User) N2() (n int, limited bool) {
	if u.MaxAffiliationsN2 == nil {
		return 0, false
	}

	return *u.MaxAffiliationsN2, true
}

// N6 is <MaxSimultaneousCallsN6>, and whether it limits the group calls of u: not if absent.
func (u *User) N6() (n int, limited bool) {
	if u.MaxSimultaneousCallsN6 == nil {
		return 0, false
	}

	return *u.MaxSimultaneousCallsN6, true
}

// ID is the MCPTT ID of the member.
func (e *Entry) ID() sip.URI { return e.id }

// ID is the MCPTT group ID of g.
func (g *Group) ID() sip.URI { return g.id }

// Entry is the entry of g of MCPTT ID id: the member, nil if id is not one.
func (g *Group) Entry(id sip.URI) *Entry {
	i, ok := g.entries[key(id)]
	if !ok {
		return nil
	}

	return &g.List.Entries[i]
}

func clone(c Settings) Settings {
	c.Users = slices.Clone(c.Users)
	for i := range c.Users {
		u := &c.Users[i]
		u.IMPUs = slices.Clone(u.IMPUs)
		u.impus = nil
		u.ImplicitAffiliations = slices.Clone(u.ImplicitAffiliations)
		u.MaxAffiliationsN2 = clonePtr(u.MaxAffiliationsN2)
		u.MaxSimultaneousCallsN6 = clonePtr(u.MaxSimultaneousCallsN6)
		u.id, u.implicit = sip.URI{}, nil
	}

	on := &c.ServiceConfiguration.OnNetwork
	on.NumLevelsPriorityHierarchy = clonePtr(on.NumLevelsPriorityHierarchy)
	on.TransmitTime.TimeLimit = clonePtr(on.TransmitTime.TimeLimit)

	fc := &on.FCTimersCounters
	fc.T1, fc.T3, fc.T7, fc.T8, fc.C7 = clonePtr(fc.T1), clonePtr(fc.T3), clonePtr(fc.T7), clonePtr(fc.T8), clonePtr(fc.C7)

	c.Groups = slices.Clone(c.Groups)
	for i := range c.Groups {
		g := &c.Groups[i]
		g.Encodings = slices.Clone(g.Encodings)
		g.List.Entries = slices.Clone(g.List.Entries)
		g.T4 = clonePtr(g.T4)
		g.id, g.entries = sip.URI{}, nil
	}

	return c
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}

	v := *p

	return &v
}

// validator collects the errors of settings, each with its path.
type validator struct {
	errs []error
	// ids are what has each identity, by key, such as the user sip:a@example.com: an identity is of one user, group
	// or function. services are those of the functions and the groups, and impus what has each IMPU, such as an IMPU
	// of sip:a@example.com: an IMPU is none of them, which a P-Asserted-Identity would make ambiguous.
	ids      map[string]string
	services map[string]bool
	impus    map[string]string
}

// add adds the error of the value at path.
func (v *validator) add(path, format string, a ...any) {
	v.errs = append(v.errs, &FieldError{Path: path, Message: fmt.Sprintf(format, a...)})
}

// conflict adds the error of the value at path with the rest of the settings.
func (v *validator) conflict(path, format string, a ...any) {
	v.errs = append(v.errs, &FieldError{Path: path, Message: fmt.Sprintf(format, a...), Conflict: true})
}

// identity parses the identity at path, a SIP URI of no parameters, headers or password, and claims it for what, a
// function or group if service; "" if it is not one. One already claimed is an error, and still the identity of what
// has it here, so that the references to it are not errors too.
func (v *validator) identity(path, s, what string, service bool) sip.URI {
	u, ok := v.uri(path, s)
	if !ok {
		return sip.URI{}
	}

	if !u.IsSIP() || u.Password != "" || len(u.Params) > 0 || u.Headers != "" {
		v.add(path, "%q is not a SIP URI without parameters or headers", s)
		return sip.URI{}
	}

	k := key(u)
	if w, ok := v.ids[k]; ok {
		v.conflict(path, "%s is also %s", s, w)
		return u
	}

	if w, ok := v.impus[k]; ok && service {
		v.conflict(path, "%s is also %s", s, w)
		return u
	}

	v.ids[k] = what
	v.services[k] = service

	return u
}

func (v *validator) uri(path, s string) (sip.URI, bool) {
	if s == "" {
		v.add(path, "is required")
		return sip.URI{}, false
	}

	u, err := sip.ParseURI(s)
	if err != nil {
		v.add(path, "%v", err)
		return sip.URI{}, false
	}

	return u, true
}

func (v *validator) service(c *ServiceConfiguration) {
	if !isDomain(c.Domain) {
		v.add("service-configuration.domain", "%q is not a domain name", c.Domain)
	}

	on := &c.OnNetwork

	const levels = "service-configuration.on-network.num-levels-priority-hierarchy"

	if n := on.NumLevelsPriorityHierarchy; n == nil {
		on.NumLevelsPriorityHierarchy = ptr(minPriorityLevels)
	} else if *n < minPriorityLevels || *n > 256 {
		v.add(levels, "%d is not within %d and 256 (TS 24.484 §8.4.2.3)", *n, minPriorityLevels)
	}

	def := floor.DefaultServerTimers
	t := &on.FCTimersCounters

	const fc = "service-configuration.on-network.fc-timers-counters."

	// TS 24.380 Table 11.1.3-1: T1, T2, T7 and T8 of zero mean nothing; T3 of zero is the one of audio cut-in.
	v.timer("service-configuration.on-network.transmit-time.time-limit", &on.TransmitTime.TimeLimit, def.T2, false)
	v.timer(fc+"T1-end-of-rtp-media", &t.T1, def.T1, false)
	v.timer(fc+"T3-stop-talking-grace", &t.T3, def.T3, true)
	v.timer(fc+"T8-floor-revoke", &t.T8, def.T8, false)

	// T7 has no default of TS 24.380 Table 11.1.3-1, which leaves it to the radio access network: the server's.
	v.timer(fc+"T7-floor-idle", &t.T7, DefaultT7, false)

	switch {
	case *t.T1 > floor.MaxT1:
		v.add(fc+"T1-end-of-rtp-media", "%s is above its maximum of %s (TS 24.380 Table 11.1.3-1)", *t.T1, floor.MaxT1)
	case *t.T1 > 0 && *t.T1 < floor.MinT1:
		// Table 11.1.3-1 sets no minimum; the server's (floor.MinT1).
		v.add(fc+"T1-end-of-rtp-media", "%s is below %s (media reported every 200 ms)", *t.T1, floor.MinT1)
	}

	if t.C7 == nil {
		t.C7 = ptr(def.C7)
	} else if *t.C7 < 0 || *t.C7 > 65535 {
		v.add(fc+"C7-floor-idle", "%d is not an unsignedShort (TS 24.484 §8.4.2.3)", *t.C7)
	}
}

// timer validates the timer at path, of default def if absent: not negative, nor zero unless zero is.
func (v *validator) timer(path string, d **time.Duration, def time.Duration, zero bool) {
	switch {
	case *d == nil:
		*d = ptr(def)
	case **d < 0:
		v.add(path, "%s is negative", **d)
	case **d == 0 && !zero:
		v.add(path, "is zero, which means nothing (TS 24.380 Table 11.1.3-1)")
	}
}

func ptr[T any](v T) *T { return &v }

func (v *validator) duration(path string, d time.Duration) {
	if d < 0 {
		v.add(path, "%s is negative", d)
	}
}

// user validates u and returns its MCPTT ID, the zero URI if invalid.
func (v *validator) user(path string, u *User) sip.URI {
	id := v.identity(path+".mcptt-user-id", u.MCPTTUserID, "the user "+u.MCPTTUserID, false)
	v.name(path+".user-alias", u.Alias)

	if len(u.IMPUs) == 0 {
		v.add(path+".impus", "needs at least one IMPU")
	}

	for i, s := range u.IMPUs {
		p := fmt.Sprintf("%s.impus[%d]", path, i)

		w, ok := v.uri(p, s)
		switch {
		case !ok:
			continue
		case !w.IsSIP() && !w.IsTel() || key(w) == "" || w.Password != "" || w.Headers != "":
			v.add(p, "%q is not a SIP or tel URI without password or headers (TS 23.003 §13.4)", s)
			continue
		}

		k := key(w)

		switch q, ok := v.ids[k]; {
		case slices.Contains(u.impus, k):
			v.add(p, "%s is listed twice", s)
			continue
		case ok && v.services[k]:
			v.conflict(p, "%s is also %s", s, q)
			continue
		}

		if _, ok := v.impus[k]; !ok {
			v.impus[k] = "an IMPU of " + u.MCPTTUserID
		}

		u.impus = append(u.impus, k)
	}

	if n := u.MaxAffiliationsN2; n != nil && *n < 0 {
		v.add(path+".max-affiliations-n2", "%d is negative", *n)
	}

	if n := u.MaxSimultaneousCallsN6; n != nil && *n < 1 {
		v.add(path+".max-simultaneous-calls-n6", "%d is not positive", *n)
	}

	return id
}

// affiliations validates the implicit affiliations of u, of MCPTT ID id: groups of which u is a member, within N2.
func (v *validator) affiliations(path string, u *User, id sip.URI, groups map[string]*Group) {
	seen := map[string]bool{}

	for i, s := range u.ImplicitAffiliations {
		p := fmt.Sprintf("%s.implicit-affiliations[%d]", path, i)

		g, ok := v.uri(p, s)
		if !ok {
			continue
		}

		k := key(g)

		switch grp := groups[k]; {
		case grp == nil:
			v.conflict(p, "%s is not a configured group", s)
		case seen[k]:
			v.add(p, "%s is listed twice", s)
		case id.IsSIP() && grp.Entry(id) == nil:
			// TS 24.379 §9.2.2.3.3 step 5: a user is affiliated to the groups it is a member of only.
			v.conflict(p, "%s is a group %s is not a member of", s, u.MCPTTUserID)
		default:
			u.implicit = append(u.implicit, grp)
		}

		seen[k] = true
	}

	if n, ok := u.N2(); ok && n >= 0 && len(u.ImplicitAffiliations) > n {
		v.add(path+".implicit-affiliations", "%d groups, more than max-affiliations-n2 %d", len(u.ImplicitAffiliations), n)
	}
}

// group validates g, indexes its entries, applies its defaults and returns its key, "" if its group ID is invalid.
func (v *validator) group(path string, g *Group, users map[string]*User) string {
	id := v.identity(path+".uri", g.URI, "the group "+g.URI, true)
	v.name(path+".display-name", g.DisplayName)

	if !g.InviteMembers {
		v.add(path+".on-network-invite-members", "is false, of a chat group (TS 24.481 §7.2.7): only prearranged groups are supported")
	}

	// TS 24.380 Table 11.1.3-1: T4 of zero releases the call as soon as the floor is idle.
	v.timer(path+".on-network-hang-timer", &g.T4, floor.DefaultServerTimers.T4, true)
	v.duration(path+".on-network-maximum-duration", g.TNG3)
	v.duration(path+".on-network-timeout-for-acknowledgement-of-required-members", g.TNG1)

	if g.InviteMembers && g.TNG3 == 0 {
		v.add(path+".on-network-maximum-duration", "is required of a prearranged group (TS 24.481 §7.2.7)")
	}

	v.encodings(path+".preferred-voice-encodings", g)

	required := v.entries(path+".list.entries", g, users)
	n := len(g.List.Entries)

	switch m := g.MaxParticipants; {
	case m == 0:
	case m < 2:
		v.add(path+".on-network-max-participant-count", "%d: a call of fewer than 2 participants is released (TS 24.379 §6.3.8.1 2)", m)
	case m < required:
		v.add(path+".on-network-max-participant-count", "%d is fewer than the %d required members (TS 24.379 §6.3.5.5 NOTE 4)", m, required)
	}

	// The invited members are the members but the initiator (TS 24.379 §6.3.3.3).
	if m := g.MinimumToStart; m < 0 || m > 65535 || m > max(n-1, 0) {
		v.add(path+".on-network-minimum-number-to-start", "%d is not within 0 and the %d members to invite", m, max(n-1, 0))
	}

	if m := g.MinimumAffiliated; m < 0 || m > n {
		v.add(path+".on-network-minimum-number-of-affiliated-members", "%d is not within 0 and the %d members", m, n)
	}

	const action = ".on-network-action-upon-expiration-of-timeout-for-acknowledgement-of-required-members"

	switch g.TNG1Action {
	case "", Proceed, Abandon:
	default:
		v.add(path+action, "%q is neither %q nor %q (TS 24.481 Table 7.2.2-1)", g.TNG1Action, Proceed, Abandon)
	}

	if required > 0 {
		// TS 24.379 §6.3.3.3: TNG1 runs when there are required members.
		if g.TNG1 == 0 {
			v.add(path+".on-network-timeout-for-acknowledgement-of-required-members", "is required with required members (TS 24.379 §6.3.3.3)")
		}

		if g.TNG1Action == "" {
			v.add(path+action, "is required with required members (TS 24.379 §6.3.3.3)")
		}
	}

	if !id.IsSIP() {
		return ""
	}

	g.id = id

	return key(id)
}

func (v *validator) encodings(path string, g *Group) {
	if len(g.Encodings) == 0 {
		g.Encodings = []string{defaultEncoding}
		return
	}

	for i, e := range g.Encodings {
		p := fmt.Sprintf("%s[%d]", path, i)

		switch {
		case !isToken(e):
			v.add(p, "%q is not an encoding name of a=rtpmap (RFC 4566 §6)", e)
		case slices.ContainsFunc(g.Encodings[:i], func(f string) bool { return strings.EqualFold(e, f) }):
			v.add(p, "%s is listed twice", e)
		}
	}
}

// entries validates and indexes the entries of g, members of the users, and returns how many are required.
func (v *validator) entries(path string, g *Group, users map[string]*User) int {
	g.entries = make(map[string]int, len(g.List.Entries))
	required := 0

	for i, e := range g.List.Entries {
		p := fmt.Sprintf("%s[%d]", path, i)

		if e.Required {
			required++
		}

		if e.UserPriority < 0 || e.UserPriority > 255 {
			v.add(p+".user-priority", "%d is not within 0 and 255 (TS 24.481 §7.2.4.2)", e.UserPriority)
		}

		u, ok := v.uri(p+".uri", e.URI)
		if !ok {
			continue
		}

		k := key(u)

		switch _, dup := g.entries[k]; {
		case len(u.Params) > 0 || k == "":
			v.add(p+".uri", "%q is not a SIP URI without parameters or headers", e.URI)
		case users[k] == nil:
			v.conflict(p+".uri", "%s is not a configured user", e.URI)
		case dup:
			v.add(p+".uri", "%s is listed twice", e.URI)
		default:
			g.entries[k] = i
			g.List.Entries[i].id = u
		}
	}

	return required
}

// maxName is the most characters of a name: of a user's alias, of a group's display name.
const maxName = 64

// name validates the name at path, optional: of at most maxName characters, none a control character.
func (v *validator) name(path, s string) {
	switch {
	case !utf8.ValidString(s):
		v.add(path, "is not UTF-8")
	case utf8.RuneCountInString(s) > maxName:
		v.add(path, "is longer than %d characters", maxName)
	case strings.TrimSpace(s) != s:
		v.add(path, "%q begins or ends with a space", s)
	case strings.ContainsFunc(s, unicode.IsControl):
		v.add(path, "%q has a control character", s)
	}
}

// isToken reports whether s is of the token characters (RFC 4566 §9).
func isToken(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if !isAlnum(c) && strings.IndexByte("!#$%&'*+-.^_`{|}~", c) < 0 {
			return false
		}
	}

	return true
}

// isDomain reports whether s is a domain name of letters, digits and hyphens (RFC 1123 §2.1).
func isDomain(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}

	for _, l := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}

		for i := range len(l) {
			if c := l[i]; !isAlnum(c) && c != '-' {
				return false
			}
		}
	}

	return true
}

func isAlnum(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}
