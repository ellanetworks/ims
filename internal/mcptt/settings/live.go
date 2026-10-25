package settings

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ellanetworks/ims/sip"
)

// Store persists the changes of Live, each before it takes effect, in one transaction. A user or group is found by
// the identity of its MCPTT ID or URI, as Key has it. Of a change of a group or member, users are the users whose
// implicit affiliations it changes, which it stores too. A user deleted goes from the groups it is a member of; a
// group deleted, or a member removed, from the implicit affiliations of its users.
type Store interface {
	UpdateSystem(ctx context.Context, s System) error
	UpdateServiceConfiguration(ctx context.Context, c ServiceConfiguration) error
	CreateUser(ctx context.Context, u User) error
	UpdateUser(ctx context.Context, u User) error
	DeleteUser(ctx context.Context, mcpttID string) error
	CreateGroup(ctx context.Context, g Group, users []User) error
	UpdateGroup(ctx context.Context, g Group, users []User) error
	DeleteGroup(ctx context.Context, uri string) error
	PutMember(ctx context.Context, group string, e Entry, users []User) error
	DeleteMember(ctx context.Context, group, mcpttID string) error
}

// Live serves the current settings to the running server and persists changes before they take effect: the Source
// of the functions. Each change is validated with the rest of the settings, as New validates them, and is for the
// calls and requests after it; a call keeps the snapshot it started with, but for its group deleted or its members
// removed, which OnChange applies to it.
type Live struct {
	store    Store
	onChange func()

	// mu serializes the changes, each from the settings before it.
	mu      sync.Mutex
	current atomic.Pointer[state]
}

// state are settings as written and their snapshot.
type state struct {
	written Settings
	snap    *Snapshot
}

// NewLive serves initial, the settings of store, which must be valid.
func NewLive(store Store, initial Settings) (*Live, error) {
	snap, err := New(initial)
	if err != nil {
		return nil, err
	}

	l := &Live{store: store}
	l.current.Store(&state{written: initial, snap: snap})

	return l, nil
}

// OnChange makes f called after each change, once it took effect; before the first change.
func (l *Live) OnChange(f func()) { l.onChange = f }

// Current is the snapshot of the current settings.
func (l *Live) Current() *Snapshot { return l.current.Load().snap }

// Get are the current settings as written, without the defaults New applies. Their slices must not be modified.
func (l *Live) Get() Settings { return l.current.Load().written }

func (l *Live) UpdateSystem(ctx context.Context, s System) error {
	return l.change(func(next *Settings) (string, error) {
		next.System = s
		return "system", nil
	}, func() error { return l.store.UpdateSystem(ctx, s) })
}

func (l *Live) UpdateServiceConfiguration(ctx context.Context, c ServiceConfiguration) error {
	return l.change(func(next *Settings) (string, error) {
		next.ServiceConfiguration = c
		return "service-configuration", nil
	}, func() error { return l.store.UpdateServiceConfiguration(ctx, c) })
}

// CreateUser adds u, after the other users, and returns it as stored.
func (l *Live) CreateUser(ctx context.Context, u User) (User, error) {
	err := l.change(func(next *Settings) (string, error) {
		u.ImplicitAffiliations = next.groupURIs(u.ImplicitAffiliations)
		next.Users = append(slices.Clone(next.Users), u)

		return "users[" + strconv.Itoa(len(next.Users)-1) + "]", nil
	}, func() error { return l.store.CreateUser(ctx, u) })

	return u, err
}

// UpdateUser replaces the user of u's MCPTT ID with u, which keeps the MCPTT ID as the user has it, and returns it as
// stored.
func (l *Live) UpdateUser(ctx context.Context, u User) (User, error) {
	err := l.change(func(next *Settings) (string, error) {
		i := next.UserIndex(u.MCPTTUserID)
		if i < 0 {
			return "", refused(ErrNotFound, "user not found")
		}

		u.MCPTTUserID = next.Users[i].MCPTTUserID
		u.ImplicitAffiliations = next.groupURIs(u.ImplicitAffiliations)
		next.Users = slices.Clone(next.Users)
		next.Users[i] = u

		return "users[" + strconv.Itoa(i) + "]", nil
	}, func() error { return l.store.UpdateUser(ctx, u) })

	return u, err
}

// DeleteUser deletes the user of MCPTT ID id, and removes it from the groups it is a member of. What the removal makes
// invalid of a group is a conflict.
func (l *Live) DeleteUser(ctx context.Context, id string) error {
	var written string

	return l.change(func(next *Settings) (string, error) {
		i := next.UserIndex(id)
		if i < 0 {
			return "", refused(ErrNotFound, "user not found")
		}

		written = next.Users[i].MCPTTUserID
		k := keyOf(written)
		member := func(e Entry) bool { return keyOf(e.URI) == k }

		groups := slices.Clone(next.Groups)
		for j := range groups {
			groups[j].List.Entries = slices.DeleteFunc(slices.Clone(groups[j].List.Entries), member)
		}

		next.Groups = groups
		next.Users = slices.Delete(slices.Clone(next.Users), i, i+1)

		return "", nil
	}, func() error { return l.store.DeleteUser(ctx, written) })
}

// Implicit is, of members by MCPTT ID, whether the group is an implicit affiliation of their user: true makes it one,
// after the user's others, false not one. A member absent keeps what its user has.
type Implicit map[string]bool

// CreateGroup adds g, after the other groups, makes it an implicit affiliation of its members as implicit has it, and
// returns it as stored.
func (l *Live) CreateGroup(ctx context.Context, g Group, implicit Implicit) (Group, error) {
	var users []User

	err := l.change(func(next *Settings) (string, error) {
		g.List.Entries = next.memberURIs(g.List.Entries)
		next.Groups = append(slices.Clone(next.Groups), g)

		changed := map[int]bool{}
		for id, on := range implicit {
			next.affiliate(id, g.URI, on, changed)
		}

		users = next.usersAt(changed)

		return "groups[" + strconv.Itoa(len(next.Groups)-1) + "]", nil
	}, func() error { return l.store.CreateGroup(ctx, g, users) })

	return g, err
}

// UpdateGroup replaces the group of g's URI with g, members included, which keeps the URI as the group has it, and
// returns it as stored. The group goes from the implicit affiliations of the members it no longer has, and is one of
// its members' as implicit has it.
func (l *Live) UpdateGroup(ctx context.Context, g Group, implicit Implicit) (Group, error) {
	var users []User

	err := l.change(func(next *Settings) (string, error) {
		i := next.GroupIndex(g.URI)
		if i < 0 {
			return "", refused(ErrNotFound, "group not found")
		}

		g.URI = next.Groups[i].URI
		g.List.Entries = next.memberURIs(g.List.Entries)

		changed := map[int]bool{}

		for _, e := range next.Groups[i].List.Entries {
			k := keyOf(e.URI)
			if !slices.ContainsFunc(g.List.Entries, func(f Entry) bool { return keyOf(f.URI) == k }) {
				next.affiliate(e.URI, g.URI, false, changed)
			}
		}

		for id, on := range implicit {
			next.affiliate(id, g.URI, on, changed)
		}

		next.Groups = slices.Clone(next.Groups)
		next.Groups[i] = g
		users = next.usersAt(changed)

		return "groups[" + strconv.Itoa(i) + "]", nil
	}, func() error { return l.store.UpdateGroup(ctx, g, users) })

	return g, err
}

// DeleteGroup deletes the group of URI uri, and removes it from the implicit affiliations of its users.
func (l *Live) DeleteGroup(ctx context.Context, uri string) error {
	var written string

	return l.change(func(next *Settings) (string, error) {
		i := next.GroupIndex(uri)
		if i < 0 {
			return "", refused(ErrNotFound, "group not found")
		}

		written = next.Groups[i].URI

		changed := map[int]bool{}
		for _, u := range next.Users {
			next.affiliate(u.MCPTTUserID, written, false, changed)
		}

		next.Groups = slices.Delete(slices.Clone(next.Groups), i, i+1)

		return "", nil
	}, func() error { return l.store.DeleteGroup(ctx, written) })
}

// PutMember adds e to the members of the group of URI group, after the others, or replaces the member of its MCPTT
// ID where it is; the group is an implicit affiliation of its user if implicit is true, not one if false, as it was
// if nil. It returns the group as stored, and whether e was added.
func (l *Live) PutMember(ctx context.Context, group string, e Entry, implicit *bool) (Group, bool, error) {
	var (
		written string
		stored  Group
		added   bool
		users   []User
	)

	err := l.change(func(next *Settings) (string, error) {
		i := next.GroupIndex(group)
		if i < 0 {
			return "", refused(ErrNotFound, "group not found")
		}

		written = next.Groups[i].URI
		e = next.memberURIs([]Entry{e})[0]

		g := next.Groups[i]
		g.List.Entries = slices.Clone(g.List.Entries)

		k := keyOf(e.URI)

		j := slices.IndexFunc(g.List.Entries, func(f Entry) bool { return k != "" && keyOf(f.URI) == k })
		if added = j < 0; added {
			j = len(g.List.Entries)
			g.List.Entries = append(g.List.Entries, e)
		} else {
			g.List.Entries[j] = e
		}

		next.Groups = slices.Clone(next.Groups)
		next.Groups[i], stored = g, g

		if implicit != nil {
			changed := map[int]bool{}
			next.affiliate(e.URI, written, *implicit, changed)
			users = next.usersAt(changed)
		}

		return fmt.Sprintf("groups[%d].list.entries[%d]", i, j), nil
	}, func() error { return l.store.PutMember(ctx, written, e, users) })

	return stored, added, err
}

// DeleteMember removes the member of MCPTT ID id from the group of URI group, and the group from the implicit
// affiliations of its user, and returns the group as stored. What the removal makes invalid of the group is a
// conflict: the request has no body.
func (l *Live) DeleteMember(ctx context.Context, group, id string) (Group, error) {
	var (
		written, member string
		stored          Group
	)

	err := l.change(func(next *Settings) (string, error) {
		i := next.GroupIndex(group)
		if i < 0 {
			return "", refused(ErrNotFound, "group not found")
		}

		g := next.Groups[i]
		k := keyOf(id)

		j := slices.IndexFunc(g.List.Entries, func(f Entry) bool { return k != "" && keyOf(f.URI) == k })
		if j < 0 {
			return "", refused(ErrNotFound, "member not found")
		}

		written, member = g.URI, g.List.Entries[j].URI
		g.List.Entries = slices.Delete(slices.Clone(g.List.Entries), j, j+1)
		next.Groups = slices.Clone(next.Groups)
		next.Groups[i], stored = g, g
		next.affiliate(member, written, false, map[int]bool{})

		return "", nil
	}, func() error { return l.store.DeleteMember(ctx, written, member) })

	return stored, err
}

// change makes the next settings with edit, which returns the path of the document it changes, validates them, then
// saves and applies them.
func (l *Live) change(edit func(next *Settings) (string, error), save func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	next := l.current.Load().written

	doc, err := edit(&next)
	if err != nil {
		return err
	}

	snap, err := New(next)
	if err != nil {
		return refusal(next, doc, err)
	}

	if err := save(); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	l.current.Store(&state{written: next, snap: snap})

	if l.onChange != nil {
		l.onChange()
	}

	return nil
}

// refusal is the ChangeError of the errors of New of next, of a change of the document at path doc: its errors at
// their path within it, and those of the documents it conflicts with.
func refusal(next Settings, doc string, err error) error {
	var errs []error
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	} else {
		errs = []error{err}
	}

	ce := &ChangeError{Kind: ErrConflict}

	for _, e := range errs {
		var fe *FieldError
		if !errors.As(e, &fe) {
			return err
		}

		p := *fe

		switch rel, ok := within(fe.Path, doc); {
		case ok:
			p.Path = rel

			if !fe.Conflict {
				ce.Kind = ErrInvalid
			}
		default:
			p.Of, p.Path = next.describe(fe.Path)
			p.Conflict = true
		}

		ce.Problems = append(ce.Problems, p)
	}

	return ce
}

// within is path relative to the document at doc, and whether it is of it.
func within(path, doc string) (string, bool) {
	switch {
	case doc == "":
		return "", false
	case path == doc:
		return "", true
	case strings.HasPrefix(path, doc+"."):
		return path[len(doc)+1:], true
	}

	return "", false
}

// describe is what the document of path is, such as the user sip:a@example.com, and path within it.
func (s Settings) describe(path string) (string, string) {
	for _, d := range []struct {
		prefix string
		of     func(int) string
	}{
		{"users[", func(i int) string { return "the user " + s.Users[i].MCPTTUserID }},
		{"groups[", func(i int) string { return "the group " + s.Groups[i].URI }},
	} {
		rest, ok := strings.CutPrefix(path, d.prefix)
		if !ok {
			continue
		}

		n, rest, ok := strings.Cut(rest, "]")
		if i, err := strconv.Atoi(n); ok && err == nil {
			return d.of(i), strings.TrimPrefix(rest, ".")
		}
	}

	for _, d := range []struct{ prefix, of string }{
		{"system", "the system"},
		{"service-configuration", "the service configuration"},
	} {
		if rest, ok := strings.CutPrefix(path, d.prefix); ok {
			return d.of, strings.TrimPrefix(rest, ".")
		}
	}

	return "", path
}

// UserIndex is the index of the user of MCPTT ID id, of its identity as Key has it; -1 if none.
func (s Settings) UserIndex(id string) int {
	k := keyOf(id)

	return slices.IndexFunc(s.Users, func(u User) bool { return k != "" && keyOf(u.MCPTTUserID) == k })
}

// GroupIndex is the index of the group of URI uri, of its identity as Key has it; -1 if none.
func (s Settings) GroupIndex(uri string) int {
	k := keyOf(uri)

	return slices.IndexFunc(s.Groups, func(g Group) bool { return k != "" && keyOf(g.URI) == k })
}

// affiliate makes the group of URI group an implicit affiliation of the user of MCPTT ID id, after its others, if on,
// or not one; and marks the user's index changed if it changes. Of no such user, it does nothing: a member of no
// user is an error of its group.
func (s *Settings) affiliate(id, group string, on bool, changed map[int]bool) {
	i := s.UserIndex(id)
	if i < 0 {
		return
	}

	u := s.Users[i]
	k := keyOf(group)

	switch j := slices.IndexFunc(u.ImplicitAffiliations, func(a string) bool { return keyOf(a) == k }); {
	case on && j < 0:
		u.ImplicitAffiliations = append(slices.Clone(u.ImplicitAffiliations), group)
	case !on && j >= 0:
		u.ImplicitAffiliations = slices.Delete(slices.Clone(u.ImplicitAffiliations), j, j+1)
	default:
		return
	}

	// The users of the settings before are shared: cloned once, on the first change.
	if len(changed) == 0 {
		s.Users = slices.Clone(s.Users)
	}

	s.Users[i] = u
	changed[i] = true
}

// usersAt are the users of the indexes changed, in order.
func (s Settings) usersAt(changed map[int]bool) []User {
	var users []User

	for i := range s.Users {
		if changed[i] {
			users = append(users, s.Users[i])
		}
	}

	return users
}

// memberURIs are entries, each of the MCPTT ID as its user has it, as the store has them.
func (s Settings) memberURIs(entries []Entry) []Entry {
	entries = slices.Clone(entries)
	if len(entries) == 0 {
		return entries
	}

	users := make(map[string]string, len(s.Users))
	for _, u := range s.Users {
		users[keyOf(u.MCPTTUserID)] = u.MCPTTUserID
	}

	for i := range entries {
		if id, ok := users[keyOf(entries[i].URI)]; ok {
			entries[i].URI = id
		}
	}

	return entries
}

// groupURIs are uris, each of the URI as its group has it, as the store has them.
func (s Settings) groupURIs(uris []string) []string {
	uris = slices.Clone(uris)

	for i := range uris {
		if j := s.GroupIndex(uris[i]); j >= 0 {
			uris[i] = s.Groups[j].URI
		}
	}

	return uris
}

// keyOf is the key of the identity of the URI s, "" if s is not one.
func keyOf(s string) string {
	u, err := sip.ParseURI(s)
	if err != nil {
		return ""
	}

	return key(u)
}
