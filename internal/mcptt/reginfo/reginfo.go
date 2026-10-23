// Package reginfo decodes the registration information document, application/reginfo+xml (RFC 3680 §5), of the
// NOTIFYs of the reg event package the server subscribes to as an application server (TS 24.229 §5.7.1.1): what a
// subscriber reads of it, the registrations and their contacts.
//
// It decodes only: the server is a subscriber, never a notifier. The elements and attributes of other namespaces are
// ignored (§5.1), the extensions of the contacts among them, such as the GRUUs of RFC 5628; the parameters of a
// contact that RFC 3261 does not define, such as +sip.instance, are its unknown-params. A document not valid against
// the schema of §5.4 in what it reads is refused, so that a subscriber acts on no contact it misread.
//
// sip should own this codec, beside sip/pidf: Ella IMS has one, internal to it.
package reginfo

import (
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

// maxSize bounds a document: the contacts of the registrations of an implicit registration set.
const maxSize = 1 << 20

// State is the value of the "state" attribute of <reginfo>: whether the document has the full state, or the changes
// since the previous one (§5.1).
type State string

const (
	Full    State = "full"
	Partial State = "partial"
)

// RegState is the state of a registration or contact (§4.7.1): init of a registration only.
type RegState string

const (
	Init       RegState = "init"
	Active     RegState = "active"
	Terminated RegState = "terminated"
)

// Event is the event that put a contact in its state (§4.7.1, Figure 2).
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

// Reginfo is the <reginfo> root element.
type Reginfo struct {
	// Version orders the documents of a subscription (§5.2); 32 bits (§5.1).
	Version       uint32
	State         State
	Registrations []Registration
}

// Registration is a <registration>: the state of an address-of-record (§5.1).
type Registration struct {
	AOR      string
	ID       string
	State    RegState
	Contacts []Contact
}

// Contact is a <contact>.
type Contact struct {
	// ID identifies the contact within the subscription (§5.1).
	ID    string
	State RegState
	Event Event
	URI   string
	// Expires, of an active contact, is the number of seconds until it expires; RetryAfter, of a probation, when to
	// register again. Nil if absent.
	Expires, RetryAfter *uint64
	// CallID and CSeq are those of the REGISTER that last updated the contact; "" and nil if absent.
	CallID string
	CSeq   *uint64
	// UnknownParams are the Contact header field parameters RFC 3261 does not define, in order.
	UnknownParams []Param
}

// Param is an <unknown-param>: a parameter name, and its value as in the Contact, quotes included; "" of none.
type Param struct {
	Name, Value string
}

// Param is the value of the unknown-param name of c, its name compared case-insensitively (RFC 3261 §7.3.1).
func (c Contact) Param(name string) (string, bool) {
	for _, p := range c.UnknownParams {
		if strings.EqualFold(p.Name, name) {
			return p.Value, true
		}
	}

	return "", false
}

// Decode decodes a registration information document.
func Decode(b []byte) (Reginfo, error) {
	if len(b) > maxSize {
		return Reginfo{}, fmt.Errorf("reginfo: document of %d bytes exceeds %d", len(b), maxSize)
	}

	root, err := xmltree.Parse(b)
	if err != nil {
		return Reginfo{}, fmt.Errorf("reginfo: %w", err)
	}

	if root.Name.Space != Namespace || root.Name.Local != "reginfo" {
		return Reginfo{}, fmt.Errorf("reginfo: root element <%s> of %q", root.Name.Local, root.Name.Space)
	}

	var r Reginfo

	v, ok := root.AttrValue("", "version")
	if r.Version, ok = parseVersion(v, ok); !ok {
		return Reginfo{}, fmt.Errorf("reginfo: <reginfo> version %q", v)
	}

	switch s, _ := root.AttrValue("", "state"); State(s) {
	case Full, Partial:
		r.State = State(s)
	default:
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
	aor, ok1 := e.AttrValue("", "aor")
	id, ok2 := e.AttrValue("", "id")
	state, _ := e.AttrValue("", "state")

	reg := Registration{AOR: xsd.Collapse(aor), ID: id, State: RegState(state)}

	switch {
	case !ok1 || reg.AOR == "":
		return Registration{}, errors.New("reginfo: <registration> without aor")
	case !ok2:
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

	c := Contact{ID: id}
	c.CallID, _ = e.AttrValue("", "callid")

	switch s, _ := e.AttrValue("", "state"); RegState(s) {
	case Active, Terminated:
		c.State = RegState(s)
	default:
		return Contact{}, fmt.Errorf("<contact> %s: state %q", id, s)
	}

	switch ev, _ := e.AttrValue("", "event"); Event(ev) {
	case Registered, Created, Refreshed, Shortened, Expired, Deactivated, Probation, Unregistered, Rejected:
		c.Event = Event(ev)
	default:
		return Contact{}, fmt.Errorf("<contact> %s: event %q", id, ev)
	}

	for _, a := range []struct {
		name string
		v    **uint64
	}{{"expires", &c.Expires}, {"retry-after", &c.RetryAfter}, {"duration-registered", nil}, {"cseq", &c.CSeq}} {
		s, ok := e.AttrValue("", a.name)
		if !ok {
			continue
		}

		n, err := parseUnsignedLong(s)
		if err != nil {
			return Contact{}, fmt.Errorf("<contact> %s: %s %q", id, a.name, s)
		}

		if a.v != nil {
			*a.v = &n
		}
	}

	// §5.1: the attribute each of these events requires.
	switch {
	case c.Event == Shortened && c.Expires == nil:
		return Contact{}, fmt.Errorf("<contact> %s: shortened without expires", id)
	case c.Event == Probation && c.RetryAfter == nil:
		return Contact{}, fmt.Errorf("<contact> %s: probation without retry-after", id)
	}

	uris := 0

	for _, x := range children(e) {
		switch x.Name.Local {
		case "uri":
			uris++
			c.URI = xsd.Collapse(x.Text())
		case "display-name":
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

	if uris != 1 || c.URI == "" {
		return Contact{}, fmt.Errorf("<contact> %s: %d <uri>", id, uris)
	}

	return c, nil
}

// children are the child elements of e of the namespace: those of others are ignored (§5.1).
func children(e *xmltree.Element) []*xmltree.Element {
	var out []*xmltree.Element

	for _, c := range e.Elements() {
		if c.Name.Space == Namespace {
			out = append(out, c)
		}
	}

	return out
}

// parseVersion parses the version of <reginfo>, an xs:nonNegativeInteger of 32 bits (§5.1).
func parseVersion(s string, present bool) (uint32, bool) {
	if !present {
		return 0, false
	}

	return xsd.ParseUnsignedInt(s)
}

// parseUnsignedLong parses an xs:unsignedLong: decimal digits, after an optional "+".
func parseUnsignedLong(s string) (uint64, error) {
	s = strings.TrimPrefix(xsd.Collapse(s), "+")
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, errors.New("not an unsignedLong")
	}

	return strconv.ParseUint(s, 10, 64)
}
