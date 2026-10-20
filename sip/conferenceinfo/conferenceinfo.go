// Package conferenceinfo is the conference information document of the conference event package,
// application/conference-info+xml (RFC 4575).
//
// Decode keeps the extension elements (RFC 4575 §6: the xs:any of other namespaces), and the elements it cannot
// decode, malformed or repeated, as Other, and the extension attributes (xs:anyAttribute) as Attr; Encode writes them
// back. An extension, such as TS 24.379 §10.1.3.6.1's <functional-alias> of an <endpoint>, decodes from them and
// sets them. Apply builds the conference state of a subscription from its full and partial notifications (§4.6).
//
// An element of the schema left out decodes as the zero value of its field, and the zero value encodes as no
// element: an empty string is no element, so an element of an empty string is not told from none.
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

// maxSize bounds a document: the roster of a large group call (TS 24.379 §6.3.3.4, a <user> per participant) fits.
const maxSize = 1 << 20

// Info is the <conference-info> root element (§5.2): the conference, and the version of the document.
type Info struct {
	// Version orders the notifications of a subscription (§4.3, §5.2): the root carries it, the only element whose
	// version is meaningful.
	Version uint32
	Conference
}

// Conference is a conference-type (§5.2): the root, or a sidebar of <sidebars-by-val> (§5.9.2).
type Conference struct {
	// Entity is the conference URI.
	Entity string
	// State is that of the document or sidebar: full, partial or deleted (§4.4).
	State           State
	Description     *Description
	Host            *Host
	ConferenceState *ConferenceState
	Users           *Users
	SidebarsByRef   *URIs
	SidebarsByVal   *Sidebars
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order: the extensions of other namespaces, and the elements of
	// the conference-info namespace unknown, malformed or repeated.
	Other []*xmltree.Element
}

// State is the value of a 'state' attribute (§4.4); empty without one, which is full.
type State string

const (
	Full    State = "full"
	Partial State = "partial"
	Deleted State = "deleted"
)

// Description is a <conference-description> (§5.3).
type Description struct {
	DisplayText string
	Subject     string
	FreeText    string
	Keywords    []string
	ConfURIs    *URIs
	ServiceURIs *URIs
	// MaximumUserCount is nil without one.
	MaximumUserCount *uint32
	AvailableMedia   *AvailableMedia
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// URIs is a uris-type (§6): <conf-uris>, <service-uris>, the <uris> of <host-info>, <associated-aors>,
// <sidebars-by-ref>. Its entries are keyed by URI (§4.5, §5.3.1): one at least, but of state deleted, whose children
// are left out (§4.4).
type URIs struct {
	State   State
	Entries []URI
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order: malformed or repeated entries.
	Other []*xmltree.Element
}

// URI is an <entry> of a uris-type.
type URI struct {
	URI         string
	DisplayText string
	Purpose     Purpose
	Modified    *Execution
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// Purpose is the <purpose> of a URI: what accessing it does (§5.3.1, §5.3.2; registry of §9.5).
type Purpose string

const (
	Participation Purpose = "participation"
	Streaming     Purpose = "streaming"
	WebPage       Purpose = "web-page"
	Recording     Purpose = "recording"
	Event         Purpose = "event"
)

// AvailableMedia is an <available-media> (§5.3.4): its entries keyed by label.
type AvailableMedia struct {
	Entries []Medium
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order: malformed or repeated entries.
	Other []*xmltree.Element
}

// Medium is an <entry> of <available-media>, a conference-medium-type.
type Medium struct {
	Label       string
	DisplayText string
	// Type is the SDP media type, required.
	Type   string
	Status MediaStatus
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// MediaStatus is a direction of SDP from the participant's point of view (§5.3.4, §5.8.5); empty without one.
type MediaStatus string

const (
	RecvOnly MediaStatus = "recvonly"
	SendOnly MediaStatus = "sendonly"
	SendRecv MediaStatus = "sendrecv"
	Inactive MediaStatus = "inactive"
)

// Host is a <host-info> (§5.4).
type Host struct {
	DisplayText string
	WebPage     string
	URIs        *URIs
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// ConferenceState is a <conference-state> (§5.5); a field is nil without its element.
type ConferenceState struct {
	UserCount *uint32
	Active    *bool
	Locked    *bool
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// Users is a <users> (§5.6): its users keyed by entity.
type Users struct {
	State State
	Users []User
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order: the extensions, malformed or repeated users.
	Other []*xmltree.Element
}

// User is a <user> (§5.6).
type User struct {
	// Entity is the URI of the user, unique among the users (§5.6): the key of the user (§4.5).
	Entity         string
	State          State
	DisplayText    string
	AssociatedAORs *URIs
	Roles          *Roles
	// Languages are language tags (§5.6.4).
	Languages     []string
	CascadedFocus string
	Endpoints     []Endpoint
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// Roles is a <roles> (§5.6.3), a user-roles-type: at least one role.
type Roles struct {
	// Entries are human-readable strings.
	Entries []string
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
}

// Endpoint is an <endpoint> (§5.7).
type Endpoint struct {
	// Entity is unique among the endpoints of the user (§5.7): the key of the endpoint (§4.5).
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
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order: the extensions, such as TS 24.379 §10.1.3.6.1's.
	Other []*xmltree.Element
}

// EndpointStatus is the <status> of an endpoint (§5.7.3); empty without one.
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

// JoiningMethod is a <joining-method> (§5.7.4); empty without one.
type JoiningMethod string

const (
	DialedIn   JoiningMethod = "dialed-in"
	DialedOut  JoiningMethod = "dialed-out"
	FocusOwner JoiningMethod = "focus-owner"
)

// DisconnectionMethod is a <disconnection-method> (§5.7.6); empty without one.
type DisconnectionMethod string

const (
	Departed DisconnectionMethod = "departed"
	Booted   DisconnectionMethod = "booted"
	Failed   DisconnectionMethod = "failed"
	Busy     DisconnectionMethod = "busy"
)

// Execution is an execution-type (§5.7.2, §5.7.5, §5.7.7): <referred>, <joining-info>, <disconnection-info>, the
// <modified> of a URI. The type has no extension point: an element with a child it does not define is malformed.
type Execution struct {
	When *xsd.DateTime
	// Reason is, as RECOMMENDED, a Reason header field value (RFC 3326).
	Reason string
	By     string
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
}

// CallInfo is a <call-info> (§5.7.9).
type CallInfo struct {
	SIP *SIPDialog
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order: the signaling of other protocols.
	Other []*xmltree.Element
}

// SIPDialog is the <sip> of a call-info: the identifier of the endpoint's dialog with the focus.
type SIPDialog struct {
	DisplayText string
	CallID      string
	FromTag     string
	ToTag       string
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// Media is a <media> of an endpoint (§5.8).
type Media struct {
	// ID is unique among the media of the endpoint: the key of the media (§4.5).
	ID          string
	DisplayText string
	Type        string
	Label       string
	// SrcID is, for RTP, the SSRC of the stream the endpoint sends (§5.8.4).
	SrcID  string
	Status MediaStatus
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// Sidebars is a <sidebars-by-val> (§5.9.2): its sidebars keyed by entity (§4.5).
type Sidebars struct {
	State   State
	Entries []Conference
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order: malformed or repeated entries.
	Other []*xmltree.Element
}

// ErrValue is wrapped by the errors of the elements Decode does not decode, malformed (RFC 4575 §6): the document it
// returns is usable without them, and keeps them.
var ErrValue = errors.New("malformed element")

// Decode decodes the document b. A malformed element is kept in the Other of its parent, with an error wrapping
// ErrValue: an element without a key (§4.5) or of a key not unique among its siblings, the first counting. An
// element given more than the schema allows is kept the same way, the first counting, malformed or not, but an
// optional one of the zero value, as none. A root without entity or version (§4.3) is an error.
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

// known is the name of e in the conference-info namespace, or "".
func known(e *xmltree.Element) string {
	if e.Name.Space != Namespace {
		return ""
	}

	return e.Name.Local
}

// seen are the names of the child elements given at most once already met: true of those decoded, false of those
// malformed.
type seen map[string]bool

// once reports whether x, of the conference-info namespace, is the first of its name among its siblings, which the
// schema allows once: a repeated one is not, with an error, the first counting.
func (d *decoder) once(s seen, x *xmltree.Element) bool {
	if _, ok := s[x.Name.Local]; ok {
		d.fail(x.Name.Local, "given more than once")
		return false
	}

	s[x.Name.Local] = false

	return true
}

// decoded records x decoded, but if given forgot it.
func (s seen) decoded(x *xmltree.Element) {
	if _, ok := s[x.Name.Local]; ok {
		s[x.Name.Local] = true
	}
}

// given forgets x, an optional element decoded, if v, its value, is the zero value: Encode writes no element of it,
// so it is as if not given (see the package documentation), and a sibling of its name may follow.
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

// conference decodes a conference-type: the root, or an <entry> of <sidebars-by-val>.
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

// uris decodes a uris-type: at least one entry, of a URI unique among them (§4.5, §5.3.1), but of state deleted,
// whose entries are left out (§4.4).
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

// availableMedia decodes an <available-media>: at least one entry, of a label unique among them (§5.3.4).
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

// key is the entity attribute of a user or endpoint, which the schema leaves optional: it is their key (§4.5), to be
// unique among their siblings (§5.6, §5.7).
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

// roles decodes a <roles>: at least one <entry> of a string, and nothing else.
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

// execution decodes an execution-type: <when>, <reason> and <by>, each at most once, and nothing else.
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

// sidebars decodes a <sidebars-by-val>: its entries of an entity unique among them (§4.5).
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

// extensions are the attributes of e of a namespace, other than conference-info's: the extensions of the schema's
// xs:anyAttribute. Those of no namespace the schema does not define are left out.
func extensions(e *xmltree.Element) []xml.Attr {
	var as []xml.Attr

	for _, a := range e.Attr {
		if a.Name.Space != "" && a.Name.Space != Namespace {
			as = append(as, a)
		}
	}

	return as
}

// text decodes an element of an xs:string.
func (d *decoder) text(e *xmltree.Element, v *string) bool {
	if e.HasElements() {
		d.fail(e.Name.Local, "elements in a string")
		return false
	}

	*v = e.Text()

	return true
}

// anyURI decodes an element of an xs:anyURI.
func (d *decoder) anyURI(e *xmltree.Element, v *string) bool {
	if !d.text(e, v) {
		return false
	}

	*v = xsd.Collapse(*v)

	return true
}

// list decodes an element of an xs:list: its items separated by white space.
func (d *decoder) list(e *xmltree.Element, v *[]string) bool {
	var s string
	if !d.text(e, &s) {
		return false
	}

	*v = strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' })

	return true
}

// unsigned decodes an element of an xs:unsignedInt.
func (d *decoder) unsigned(e *xmltree.Element, v **uint32) bool {
	n, ok := xsd.ParseUnsignedInt(e.Text())
	if !ok || e.HasElements() {
		d.fail(e.Name.Local, "%q", e.Text())
		return false
	}

	*v = &n

	return true
}

// boolean decodes an element of an xs:boolean.
func (d *decoder) boolean(e *xmltree.Element, v **bool) bool {
	b, ok := xsd.ParseBoolean(e.Text())
	if !ok || e.HasElements() {
		d.fail(e.Name.Local, "%q", e.Text())
		return false
	}

	*v = &b

	return true
}

// dateTime decodes an element of an xs:dateTime (§5.7.2, §5.7.5, §5.7.7: of a timezone or not, UTC as RECOMMENDED).
// One of the year 9999 that its timezone or 24:00:00 carries into 10000 is malformed: Encode cannot write it.
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

// enum decodes an element of an enumeration of values.
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
