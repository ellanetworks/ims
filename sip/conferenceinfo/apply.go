package conferenceinfo

import (
	"encoding/xml"
	"errors"
	"fmt"
	"slices"

	"github.com/ellanetworks/ims/sip/xmltree"
)

var (
	// RFC 4575 section 4.6
	ErrStale = errors.New("conferenceinfo: version not newer than the local one")
	// RFC 4575 section 4.6
	ErrVersionGap = errors.New("conferenceinfo: partial notification out of sequence")
	// RFC 4575 section 4.6
	ErrIncoherent = errors.New("conferenceinfo: partial notification not applicable")
	// RFC 4575 section 5.2
	ErrEntity = errors.New("conferenceinfo: notification of another conference")
)

// RFC 4575 section 4.6
func (i *Info) Apply(n Info) error {
	switch {
	case i.Entity == "" && n.State == Partial:
		return ErrVersionGap
	case i.Entity == "":
	case n.Version <= i.Version:
		return ErrStale
	case n.Entity != i.Entity:
		return fmt.Errorf("%w: %q, not %q", ErrEntity, n.Entity, i.Entity)
	case n.State != Partial:
	case n.Version != i.Version+1:
		return ErrVersionGap
	case i.State == Deleted:
		return fmt.Errorf("%w: after a deleted notification", ErrIncoherent)
	}

	switch n.State {
	case Deleted:
		*i = Info{Version: n.Version, Conference: Conference{Entity: n.Entity, State: Deleted, Attr: n.Attr}}
		return nil
	case Partial:
	default:
		*i = n
		return nil
	}

	var m merger

	c := i.Conference
	c.merge(&m, &n.Conference)

	if m.err != nil {
		return m.err
	}

	i.Version, i.Conference = n.Version, c

	return nil
}

type merger struct {
	err error
}

func (c *Conference) merge(m *merger, n *Conference) {
	c.Entity = n.Entity
	c.State = ""
	c.Description = replace(c.Description, n.Description)
	c.Host = replace(c.Host, n.Host)
	c.ConferenceState = replace(c.ConferenceState, n.ConferenceState)
	c.Users = mergeContainer(c.Users, n.Users, func(u *Users) State { return u.State },
		func(l, n *Users) { l.merge(m, n) })
	c.SidebarsByRef = mergeContainer(c.SidebarsByRef, n.SidebarsByRef, func(u *URIs) State { return u.State },
		func(l, n *URIs) { l.merge(m, n) })
	c.SidebarsByVal = mergeContainer(c.SidebarsByVal, n.SidebarsByVal, func(s *Sidebars) State { return s.State },
		func(l, n *Sidebars) { l.merge(m, n) })
	c.Attr = mergeAttr(c.Attr, n.Attr)
	c.Other = m.other(c.Other, n.Other)
}

func (u *Users) merge(m *merger, n *Users) {
	u.Users = mergeKeyed(u.Users, n.Users, func(x *User) string { return x.Entity }, func(x *User) State { return x.State },
		func(l, n *User) { l.merge(m, n) })
	u.Attr = mergeAttr(u.Attr, n.Attr)
	u.Other = m.other(u.Other, n.Other)
}

func (u *User) merge(m *merger, n *User) {
	u.Entity = n.Entity
	u.State = ""
	u.DisplayText = replace(u.DisplayText, n.DisplayText)
	u.AssociatedAORs = mergeContainer(u.AssociatedAORs, n.AssociatedAORs, func(x *URIs) State { return x.State },
		func(l, n *URIs) { l.merge(m, n) })
	u.Roles = replace(u.Roles, n.Roles)
	u.Languages = replaceList(u.Languages, n.Languages)
	u.CascadedFocus = replace(u.CascadedFocus, n.CascadedFocus)
	u.Endpoints = mergeKeyed(u.Endpoints, n.Endpoints, func(x *Endpoint) string { return x.Entity },
		func(x *Endpoint) State { return x.State }, func(l, n *Endpoint) { l.merge(m, n) })
	u.Attr = mergeAttr(u.Attr, n.Attr)
	u.Other = m.other(u.Other, n.Other)
}

func (ep *Endpoint) merge(m *merger, n *Endpoint) {
	ep.Entity = n.Entity
	ep.State = ""
	ep.DisplayText = replace(ep.DisplayText, n.DisplayText)
	ep.Referred = replace(ep.Referred, n.Referred)
	ep.Status = replace(ep.Status, n.Status)
	ep.JoiningMethod = replace(ep.JoiningMethod, n.JoiningMethod)
	ep.JoiningInfo = replace(ep.JoiningInfo, n.JoiningInfo)
	ep.DisconnectionMethod = replace(ep.DisconnectionMethod, n.DisconnectionMethod)
	ep.DisconnectionInfo = replace(ep.DisconnectionInfo, n.DisconnectionInfo)
	ep.Media = mergeKeyed(ep.Media, n.Media, func(x *Media) string { return x.ID }, func(*Media) State { return Partial },
		func(l, n *Media) { l.merge(m, n) })
	ep.CallInfo = replace(ep.CallInfo, n.CallInfo)
	ep.Attr = mergeAttr(ep.Attr, n.Attr)
	ep.Other = m.other(ep.Other, n.Other)
}

func (md *Media) merge(m *merger, n *Media) {
	md.ID = n.ID
	md.DisplayText = replace(md.DisplayText, n.DisplayText)
	md.Type = replace(md.Type, n.Type)
	md.Label = replace(md.Label, n.Label)
	md.SrcID = replace(md.SrcID, n.SrcID)
	md.Status = replace(md.Status, n.Status)
	md.Attr = mergeAttr(md.Attr, n.Attr)
	md.Other = m.other(md.Other, n.Other)
}

func (u *URIs) merge(m *merger, n *URIs) {
	u.Entries = mergeKeyed(u.Entries, n.Entries, func(x *URI) string { return x.URI }, func(*URI) State { return Partial },
		func(l, n *URI) { l.merge(m, n) })
	u.Attr = mergeAttr(u.Attr, n.Attr)
	u.Other = m.other(u.Other, n.Other)
}

func (u *URI) merge(m *merger, n *URI) {
	u.URI = n.URI
	u.DisplayText = replace(u.DisplayText, n.DisplayText)
	u.Purpose = replace(u.Purpose, n.Purpose)
	u.Modified = replace(u.Modified, n.Modified)
	u.Attr = mergeAttr(u.Attr, n.Attr)
	u.Other = m.other(u.Other, n.Other)
}

func (s *Sidebars) merge(m *merger, n *Sidebars) {
	s.Entries = mergeKeyed(s.Entries, n.Entries, func(x *Conference) string { return x.Entity },
		func(x *Conference) State { return x.State }, func(l, n *Conference) { l.merge(m, n) })
	s.Attr = mergeAttr(s.Attr, n.Attr)
	s.Other = m.other(s.Other, n.Other)
}

// RFC 4575 section 4.6
func replace[T comparable](l, n T) T {
	var zero T
	if n == zero {
		return l
	}

	return n
}

func replaceList(l, n []string) []string {
	if n == nil {
		return l
	}

	return n
}

func mergeContainer[T any](l, n *T, state func(*T) State, merge func(*T, *T)) *T {
	switch {
	case n == nil:
		return l
	case state(n) == Deleted:
		return nil
	case state(n) != Partial:
		return n
	}

	var c T
	if l != nil {
		c = *l
	}

	merge(&c, n)

	return &c
}

// RFC 4575 section 4.6
func mergeKeyed[T any](l, n []T, key func(*T) string, state func(*T) State, merge func(*T, *T)) []T {
	l = slices.Clone(l)

	for k := range n {
		x := &n[k]
		j := slices.IndexFunc(l, func(o T) bool { return key(&o) == key(x) })

		switch s := state(x); {
		case s == Deleted:
			if j >= 0 {
				l = slices.Delete(l, j, j+1)
			}
		case s != Partial && j >= 0:
			l[j] = *x
		case s != Partial:
			l = append(l, *x)
		case j >= 0:
			merge(&l[j], x)
		default:
			var row T

			merge(&row, x)
			l = append(l, row)
		}
	}

	return l
}

// RFC 4575 section 4.4
func mergeAttr(l, n []xml.Attr) []xml.Attr {
	if len(n) == 0 {
		return l
	}

	l = slices.DeleteFunc(slices.Clone(l), func(a xml.Attr) bool {
		return slices.ContainsFunc(n, func(o xml.Attr) bool { return o.Name == a.Name })
	})

	return append(l, n...)
}

// RFC 4575 section 4.4
func (m *merger) other(l, n []*xmltree.Element) []*xmltree.Element {
	for _, e := range n {
		if e.Name.Space == Namespace && m.err == nil {
			m.err = fmt.Errorf("%w: <%s> not decoded", ErrIncoherent, e.Name.Local)
		}
	}

	if len(n) == 0 {
		return l
	}

	l = slices.DeleteFunc(slices.Clone(l), func(e *xmltree.Element) bool {
		return slices.ContainsFunc(n, func(o *xmltree.Element) bool { return o.Name == e.Name })
	})

	return append(l, n...)
}
