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

	"github.com/ellanetworks/ims/internal/deepequal"
	"github.com/ellanetworks/ims/sip"
)

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

type Live struct {
	store    Store
	onChange func(ctx context.Context, before, after *Snapshot)

	mu      sync.Mutex
	stateMu sync.Mutex
	current atomic.Pointer[state]
}

type state struct {
	written Settings
	snap    *Snapshot
}

func NewLive(store Store, initial Settings) (*Live, error) {
	snap, err := New(initial)
	if err != nil {
		return nil, err
	}

	l := &Live{store: store}
	l.current.Store(&state{written: initial, snap: snap})

	return l, nil
}

func (l *Live) OnChange(f func(ctx context.Context, before, after *Snapshot)) { l.onChange = f }

func (l *Live) Current() *Snapshot { return l.current.Load().snap }

func (l *Live) Get() Settings { return l.current.Load().written }

func (l *Live) UpdateSystem(ctx context.Context, s System) error {
	return l.change(ctx, func(next *Settings) (string, error) {
		next.System = s
		return "system", nil
	}, func() error { return l.store.UpdateSystem(ctx, s) })
}

func (l *Live) UpdateServiceConfiguration(ctx context.Context, c ServiceConfiguration) error {
	return l.change(ctx, func(next *Settings) (string, error) {
		next.ServiceConfiguration = c
		return "service-configuration", nil
	}, func() error { return l.store.UpdateServiceConfiguration(ctx, c) })
}

func (l *Live) CreateUser(ctx context.Context, u User) (User, error) {
	err := l.change(ctx, func(next *Settings) (string, error) {
		u.ImplicitAffiliations = next.groupURIs(u.ImplicitAffiliations)
		next.Users = append(slices.Clone(next.Users), u)

		return "users[" + strconv.Itoa(len(next.Users)-1) + "]", nil
	}, func() error { return l.store.CreateUser(ctx, u) })

	return u, err
}

func (l *Live) UpdateUser(ctx context.Context, u User) (User, error) {
	err := l.change(ctx, func(next *Settings) (string, error) {
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

func (l *Live) DeleteUser(ctx context.Context, id string) error {
	var written string

	return l.change(ctx, func(next *Settings) (string, error) {
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

type Implicit map[string]bool

func (l *Live) CreateGroup(ctx context.Context, g Group, implicit Implicit) (Group, error) {
	var users []User

	err := l.change(ctx, func(next *Settings) (string, error) {
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

func (l *Live) UpdateGroup(ctx context.Context, g Group, implicit Implicit) (Group, error) {
	var users []User

	err := l.change(ctx, func(next *Settings) (string, error) {
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

func (l *Live) DeleteGroup(ctx context.Context, uri string) error {
	var written string

	return l.change(ctx, func(next *Settings) (string, error) {
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

func (l *Live) PutMember(ctx context.Context, group string, e Entry, implicit *bool) (Group, bool, error) {
	var (
		written string
		stored  Group
		added   bool
		users   []User
	)

	err := l.change(ctx, func(next *Settings) (string, error) {
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

func (l *Live) DeleteMember(ctx context.Context, group, id string) (Group, error) {
	var (
		written, member string
		stored          Group
	)

	err := l.change(ctx, func(next *Settings) (string, error) {
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

func (l *Live) change(ctx context.Context, edit func(next *Settings) (string, error), save func() error) error {
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

	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	l.swapLocked(context.WithoutCancel(ctx), next, snap)

	return nil
}

func (l *Live) Reload(ctx context.Context, next Settings) error {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if deepequal.IgnoringEmpty(l.current.Load().written, next) {
		return nil
	}

	snap, err := New(next)
	if err != nil {
		return err
	}

	l.swapLocked(ctx, next, snap)

	return nil
}

func (l *Live) swapLocked(ctx context.Context, next Settings, snap *Snapshot) {
	before := l.current.Swap(&state{written: next, snap: snap}).snap

	if l.onChange != nil {
		l.onChange(ctx, before, snap)
	}
}

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

func (s Settings) UserIndex(id string) int {
	k := keyOf(id)

	return slices.IndexFunc(s.Users, func(u User) bool { return k != "" && keyOf(u.MCPTTUserID) == k })
}

func (s Settings) GroupIndex(uri string) int {
	k := keyOf(uri)

	return slices.IndexFunc(s.Groups, func(g Group) bool { return k != "" && keyOf(g.URI) == k })
}

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

	if len(changed) == 0 {
		s.Users = slices.Clone(s.Users)
	}

	s.Users[i] = u
	changed[i] = true
}

func (s Settings) usersAt(changed map[int]bool) []User {
	var users []User

	for i := range s.Users {
		if changed[i] {
			users = append(users, s.Users[i])
		}
	}

	return users
}

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

func (s Settings) groupURIs(uris []string) []string {
	uris = slices.Clone(uris)

	for i := range uris {
		if j := s.GroupIndex(uris[i]); j >= 0 {
			uris[i] = s.Groups[j].URI
		}
	}

	return uris
}

func keyOf(s string) string {
	u, err := sip.ParseURI(s)
	if err != nil {
		return ""
	}

	return key(u)
}
