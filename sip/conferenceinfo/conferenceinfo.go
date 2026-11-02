package conferenceinfo

import (
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ellanetworks/ims/sip/xmltree"
	"github.com/ellanetworks/ims/sip/xsd"
)

const (
	ContentType = "application/conference-info+xml"
	Namespace   = "urn:ietf:params:xml:ns:conference-info"
)

const maxSize = 1 << 20

// RFC 4575 section 5.2
type Info struct {
	Version uint32
	Conference
}

// RFC 4575 section 5.2
type Conference struct {
	Entity          string
	State           State
	Description     *Description
	Host            *Host
	ConferenceState *ConferenceState
	Users           *Users
	SidebarsByRef   *URIs
	SidebarsByVal   *Sidebars
	Attr            []xml.Attr
	Other           []*xmltree.Element
}

// RFC 4575 section 4.4
type State string

const (
	Full    State = "full"
	Partial State = "partial"
	Deleted State = "deleted"
)

// RFC 4575 section 5.3
type Description struct {
	DisplayText      string
	Subject          string
	FreeText         string
	Keywords         []string
	ConfURIs         *URIs
	ServiceURIs      *URIs
	MaximumUserCount *uint32
	AvailableMedia   *AvailableMedia
	Attr             []xml.Attr
	Other            []*xmltree.Element
}

// RFC 4575 section 5.3.1
type URIs struct {
	State   State
	Entries []URI
	Attr    []xml.Attr
	Other   []*xmltree.Element
}

type URI struct {
	URI         string
	DisplayText string
	Purpose     Purpose
	Modified    *Execution
	Attr        []xml.Attr
	Other       []*xmltree.Element
}

// RFC 4575 section 5.3.1
// RFC 4575 section 5.3.2
type Purpose string

const (
	Participation Purpose = "participation"
	Streaming     Purpose = "streaming"
	WebPage       Purpose = "web-page"
	Recording     Purpose = "recording"
	Event         Purpose = "event"
)

// RFC 4575 section 5.3.4
type AvailableMedia struct {
	Entries []Medium
	Attr    []xml.Attr
	Other   []*xmltree.Element
}

type Medium struct {
	Label       string
	DisplayText string
	Type        string
	Status      MediaStatus
	Attr        []xml.Attr
	Other       []*xmltree.Element
}

// RFC 4575 section 5.8.5
type MediaStatus string

const (
	RecvOnly MediaStatus = "recvonly"
	SendOnly MediaStatus = "sendonly"
	SendRecv MediaStatus = "sendrecv"
	Inactive MediaStatus = "inactive"
)

// RFC 4575 section 5.4
type Host struct {
	DisplayText string
	WebPage     string
	URIs        *URIs
	Attr        []xml.Attr
	Other       []*xmltree.Element
}

// RFC 4575 section 5.5
type ConferenceState struct {
	UserCount *uint32
	Active    *bool
	Locked    *bool
	Attr      []xml.Attr
	Other     []*xmltree.Element
}

// RFC 4575 section 5.6
type Users struct {
	State State
	Users []User
	Attr  []xml.Attr
	Other []*xmltree.Element
}

// RFC 4575 section 5.6
type User struct {
	Entity         string
	State          State
	DisplayText    string
	AssociatedAORs *URIs
	Roles          *Roles
	Languages      []string
	CascadedFocus  string
	Endpoints      []Endpoint
	Attr           []xml.Attr
	Other          []*xmltree.Element
}

// RFC 4575 section 5.6.3
type Roles struct {
	Entries []string
	Attr    []xml.Attr
}

// RFC 4575 section 5.7
type Endpoint struct {
	Entity              string
	State               State
	DisplayText         string
	Referred            *Execution
	Status              EndpointStatus
	JoiningMethod       JoiningMethod
	JoiningInfo         *Execution
	DisconnectionMethod DisconnectionMethod
	DisconnectionInfo   *Execution
	Media               []Media
	CallInfo            *CallInfo
	Attr                []xml.Attr
	Other               []*xmltree.Element
}

// RFC 4575 section 5.7.3
type EndpointStatus string

const (
	Pending       EndpointStatus = "pending"
	DialingOut    EndpointStatus = "dialing-out"
	DialingIn     EndpointStatus = "dialing-in"
	Alerting      EndpointStatus = "alerting"
	OnHold        EndpointStatus = "on-hold"
	Connected     EndpointStatus = "connected"
	MutedViaFocus EndpointStatus = "muted-via-focus"
	Disconnecting EndpointStatus = "disconnecting"
	Disconnected  EndpointStatus = "disconnected"
)

// RFC 4575 section 5.7.4
type JoiningMethod string

const (
	DialedIn   JoiningMethod = "dialed-in"
	DialedOut  JoiningMethod = "dialed-out"
	FocusOwner JoiningMethod = "focus-owner"
)

// RFC 4575 section 5.7.6
type DisconnectionMethod string

const (
	Departed DisconnectionMethod = "departed"
	Booted   DisconnectionMethod = "booted"
	Failed   DisconnectionMethod = "failed"
	Busy     DisconnectionMethod = "busy"
)

// RFC 4575 section 5.7.2
// RFC 4575 section 5.7.5
// RFC 4575 section 5.7.7
type Execution struct {
	When   *xsd.DateTime
	Reason string
	By     string
	Attr   []xml.Attr
}

// RFC 4575 section 5.7.9
type CallInfo struct {
	SIP   *SIPDialog
	Attr  []xml.Attr
	Other []*xmltree.Element
}

type SIPDialog struct {
	DisplayText string
	CallID      string
	FromTag     string
	ToTag       string
	Attr        []xml.Attr
	Other       []*xmltree.Element
}

// RFC 4575 section 5.8
type Media struct {
	ID          string
	DisplayText string
	Type        string
	Label       string
	SrcID       string
	Status      MediaStatus
	Attr        []xml.Attr
	Other       []*xmltree.Element
}

// RFC 4575 section 5.9.2
type Sidebars struct {
	State   State
	Entries []Conference
	Attr    []xml.Attr
	Other   []*xmltree.Element
}

var ErrValue = errors.New("malformed element")

func Decode(b []byte) (Info, error) {
	if len(b) > maxSize {
		return Info{}, fmt.Errorf("conferenceinfo: document of %d bytes exceeds %d", len(b), maxSize)
	}

	root, err := xmltree.Parse(b)
	if err != nil {
		return Info{}, fmt.Errorf("conferenceinfo: %w", err)
	}

	if root.Name != name("conference-info") {
		return Info{}, fmt.Errorf("conferenceinfo: root element <%s> of %q", root.Name.Local, root.Name.Space)
	}

	v, ok := root.AttrValue("", "version")
	if !ok {
		return Info{}, errors.New("conferenceinfo: <conference-info> without version")
	}

	version, ok := xsd.ParseUnsignedInt(v)
	if !ok {
		return Info{}, fmt.Errorf("conferenceinfo: version %q", v)
	}

	if entity, _ := root.AttrValue("", "entity"); xsd.Collapse(entity) == "" {
		return Info{}, errors.New("conferenceinfo: <conference-info> without entity")
	}

	if s, ok := root.AttrValue("", "state"); ok && !slices.Contains([]State{Full, Partial, Deleted}, State(xsd.Collapse(s))) {
		return Info{}, fmt.Errorf("conferenceinfo: <conference-info> of state %q", s)
	}

	var d decoder

	c, _ := d.conference(root)

	return Info{Version: version, Conference: c}, errors.Join(d.errs...)
}

type decoder struct {
	errs []error
}

func (d *decoder) fail(element, format string, a ...any) {
	d.errs = append(d.errs, fmt.Errorf("conferenceinfo: <%s>: %w: %s", element, ErrValue, fmt.Sprintf(format, a...)))
}

func known(e *xmltree.Element) string {
	if e.Name.Space != Namespace {
		return ""
	}

	return e.Name.Local
}

type seen map[string]bool

func (d *decoder) once(s seen, x *xmltree.Element) bool {
	if _, ok := s[x.Name.Local]; ok {
		d.fail(x.Name.Local, "given more than once")
		return false
	}

	s[x.Name.Local] = false

	return true
}

func (s seen) decoded(x *xmltree.Element) {
	if _, ok := s[x.Name.Local]; ok {
		s[x.Name.Local] = true
	}
}

func given[T comparable](s seen, x *xmltree.Element, v T) bool {
	var zero T
	if v == zero {
		delete(s, x.Name.Local)
	}

	return true
}

func (d *decoder) state(e *xmltree.Element) (State, bool) {
	v, ok := e.AttrValue("", "state")
	if !ok {
		return "", true
	}

	if s := State(xsd.Collapse(v)); slices.Contains([]State{Full, Partial, Deleted}, s) {
		return s, true
	}

	d.fail(e.Name.Local, "state %q", v)

	return "", false
}

func (d *decoder) conference(e *xmltree.Element) (Conference, bool) {
	entity, _ := e.AttrValue("", "entity")
	if entity = xsd.Collapse(entity); entity == "" {
		d.fail(e.Name.Local, "without entity")
		return Conference{}, false
	}

	state, ok := d.state(e)
	if !ok {
		return Conference{}, false
	}

	c := Conference{Entity: entity, State: state, Attr: extensions(e)}
	s := seen{}

	for _, x := range e.Elements() {
		n := known(x)
		ok := false

		switch n {
		case "conference-description":
			ok = d.once(s, x) && d.description(x, &c.Description)
		case "host-info":
			ok = d.once(s, x) && d.host(x, &c.Host)
		case "conference-state":
			ok = d.once(s, x) && d.conferenceState(x, &c.ConferenceState)
		case "users":
			ok = d.once(s, x) && d.users(x, &c.Users)
		case "sidebars-by-ref":
			ok = d.once(s, x) && d.uris(x, &c.SidebarsByRef)
		case "sidebars-by-val":
			ok = d.once(s, x) && d.sidebars(x, &c.SidebarsByVal)
		}

		if ok {
			s.decoded(x)
			continue
		}

		c.Other = append(c.Other, x)
	}

	return c, true
}

func (d *decoder) description(e *xmltree.Element, v **Description) bool {
	c := Description{Attr: extensions(e)}

	s := seen{}

	for _, x := range e.Elements() {
		n := known(x)
		ok := false

		switch n {
		case "display-text":
			ok = d.once(s, x) && d.text(x, &c.DisplayText) && given(s, x, c.DisplayText)
		case "subject":
			ok = d.once(s, x) && d.text(x, &c.Subject) && given(s, x, c.Subject)
		case "free-text":
			ok = d.once(s, x) && d.text(x, &c.FreeText) && given(s, x, c.FreeText)
		case "keywords":
			ok = d.once(s, x) && d.list(x, &c.Keywords) && given(s, x, len(c.Keywords))
		case "conf-uris":
			ok = d.once(s, x) && d.uris(x, &c.ConfURIs)
		case "service-uris":
			ok = d.once(s, x) && d.uris(x, &c.ServiceURIs)
		case "maximum-user-count":
			ok = d.once(s, x) && d.unsigned(x, &c.MaximumUserCount)
		case "available-media":
			ok = d.once(s, x) && d.availableMedia(x, &c.AvailableMedia)
		}

		if ok {
			s.decoded(x)
			continue
		}

		c.Other = append(c.Other, x)
	}

	*v = &c

	return true
}

// RFC 4575 section 5.3.1
func (d *decoder) uris(e *xmltree.Element, v **URIs) bool {
	state, ok := d.state(e)
	if !ok {
		return false
	}

	u := URIs{State: state, Attr: extensions(e)}

	for _, x := range e.Elements() {
		if known(x) == "entry" {
			if entry, ok := d.uri(x); ok {
				if !slices.ContainsFunc(u.Entries, func(o URI) bool { return o.URI == entry.URI }) {
					u.Entries = append(u.Entries, entry)
					continue
				}

				d.fail("entry", "uri %q twice", entry.URI)
			}
		}

		u.Other = append(u.Other, x)
	}

	if len(u.Entries) == 0 && u.State != Deleted {
		d.fail(e.Name.Local, "no entry")
		return false
	}

	*v = &u

	return true
}

func (d *decoder) uri(e *xmltree.Element) (URI, bool) {
	u := URI{Attr: extensions(e)}

	s := seen{}

	for _, x := range e.Elements() {
		n := known(x)
		ok := false

		switch n {
		case "uri":
			ok = d.once(s, x) && d.anyURI(x, &u.URI)
		case "display-text":
			ok = d.once(s, x) && d.text(x, &u.DisplayText) && given(s, x, u.DisplayText)
		case "purpose":
			ok = d.once(s, x) && d.text(x, (*string)(&u.Purpose)) && given(s, x, u.Purpose)
		case "modified":
			ok = d.once(s, x) && d.execution(x, &u.Modified)
		}

		if ok {
			s.decoded(x)
			continue
		}

		u.Other = append(u.Other, x)
	}

	if !s["uri"] {
		d.fail("entry", "without uri")
		return URI{}, false
	}

	return u, true
}

// RFC 4575 section 5.3.4
func (d *decoder) availableMedia(e *xmltree.Element, v **AvailableMedia) bool {
	a := AvailableMedia{Attr: extensions(e)}

	for _, x := range e.Elements() {
		if known(x) == "entry" {
			if m, ok := d.medium(x); ok {
				if !slices.ContainsFunc(a.Entries, func(o Medium) bool { return o.Label == m.Label }) {
					a.Entries = append(a.Entries, m)
					continue
				}

				d.fail("entry", "label %q twice", m.Label)
			}
		}

		a.Other = append(a.Other, x)
	}

	if len(a.Entries) == 0 {
		d.fail("available-media", "no entry")
		return false
	}

	*v = &a

	return true
}

func (d *decoder) medium(e *xmltree.Element) (Medium, bool) {
	label, ok := e.AttrValue("", "label")
	if !ok {
		d.fail("entry", "without label")
		return Medium{}, false
	}

	m := Medium{Label: label, Attr: extensions(e)}
	s := seen{}

	for _, x := range e.Elements() {
		n := known(x)
		ok := false

		switch n {
		case "display-text":
			ok = d.once(s, x) && d.text(x, &m.DisplayText) && given(s, x, m.DisplayText)
		case "type":
			ok = d.once(s, x) && d.text(x, &m.Type)
		case "status":
			ok = d.once(s, x) && enum(d, x, &m.Status, mediaStatuses...)
		}

		if ok {
			s.decoded(x)
			continue
		}

		m.Other = append(m.Other, x)
	}

	if !s["type"] {
		d.fail("entry", "label %q without type", label)
		return Medium{}, false
	}

	return m, true
}

func (d *decoder) host(e *xmltree.Element, v **Host) bool {
	h := Host{Attr: extensions(e)}

	s := seen{}

	for _, x := range e.Elements() {
		n := known(x)
		ok := false

		switch n {
		case "display-text":
			ok = d.once(s, x) && d.text(x, &h.DisplayText) && given(s, x, h.DisplayText)
		case "web-page":
			ok = d.once(s, x) && d.anyURI(x, &h.WebPage) && given(s, x, h.WebPage)
		case "uris":
			ok = d.once(s, x) && d.uris(x, &h.URIs)
		}

		if ok {
			s.decoded(x)
			continue
		}

		h.Other = append(h.Other, x)
	}

	*v = &h

	return true
}

func (d *decoder) conferenceState(e *xmltree.Element, v **ConferenceState) bool {
	c := ConferenceState{Attr: extensions(e)}

	s := seen{}

	for _, x := range e.Elements() {
		n := known(x)
		ok := false

		switch n {
		case "user-count":
			ok = d.once(s, x) && d.unsigned(x, &c.UserCount)
		case "active":
			ok = d.once(s, x) && d.boolean(x, &c.Active)
		case "locked":
			ok = d.once(s, x) && d.boolean(x, &c.Locked)
		}

		if ok {
			s.decoded(x)
			continue
		}

		c.Other = append(c.Other, x)
	}

	*v = &c

	return true
}

func (d *decoder) users(e *xmltree.Element, v **Users) bool {
	state, ok := d.state(e)
	if !ok {
		return false
	}

	u := Users{State: state, Attr: extensions(e)}

	for _, x := range e.Elements() {
		if known(x) == "user" {
			if user, ok := d.user(x); ok {
				if !slices.ContainsFunc(u.Users, func(o User) bool { return o.Entity == user.Entity }) {
					u.Users = append(u.Users, user)
					continue
				}

				d.fail("user", "entity %q twice", user.Entity)
			}
		}

		u.Other = append(u.Other, x)
	}

	*v = &u

	return true
}

// RFC 4575 section 4.5
func (d *decoder) key(e *xmltree.Element) (string, bool) {
	entity, ok := e.AttrValue("", "entity")
	if !ok {
		d.fail(e.Name.Local, "without entity")
	}

	return entity, ok
}

func (d *decoder) user(e *xmltree.Element) (User, bool) {
	entity, ok := d.key(e)
	if !ok {
		return User{}, false
	}

	state, ok := d.state(e)
	if !ok {
		return User{}, false
	}

	u := User{Entity: xsd.Collapse(entity), State: state, Attr: extensions(e)}
	s := seen{}

	for _, x := range e.Elements() {
		n := known(x)
		ok := false

		switch n {
		case "display-text":
			ok = d.once(s, x) && d.text(x, &u.DisplayText) && given(s, x, u.DisplayText)
		case "associated-aors":
			ok = d.once(s, x) && d.uris(x, &u.AssociatedAORs)
		case "roles":
			ok = d.once(s, x) && d.roles(x, &u.Roles)
		case "languages":
			ok = d.once(s, x) && d.list(x, &u.Languages) && given(s, x, len(u.Languages))
		case "cascaded-focus":
			ok = d.once(s, x) && d.anyURI(x, &u.CascadedFocus) && given(s, x, u.CascadedFocus)
		case "endpoint":
			if ep, ok := d.endpoint(x); ok {
				if !slices.ContainsFunc(u.Endpoints, func(o Endpoint) bool { return o.Entity == ep.Entity }) {
					u.Endpoints = append(u.Endpoints, ep)
					continue
				}

				d.fail("endpoint", "entity %q twice", ep.Entity)
			}
		}

		if ok {
			s.decoded(x)
			continue
		}

		u.Other = append(u.Other, x)
	}

	return u, true
}

func (d *decoder) roles(e *xmltree.Element, v **Roles) bool {
	r := Roles{Attr: extensions(e)}

	for _, x := range e.Elements() {
		var entry string
		if known(x) != "entry" || !d.text(x, &entry) {
			d.fail("roles", "<%s>", x.Name.Local)
			return false
		}

		r.Entries = append(r.Entries, entry)
	}

	if len(r.Entries) == 0 {
		d.fail("roles", "no entry")
		return false
	}

	*v = &r

	return true
}

func (d *decoder) endpoint(e *xmltree.Element) (Endpoint, bool) {
	entity, ok := d.key(e)
	if !ok {
		return Endpoint{}, false
	}

	state, ok := d.state(e)
	if !ok {
		return Endpoint{}, false
	}

	ep := Endpoint{Entity: entity, State: state, Attr: extensions(e)}
	s := seen{}

	for _, x := range e.Elements() {
		n := known(x)
		ok := false

		switch n {
		case "display-text":
			ok = d.once(s, x) && d.text(x, &ep.DisplayText) && given(s, x, ep.DisplayText)
		case "referred":
			ok = d.once(s, x) && d.execution(x, &ep.Referred)
		case "status":
			ok = d.once(s, x) && enum(d, x, &ep.Status, endpointStatuses...)
		case "joining-method":
			ok = d.once(s, x) && enum(d, x, &ep.JoiningMethod, DialedIn, DialedOut, FocusOwner)
		case "joining-info":
			ok = d.once(s, x) && d.execution(x, &ep.JoiningInfo)
		case "disconnection-method":
			ok = d.once(s, x) && enum(d, x, &ep.DisconnectionMethod, Departed, Booted, Failed, Busy)
		case "disconnection-info":
			ok = d.once(s, x) && d.execution(x, &ep.DisconnectionInfo)
		case "media":
			if m, ok := d.media(x); ok {
				if !slices.ContainsFunc(ep.Media, func(o Media) bool { return o.ID == m.ID }) {
					ep.Media = append(ep.Media, m)
					continue
				}

				d.fail("media", "id %q twice", m.ID)
			}
		case "call-info":
			ok = d.once(s, x) && d.callInfo(x, &ep.CallInfo)
		}

		if ok {
			s.decoded(x)
			continue
		}

		ep.Other = append(ep.Other, x)
	}

	return ep, true
}

func (d *decoder) execution(e *xmltree.Element, v **Execution) bool {
	x := Execution{Attr: extensions(e)}

	s := seen{}

	for _, c := range e.Elements() {
		n := known(c)
		ok := false

		switch n {
		case "when":
			ok = d.once(s, c) && d.dateTime(c, &x.When)
		case "reason":
			ok = d.once(s, c) && d.text(c, &x.Reason)
		case "by":
			ok = d.once(s, c) && d.anyURI(c, &x.By)
		}

		if !ok {
			d.fail(e.Name.Local, "<%s>", c.Name.Local)
			return false
		}

		s[n] = true
	}

	*v = &x

	return true
}

func (d *decoder) callInfo(e *xmltree.Element, v **CallInfo) bool {
	c := CallInfo{Attr: extensions(e)}
	s := seen{}

	for _, x := range e.Elements() {
		if known(x) == "sip" && d.once(s, x) {
			if c.SIP = d.sipDialog(x); c.SIP != nil {
				continue
			}
		}

		c.Other = append(c.Other, x)
	}

	*v = &c

	return true
}

func (d *decoder) sipDialog(e *xmltree.Element) *SIPDialog {
	sd := SIPDialog{Attr: extensions(e)}

	s := seen{}

	for _, x := range e.Elements() {
		n := known(x)
		ok := false

		switch n {
		case "display-text":
			ok = d.once(s, x) && d.text(x, &sd.DisplayText) && given(s, x, sd.DisplayText)
		case "call-id":
			ok = d.once(s, x) && d.text(x, &sd.CallID)
		case "from-tag":
			ok = d.once(s, x) && d.text(x, &sd.FromTag)
		case "to-tag":
			ok = d.once(s, x) && d.text(x, &sd.ToTag)
		}

		if ok {
			s.decoded(x)
			continue
		}

		sd.Other = append(sd.Other, x)
	}

	if !s["call-id"] || !s["from-tag"] || !s["to-tag"] {
		d.fail("sip", "without call-id, from-tag or to-tag")
		return nil
	}

	return &sd
}

func (d *decoder) media(e *xmltree.Element) (Media, bool) {
	id, ok := e.AttrValue("", "id")
	if !ok {
		d.fail("media", "without id")
		return Media{}, false
	}

	m := Media{ID: id, Attr: extensions(e)}
	s := seen{}

	for _, x := range e.Elements() {
		n := known(x)
		ok := false

		switch n {
		case "display-text":
			ok = d.once(s, x) && d.text(x, &m.DisplayText) && given(s, x, m.DisplayText)
		case "type":
			ok = d.once(s, x) && d.text(x, &m.Type) && given(s, x, m.Type)
		case "label":
			ok = d.once(s, x) && d.text(x, &m.Label) && given(s, x, m.Label)
		case "src-id":
			ok = d.once(s, x) && d.text(x, &m.SrcID) && given(s, x, m.SrcID)
		case "status":
			ok = d.once(s, x) && enum(d, x, &m.Status, mediaStatuses...)
		}

		if ok {
			s.decoded(x)
			continue
		}

		m.Other = append(m.Other, x)
	}

	return m, true
}

// RFC 4575 section 5.9.2
func (d *decoder) sidebars(e *xmltree.Element, v **Sidebars) bool {
	state, ok := d.state(e)
	if !ok {
		return false
	}

	sb := Sidebars{State: state, Attr: extensions(e)}

	for _, x := range e.Elements() {
		if known(x) == "entry" {
			if c, ok := d.conference(x); ok {
				if !slices.ContainsFunc(sb.Entries, func(o Conference) bool { return o.Entity == c.Entity }) {
					sb.Entries = append(sb.Entries, c)
					continue
				}

				d.fail("entry", "entity %q twice", c.Entity)
			}
		}

		sb.Other = append(sb.Other, x)
	}

	*v = &sb

	return true
}

func extensions(e *xmltree.Element) []xml.Attr {
	var as []xml.Attr

	for _, a := range e.Attr {
		if a.Name.Space != "" && a.Name.Space != Namespace {
			as = append(as, a)
		}
	}

	return as
}

func (d *decoder) text(e *xmltree.Element, v *string) bool {
	if e.HasElements() {
		d.fail(e.Name.Local, "elements in a string")
		return false
	}

	*v = e.Text()

	return true
}

func (d *decoder) anyURI(e *xmltree.Element, v *string) bool {
	if !d.text(e, v) {
		return false
	}

	*v = xsd.Collapse(*v)

	return true
}

func (d *decoder) list(e *xmltree.Element, v *[]string) bool {
	var s string
	if !d.text(e, &s) {
		return false
	}

	*v = strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' })

	return true
}

func (d *decoder) unsigned(e *xmltree.Element, v **uint32) bool {
	n, ok := xsd.ParseUnsignedInt(e.Text())
	if !ok || e.HasElements() {
		d.fail(e.Name.Local, "%q", e.Text())
		return false
	}

	*v = &n

	return true
}

func (d *decoder) boolean(e *xmltree.Element, v **bool) bool {
	b, ok := xsd.ParseBoolean(e.Text())
	if !ok || e.HasElements() {
		d.fail(e.Name.Local, "%q", e.Text())
		return false
	}

	*v = &b

	return true
}

func (d *decoder) dateTime(e *xmltree.Element, v **xsd.DateTime) bool {
	t, err := xsd.ParseDateTime(e.Text())
	if err != nil || t.String() == "" || e.HasElements() {
		d.fail(e.Name.Local, "%q", e.Text())
		return false
	}

	*v = &t

	return true
}

var (
	mediaStatuses    = []MediaStatus{RecvOnly, SendOnly, SendRecv, Inactive}
	endpointStatuses = []EndpointStatus{
		Pending, DialingOut, DialingIn, Alerting, OnHold, Connected, MutedViaFocus, Disconnecting, Disconnected,
	}
)

func enum[T ~string](d *decoder, e *xmltree.Element, v *T, values ...T) bool {
	t := T(xsd.Collapse(e.Text()))
	if !slices.Contains(values, t) || e.HasElements() {
		d.fail(e.Name.Local, "%q", e.Text())
		return false
	}

	*v = t

	return true
}

func name(local string) xml.Name { return xml.Name{Space: Namespace, Local: local} }
