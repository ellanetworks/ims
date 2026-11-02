package reginfo

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/sip/xmltree"
	"github.com/ellanetworks/ims/sip/xsd"
)

const (
	ContentType = "application/reginfo+xml"
	Namespace   = "urn:ietf:params:xml:ns:reginfo"
)

const maxSize = 1 << 20

// RFC 3680 section 5.1
type State string

const (
	Full    State = "full"
	Partial State = "partial"
)

// RFC 3680 section 4.7.1
type RegState string

const (
	Init       RegState = "init"
	Active     RegState = "active"
	Terminated RegState = "terminated"
)

// RFC 3680 section 4.7.1
type Event string

const (
	Registered   Event = "registered"
	Created      Event = "created"
	Refreshed    Event = "refreshed"
	Shortened    Event = "shortened"
	Expired      Event = "expired"
	Deactivated  Event = "deactivated"
	Probation    Event = "probation"
	Unregistered Event = "unregistered"
	Rejected     Event = "rejected"
)

type Reginfo struct {
	Version       uint32
	State         State
	Registrations []Registration
}

// RFC 3680 section 5.1
type Registration struct {
	AOR      string
	ID       string
	State    RegState
	Contacts []Contact
}

type Contact struct {
	ID                                      string
	State                                   RegState
	Event                                   Event
	URI                                     string
	DisplayName, DisplayLang                string
	Expires, RetryAfter, DurationRegistered *uint64
	Q                                       string
	CallID                                  string
	CSeq                                    *uint64
	UnknownParams                           []Param
}

type Param struct {
	Name, Value string
}

// RFC 3261 section 7.3.1
func (c Contact) Param(name string) (string, bool) {
	for _, p := range c.UnknownParams {
		if strings.EqualFold(p.Name, name) {
			return p.Value, true
		}
	}

	return "", false
}

func Decode(b []byte) (Reginfo, error) {
	if len(b) > maxSize {
		return Reginfo{}, fmt.Errorf("reginfo: document of %d bytes exceeds %d", len(b), maxSize)
	}

	root, err := xmltree.Parse(b)
	if err != nil {
		return Reginfo{}, fmt.Errorf("reginfo: %w", err)
	}

	if root.Name != name("reginfo") {
		return Reginfo{}, fmt.Errorf("reginfo: root element <%s> of %q", root.Name.Local, root.Name.Space)
	}

	var r Reginfo

	v, ok := root.AttrValue("", "version")
	if !ok {
		return Reginfo{}, errors.New("reginfo: <reginfo> without version")
	}

	if r.Version, ok = xsd.ParseUnsignedInt(v); !ok {
		return Reginfo{}, fmt.Errorf("reginfo: <reginfo> version %q", v)
	}

	s, _ := root.AttrValue("", "state")
	if r.State = State(s); r.State != Full && r.State != Partial {
		return Reginfo{}, fmt.Errorf("reginfo: <reginfo> state %q", s)
	}

	for _, e := range children(root) {
		if e.Name.Local != "registration" {
			return Reginfo{}, fmt.Errorf("reginfo: <%s> in <reginfo>", e.Name.Local)
		}

		reg, err := registration(e)
		if err != nil {
			return Reginfo{}, err
		}

		r.Registrations = append(r.Registrations, reg)
	}

	return r, nil
}

func registration(e *xmltree.Element) (Registration, error) {
	aor, _ := e.AttrValue("", "aor")
	id, ok := e.AttrValue("", "id")
	state, _ := e.AttrValue("", "state")

	reg := Registration{AOR: xsd.Collapse(aor), ID: id, State: RegState(state)}

	switch {
	case reg.AOR == "":
		return Registration{}, errors.New("reginfo: <registration> without aor")
	case !ok:
		return Registration{}, fmt.Errorf("reginfo: <registration> of %s without id", reg.AOR)
	case reg.State != Init && reg.State != Active && reg.State != Terminated:
		return Registration{}, fmt.Errorf("reginfo: <registration> of %s: state %q", reg.AOR, state)
	}

	for _, c := range children(e) {
		if c.Name.Local != "contact" {
			return Registration{}, fmt.Errorf("reginfo: <%s> in <registration> of %s", c.Name.Local, reg.AOR)
		}

		ct, err := contact(c)
		if err != nil {
			return Registration{}, fmt.Errorf("reginfo: <registration> of %s: %w", reg.AOR, err)
		}

		reg.Contacts = append(reg.Contacts, ct)
	}

	return reg, nil
}

func contact(e *xmltree.Element) (Contact, error) {
	id, ok := e.AttrValue("", "id")
	if !ok {
		return Contact{}, errors.New("<contact> without id")
	}

	state, _ := e.AttrValue("", "state")
	event, _ := e.AttrValue("", "event")

	c := Contact{ID: id, State: RegState(state), Event: Event(event)}
	c.Q, _ = e.AttrValue("", "q")
	c.CallID, _ = e.AttrValue("", "callid")

	for _, a := range append(c.numbers(), number{"cseq", &c.CSeq}) {
		s, ok := e.AttrValue("", a.name)
		if !ok {
			continue
		}

		n, err := parseUnsignedLong(s)
		if err != nil {
			return Contact{}, fmt.Errorf("<contact> %s: %s %q", id, a.name, s)
		}

		*a.v = &n
	}

	if err := c.valid(); err != nil {
		return Contact{}, err
	}

	uris, names := 0, 0

	for _, x := range children(e) {
		switch x.Name.Local {
		case "uri":
			uris++
			c.URI = xsd.Collapse(x.Text())
		case "display-name":
			names++
			c.DisplayName = x.Text()
			c.DisplayLang, _ = x.AttrValue(xmltree.XMLNamespace, "lang")
		case "unknown-param":
			name, ok := x.AttrValue("", "name")
			if !ok {
				return Contact{}, fmt.Errorf("<contact> %s: <unknown-param> without name", id)
			}

			c.UnknownParams = append(c.UnknownParams, Param{Name: name, Value: x.Text()})
		default:
			return Contact{}, fmt.Errorf("<contact> %s: <%s>", id, x.Name.Local)
		}
	}

	switch {
	case uris != 1 || c.URI == "":
		return Contact{}, fmt.Errorf("<contact> %s: %d <uri>", id, uris)
	case names > 1:
		return Contact{}, fmt.Errorf("<contact> %s: %d <display-name>", id, names)
	}

	return c, nil
}

type number struct {
	name string
	v    **uint64
}

func (c *Contact) numbers() []number {
	return []number{{"expires", &c.Expires}, {"retry-after", &c.RetryAfter}, {"duration-registered", &c.DurationRegistered}}
}

// RFC 3680 section 5.1
func (c *Contact) valid() error {
	switch {
	case c.State != Active && c.State != Terminated:
		return fmt.Errorf("<contact> %s: state %q", c.ID, c.State)
	case !validEvent(c.Event):
		return fmt.Errorf("<contact> %s: event %q", c.ID, c.Event)
	case c.Event == Shortened && c.Expires == nil:
		return fmt.Errorf("<contact> %s: shortened without expires", c.ID)
	case c.Event == Probation && c.RetryAfter == nil:
		return fmt.Errorf("<contact> %s: probation without retry-after", c.ID)
	}

	return nil
}

func validEvent(e Event) bool {
	switch e {
	case Registered, Created, Refreshed, Shortened, Expired, Deactivated, Probation, Unregistered, Rejected:
		return true
	}

	return false
}

// RFC 3680 section 5.1
func children(e *xmltree.Element) []*xmltree.Element {
	var out []*xmltree.Element

	for _, c := range e.Elements() {
		if c.Name.Space == Namespace {
			out = append(out, c)
		}
	}

	return out
}

func parseUnsignedLong(s string) (uint64, error) {
	s = strings.TrimPrefix(xsd.Collapse(s), "+")
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, errors.New("not an unsignedLong")
	}

	return strconv.ParseUint(s, 10, 64)
}

func Encode(r Reginfo) ([]byte, error) {
	if r.State != Full && r.State != Partial {
		return nil, fmt.Errorf("reginfo: state %q", r.State)
	}

	root := &xmltree.Element{Name: name("reginfo"), Attr: []xml.Attr{
		attr("version", strconv.FormatUint(uint64(r.Version), 10)), attr("state", string(r.State)),
	}}

	for _, reg := range r.Registrations {
		e, err := reg.element()
		if err != nil {
			return nil, err
		}

		root.Children = append(root.Children, e)
	}

	b, err := xmltree.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("reginfo: %w", err)
	}

	return b, nil
}

func (reg *Registration) element() (*xmltree.Element, error) {
	switch {
	case xsd.Collapse(reg.AOR) == "":
		return nil, errors.New("reginfo: registration without aor")
	case reg.State != Init && reg.State != Active && reg.State != Terminated:
		return nil, fmt.Errorf("reginfo: registration %s: state %q", reg.AOR, reg.State)
	}

	e := &xmltree.Element{Name: name("registration"), Attr: []xml.Attr{
		attr("aor", reg.AOR), attr("id", reg.ID), attr("state", string(reg.State)),
	}}

	for i := range reg.Contacts {
		c, err := reg.Contacts[i].element()
		if err != nil {
			return nil, fmt.Errorf("reginfo: registration %s: %w", reg.AOR, err)
		}

		e.Children = append(e.Children, c)
	}

	return e, nil
}

// RFC 3680 section 5.4
func (c *Contact) element() (*xmltree.Element, error) {
	if err := c.valid(); err != nil {
		return nil, err
	}

	if xsd.Collapse(c.URI) == "" {
		return nil, fmt.Errorf("<contact> %s without uri", c.ID)
	}

	e := &xmltree.Element{Name: name("contact"), Attr: []xml.Attr{
		attr("id", c.ID), attr("state", string(c.State)), attr("event", string(c.Event)),
	}}

	for _, a := range c.numbers() {
		if *a.v != nil {
			e.Attr = append(e.Attr, attr(a.name, strconv.FormatUint(**a.v, 10)))
		}
	}

	if c.CallID != "" {
		e.Attr = append(e.Attr, attr("callid", c.CallID))
	}

	if c.CSeq != nil {
		e.Attr = append(e.Attr, attr("cseq", strconv.FormatUint(*c.CSeq, 10)))
	}

	if c.Q != "" {
		e.Attr = append(e.Attr, attr("q", c.Q))
	}

	e.Children = append(e.Children, xmltree.New(Namespace, "uri", xmltree.Text(c.URI)))

	if c.DisplayName != "" || c.DisplayLang != "" {
		d := xmltree.New(Namespace, "display-name", xmltree.Text(c.DisplayName))
		if c.DisplayLang != "" {
			d.Attr = []xml.Attr{{Name: xml.Name{Space: xmltree.XMLNamespace, Local: "lang"}, Value: c.DisplayLang}}
		}

		e.Children = append(e.Children, d)
	}

	for _, p := range c.UnknownParams {
		u := xmltree.New(Namespace, "unknown-param")
		u.Attr = []xml.Attr{attr("name", p.Name)}

		if p.Value != "" {
			u.Children = []xmltree.Node{xmltree.Text(p.Value)}
		}

		e.Children = append(e.Children, u)
	}

	return e, nil
}

func name(local string) xml.Name { return xml.Name{Space: Namespace, Local: local} }

func attr(local, value string) xml.Attr { return xml.Attr{Name: xml.Name{Local: local}, Value: value} }
