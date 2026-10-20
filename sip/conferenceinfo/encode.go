package conferenceinfo

import (
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/sip/xmltree"
	"github.com/ellanetworks/ims/sip/xsd"
)

// Encode encodes i, in the order of the schema (§6): the elements of Other after those decoded, where the schema's
// extension point is. Keys are to be unique among their siblings (§4.5), as Decode requires.
func Encode(i Info) ([]byte, error) {
	root, err := i.element("conference-info")
	if err != nil {
		return nil, fmt.Errorf("conferenceinfo: %w", err)
	}

	root.Attr = append(root.Attr, attr("version", strconv.FormatUint(uint64(i.Version), 10)))

	b, err := xmltree.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("conferenceinfo: %w", err)
	}

	return b, nil
}

func (c *Conference) element(local string) (*xmltree.Element, error) {
	if xsd.Collapse(c.Entity) == "" {
		return nil, errors.New("conference without entity")
	}

	e := &xmltree.Element{Name: name(local), Attr: []xml.Attr{attr("entity", c.Entity)}}
	if err := setState(e, c.State, c.Attr); err != nil {
		return nil, err
	}

	if c.Description != nil {
		x, err := c.Description.element()
		if err != nil {
			return nil, err
		}

		e.Children = append(e.Children, x)
	}

	if c.Host != nil {
		x, err := c.Host.element()
		if err != nil {
			return nil, err
		}

		e.Children = append(e.Children, x)
	}

	if s := c.ConferenceState; s != nil {
		x := &xmltree.Element{Name: name("conference-state")}
		if err := setState(x, "", s.Attr); err != nil {
			return nil, err
		}

		x.Children = appendUnsigned(x.Children, "user-count", s.UserCount)
		x.Children = appendBool(x.Children, "active", s.Active)
		x.Children = appendBool(x.Children, "locked", s.Locked)
		x.Children = appendElements(x.Children, s.Other)
		e.Children = append(e.Children, x)
	}

	if c.Users != nil {
		x, err := c.Users.element()
		if err != nil {
			return nil, err
		}

		e.Children = append(e.Children, x)
	}

	if c.SidebarsByRef != nil {
		x, err := c.SidebarsByRef.element("sidebars-by-ref")
		if err != nil {
			return nil, err
		}

		e.Children = append(e.Children, x)
	}

	if sb := c.SidebarsByVal; sb != nil {
		x := &xmltree.Element{Name: name("sidebars-by-val")}
		if err := setState(x, sb.State, sb.Attr); err != nil {
			return nil, err
		}

		var keys []string

		for _, entry := range sb.Entries {
			if slices.Contains(keys, entry.Entity) {
				return nil, fmt.Errorf("sidebar %q twice", entry.Entity)
			}

			keys = append(keys, entry.Entity)

			y, err := entry.element("entry")
			if err != nil {
				return nil, err
			}

			x.Children = append(x.Children, y)
		}

		x.Children = appendElements(x.Children, sb.Other)
		e.Children = append(e.Children, x)
	}

	e.Children = appendElements(e.Children, c.Other)

	return e, nil
}

func (d *Description) element() (*xmltree.Element, error) {
	e := &xmltree.Element{Name: name("conference-description")}
	if err := setState(e, "", d.Attr); err != nil {
		return nil, err
	}

	e.Children = appendText(e.Children, "display-text", d.DisplayText)
	e.Children = appendText(e.Children, "subject", d.Subject)
	e.Children = appendText(e.Children, "free-text", d.FreeText)
	e.Children = appendList(e.Children, "keywords", d.Keywords)

	for _, u := range []struct {
		local string
		uris  *URIs
	}{{"conf-uris", d.ConfURIs}, {"service-uris", d.ServiceURIs}} {
		if u.uris != nil {
			x, err := u.uris.element(u.local)
			if err != nil {
				return nil, err
			}

			e.Children = append(e.Children, x)
		}
	}

	e.Children = appendUnsigned(e.Children, "maximum-user-count", d.MaximumUserCount)

	if a := d.AvailableMedia; a != nil {
		if len(a.Entries) == 0 {
			return nil, errors.New("available-media without entry")
		}

		x := &xmltree.Element{Name: name("available-media")}
		if err := setState(x, "", a.Attr); err != nil {
			return nil, err
		}

		var labels []string

		for _, m := range a.Entries {
			if slices.Contains(labels, m.Label) {
				return nil, fmt.Errorf("available-media label %q twice", m.Label)
			}

			labels = append(labels, m.Label)

			if m.Status != "" && !slices.Contains(mediaStatuses, m.Status) {
				return nil, fmt.Errorf("available-media %q: status %q", m.Label, m.Status)
			}

			y := &xmltree.Element{Name: name("entry"), Attr: []xml.Attr{attr("label", m.Label)}}
			if err := setState(y, "", m.Attr); err != nil {
				return nil, err
			}

			y.Children = appendText(y.Children, "display-text", m.DisplayText)
			y.Children = append(y.Children, xmltree.New(Namespace, "type", text(m.Type)...))
			y.Children = appendText(y.Children, "status", string(m.Status))
			y.Children = appendElements(y.Children, m.Other)
			x.Children = append(x.Children, y)
		}

		x.Children = appendElements(x.Children, a.Other)
		e.Children = append(e.Children, x)
	}

	e.Children = appendElements(e.Children, d.Other)

	return e, nil
}

func (u *URIs) element(local string) (*xmltree.Element, error) {
	if len(u.Entries) == 0 && u.State != Deleted {
		return nil, fmt.Errorf("%s without entry", local)
	}

	e := &xmltree.Element{Name: name(local)}
	if err := setState(e, u.State, u.Attr); err != nil {
		return nil, err
	}

	var keys []string

	for _, entry := range u.Entries {
		if slices.Contains(keys, entry.URI) {
			return nil, fmt.Errorf("%s: uri %q twice", local, entry.URI)
		}

		keys = append(keys, entry.URI)

		x := &xmltree.Element{Name: name("entry")}
		if err := setState(x, "", entry.Attr); err != nil {
			return nil, err
		}

		x.Children = append(x.Children, xmltree.New(Namespace, "uri", text(entry.URI)...))
		x.Children = appendText(x.Children, "display-text", entry.DisplayText)
		x.Children = appendText(x.Children, "purpose", string(entry.Purpose))

		var err error
		if x.Children, err = appendExecution(x.Children, "modified", entry.Modified); err != nil {
			return nil, err
		}

		x.Children = appendElements(x.Children, entry.Other)
		e.Children = append(e.Children, x)
	}

	e.Children = appendElements(e.Children, u.Other)

	return e, nil
}

func (h *Host) element() (*xmltree.Element, error) {
	e := &xmltree.Element{Name: name("host-info")}
	if err := setState(e, "", h.Attr); err != nil {
		return nil, err
	}

	e.Children = appendText(e.Children, "display-text", h.DisplayText)
	e.Children = appendText(e.Children, "web-page", h.WebPage)

	if h.URIs != nil {
		x, err := h.URIs.element("uris")
		if err != nil {
			return nil, err
		}

		e.Children = append(e.Children, x)
	}

	e.Children = appendElements(e.Children, h.Other)

	return e, nil
}

func (u *Users) element() (*xmltree.Element, error) {
	e := &xmltree.Element{Name: name("users")}
	if err := setState(e, u.State, u.Attr); err != nil {
		return nil, err
	}

	var keys []string

	for _, user := range u.Users {
		if slices.Contains(keys, user.Entity) {
			return nil, fmt.Errorf("user %q twice", user.Entity)
		}

		keys = append(keys, user.Entity)

		x, err := user.element()
		if err != nil {
			return nil, err
		}

		e.Children = append(e.Children, x)
	}

	e.Children = appendElements(e.Children, u.Other)

	return e, nil
}

func (u *User) element() (*xmltree.Element, error) {
	e := &xmltree.Element{Name: name("user"), Attr: []xml.Attr{attr("entity", u.Entity)}}
	if err := setState(e, u.State, u.Attr); err != nil {
		return nil, err
	}

	e.Children = appendText(e.Children, "display-text", u.DisplayText)

	if u.AssociatedAORs != nil {
		x, err := u.AssociatedAORs.element("associated-aors")
		if err != nil {
			return nil, err
		}

		e.Children = append(e.Children, x)
	}

	if r := u.Roles; r != nil {
		if len(r.Entries) == 0 {
			return nil, fmt.Errorf("user %q: roles without entry", u.Entity)
		}

		x := &xmltree.Element{Name: name("roles")}
		if err := setState(x, "", r.Attr); err != nil {
			return nil, err
		}

		for _, entry := range r.Entries {
			x.Children = append(x.Children, xmltree.New(Namespace, "entry", text(entry)...))
		}

		e.Children = append(e.Children, x)
	}

	e.Children = appendList(e.Children, "languages", u.Languages)
	e.Children = appendText(e.Children, "cascaded-focus", u.CascadedFocus)

	var keys []string

	for _, ep := range u.Endpoints {
		if slices.Contains(keys, ep.Entity) {
			return nil, fmt.Errorf("user %q: endpoint %q twice", u.Entity, ep.Entity)
		}

		keys = append(keys, ep.Entity)

		x, err := ep.element()
		if err != nil {
			return nil, fmt.Errorf("user %q: %w", u.Entity, err)
		}

		e.Children = append(e.Children, x)
	}

	e.Children = appendElements(e.Children, u.Other)

	return e, nil
}

func (ep *Endpoint) element() (*xmltree.Element, error) {
	e := &xmltree.Element{Name: name("endpoint"), Attr: []xml.Attr{attr("entity", ep.Entity)}}
	if err := setState(e, ep.State, ep.Attr); err != nil {
		return nil, err
	}

	if ep.Status != "" && !slices.Contains(endpointStatuses, ep.Status) ||
		ep.JoiningMethod != "" && !slices.Contains([]JoiningMethod{DialedIn, DialedOut, FocusOwner}, ep.JoiningMethod) ||
		ep.DisconnectionMethod != "" && !slices.Contains([]DisconnectionMethod{Departed, Booted, Failed, Busy}, ep.DisconnectionMethod) {
		return nil, fmt.Errorf("endpoint %q: status %q, joining method %q, disconnection method %q", ep.Entity, ep.Status,
			ep.JoiningMethod, ep.DisconnectionMethod)
	}

	e.Children = appendText(e.Children, "display-text", ep.DisplayText)

	var err error

	// The values and executions in the order of the schema.
	for _, x := range []struct {
		local, value string
		execution    *Execution
	}{
		{"referred", "", ep.Referred},
		{"status", string(ep.Status), nil},
		{"joining-method", string(ep.JoiningMethod), nil},
		{"joining-info", "", ep.JoiningInfo},
		{"disconnection-method", string(ep.DisconnectionMethod), nil},
		{"disconnection-info", "", ep.DisconnectionInfo},
	} {
		e.Children = appendText(e.Children, x.local, x.value)
		if e.Children, err = appendExecution(e.Children, x.local, x.execution); err != nil {
			return nil, err
		}
	}

	var ids []string

	for _, m := range ep.Media {
		if slices.Contains(ids, m.ID) {
			return nil, fmt.Errorf("endpoint %q: media %q twice", ep.Entity, m.ID)
		}

		ids = append(ids, m.ID)

		if m.Status != "" && !slices.Contains(mediaStatuses, m.Status) {
			return nil, fmt.Errorf("endpoint %q: media %q: status %q", ep.Entity, m.ID, m.Status)
		}

		x := &xmltree.Element{Name: name("media"), Attr: []xml.Attr{attr("id", m.ID)}}
		if err := setState(x, "", m.Attr); err != nil {
			return nil, err
		}

		x.Children = appendText(x.Children, "display-text", m.DisplayText)
		x.Children = appendText(x.Children, "type", m.Type)
		x.Children = appendText(x.Children, "label", m.Label)
		x.Children = appendText(x.Children, "src-id", m.SrcID)
		x.Children = appendText(x.Children, "status", string(m.Status))
		x.Children = appendElements(x.Children, m.Other)
		e.Children = append(e.Children, x)
	}

	if c := ep.CallInfo; c != nil {
		x := &xmltree.Element{Name: name("call-info")}
		if err := setState(x, "", c.Attr); err != nil {
			return nil, err
		}

		if s := c.SIP; s != nil {
			y := &xmltree.Element{Name: name("sip")}
			if err := setState(y, "", s.Attr); err != nil {
				return nil, err
			}

			y.Children = appendText(y.Children, "display-text", s.DisplayText)
			y.Children = append(y.Children, xmltree.New(Namespace, "call-id", text(s.CallID)...),
				xmltree.New(Namespace, "from-tag", text(s.FromTag)...), xmltree.New(Namespace, "to-tag", text(s.ToTag)...))
			y.Children = appendElements(y.Children, s.Other)
			x.Children = append(x.Children, y)
		}

		x.Children = appendElements(x.Children, c.Other)
		e.Children = append(e.Children, x)
	}

	e.Children = appendElements(e.Children, ep.Other)

	return e, nil
}

// setState sets the state s of e, if any, and the extension attributes as: of a namespace, other than
// conference-info's.
func setState(e *xmltree.Element, s State, as []xml.Attr) error {
	switch s {
	case "":
	case Full, Partial, Deleted:
		e.Attr = append(e.Attr, attr("state", string(s)))
	default:
		return fmt.Errorf("<%s> of state %q", e.Name.Local, s)
	}

	for _, a := range as {
		if a.Name.Space == "" || a.Name.Space == Namespace {
			return fmt.Errorf("<%s>: extension attribute {%s}%s", e.Name.Local, a.Name.Space, a.Name.Local)
		}
	}

	e.Attr = append(e.Attr, as...)

	return nil
}

func attr(local, value string) xml.Attr { return xml.Attr{Name: xml.Name{Local: local}, Value: value} }

// text is the content of an element of the string s: none for an empty one.
func text(s string) []xmltree.Node {
	if s == "" {
		return nil
	}

	return []xmltree.Node{xmltree.Text(s)}
}

func appendText(ns []xmltree.Node, local, s string) []xmltree.Node {
	if s == "" {
		return ns
	}

	return append(ns, xmltree.New(Namespace, local, xmltree.Text(s)))
}

func appendList(ns []xmltree.Node, local string, items []string) []xmltree.Node {
	if len(items) == 0 {
		return ns
	}

	return append(ns, xmltree.New(Namespace, local, xmltree.Text(strings.Join(items, " "))))
}

func appendUnsigned(ns []xmltree.Node, local string, n *uint32) []xmltree.Node {
	if n == nil {
		return ns
	}

	return append(ns, xmltree.New(Namespace, local, xmltree.Text(strconv.FormatUint(uint64(*n), 10))))
}

func appendBool(ns []xmltree.Node, local string, b *bool) []xmltree.Node {
	if b == nil {
		return ns
	}

	return append(ns, xmltree.New(Namespace, local, xmltree.Text(xsd.FormatBoolean(*b))))
}

func appendExecution(ns []xmltree.Node, local string, x *Execution) ([]xmltree.Node, error) {
	if x == nil {
		return ns, nil
	}

	e := xmltree.New(Namespace, local)
	if err := setState(e, "", x.Attr); err != nil {
		return nil, err
	}

	if x.When != nil {
		when := x.When.String()
		if when == "" {
			return nil, fmt.Errorf("<%s>: when of the year %d", local, x.When.Year())
		}

		e.Children = append(e.Children, xmltree.New(Namespace, "when", xmltree.Text(when)))
	}

	e.Children = appendText(e.Children, "reason", x.Reason)
	e.Children = appendText(e.Children, "by", x.By)

	return append(ns, e), nil
}

func appendElements(ns []xmltree.Node, es []*xmltree.Element) []xmltree.Node {
	for _, e := range es {
		ns = append(ns, e)
	}

	return ns
}
