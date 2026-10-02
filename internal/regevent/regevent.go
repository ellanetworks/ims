package regevent

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	ContentType = "application/reginfo+xml"
	Namespace   = "urn:ietf:params:xml:ns:reginfo"
)

const (
	Full    = "full"
	Partial = "partial"
)

const (
	Init       = "init"
	Active     = "active"
	Terminated = "terminated"
)

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

const maxDocumentSize = 1 << 20

const xmlNamespace = "http://www.w3.org/XML/1998/namespace"

type Reginfo struct {
	Version       uint64
	State         string
	Registrations []Registration
}

type Registration struct {
	AOR      string
	ID       string
	State    string
	Contacts []Contact
}

type Contact struct {
	ID            string
	State         string
	Event         Event
	Expires       *uint32
	RetryAfter    *uint32
	CallID        string
	CSeq          *uint32
	Q             string
	URI           string
	DisplayName   string
	DisplayLang   string
	UnknownParams []UnknownParam
}

type UnknownParam struct {
	Name  string
	Value string
}

type xmlReginfo struct {
	XMLName       xml.Name          `xml:"urn:ietf:params:xml:ns:reginfo reginfo"`
	Version       uint64            `xml:"version,attr"`
	State         string            `xml:"state,attr"`
	Registrations []xmlRegistration `xml:"registration"`
}

type xmlRegistration struct {
	AOR      string       `xml:"aor,attr"`
	ID       string       `xml:"id,attr"`
	State    string       `xml:"state,attr"`
	Contacts []xmlContact `xml:"contact"`
}

type xmlContact struct {
	ID            string            `xml:"id,attr"`
	State         string            `xml:"state,attr"`
	Event         string            `xml:"event,attr"`
	Expires       *uint32           `xml:"expires,attr,omitempty"`
	RetryAfter    *uint32           `xml:"retry-after,attr,omitempty"`
	CallID        string            `xml:"callid,attr,omitempty"`
	CSeq          *uint32           `xml:"cseq,attr,omitempty"`
	Q             string            `xml:"q,attr,omitempty"`
	URI           string            `xml:"uri"`
	DisplayName   *xmlDisplayName   `xml:"display-name,omitempty"`
	UnknownParams []xmlUnknownParam `xml:"unknown-param"`
}

type xmlDisplayName struct {
	Lang  string `xml:"http://www.w3.org/XML/1998/namespace lang,attr,omitempty"`
	Value string `xml:",chardata"`
}

type xmlUnknownParam struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

func Encode(r Reginfo) ([]byte, error) {
	if err := validate(r); err != nil {
		return nil, fmt.Errorf("encode reginfo: %w", err)
	}

	doc := xmlReginfo{Version: r.Version, State: r.State}

	for _, reg := range r.Registrations {
		xr := xmlRegistration{AOR: reg.AOR, ID: reg.ID, State: reg.State}

		for _, c := range reg.Contacts {
			xc := xmlContact{
				ID:         c.ID,
				State:      c.State,
				Event:      string(c.Event),
				Expires:    c.Expires,
				RetryAfter: c.RetryAfter,
				CallID:     c.CallID,
				CSeq:       c.CSeq,
				Q:          c.Q,
				URI:        c.URI,
			}

			if c.DisplayName != "" {
				xc.DisplayName = &xmlDisplayName{Lang: c.DisplayLang, Value: c.DisplayName}
			}

			for _, p := range c.UnknownParams {
				xc.UnknownParams = append(xc.UnknownParams, xmlUnknownParam(p))
			}

			xr.Contacts = append(xr.Contacts, xc)
		}

		doc.Registrations = append(doc.Registrations, xr)
	}

	out, err := xml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode reginfo: %w", err)
	}

	return append([]byte(xml.Header), out...), nil
}

func validate(r Reginfo) error {
	if err := validateState(r.State); err != nil {
		return err
	}

	for _, reg := range r.Registrations {
		if err := validateRegistration(reg); err != nil {
			return err
		}

		for _, c := range reg.Contacts {
			if err := validateContact(c); err != nil {
				return fmt.Errorf("registration %s: %w", reg.AOR, err)
			}
		}
	}

	return nil
}

func validateState(state string) error {
	if state != Full && state != Partial {
		return fmt.Errorf("invalid reginfo state %q", state)
	}

	return nil
}

func validateRegistration(reg Registration) error {
	if reg.AOR == "" {
		return errors.New("registration without aor")
	}

	if reg.ID == "" {
		return fmt.Errorf("registration %s without id", reg.AOR)
	}

	if reg.State != Init && reg.State != Active && reg.State != Terminated {
		return fmt.Errorf("registration %s: invalid state %q", reg.AOR, reg.State)
	}

	return nil
}

func validateContact(c Contact) error {
	if c.ID == "" {
		return errors.New("contact without id")
	}

	if c.State != Active && c.State != Terminated {
		return fmt.Errorf("contact %s: invalid state %q", c.ID, c.State)
	}

	if !validEvent(c.Event) {
		return fmt.Errorf("contact %s: invalid event %q", c.ID, c.Event)
	}

	if c.URI == "" {
		return fmt.Errorf("contact %s without uri", c.ID)
	}

	for _, p := range c.UnknownParams {
		if p.Name == "" {
			return fmt.Errorf("contact %s: unknown-param without name", c.ID)
		}
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

type attrs []xml.Attr

func (a attrs) get(local string) (string, bool) {
	for _, at := range a {
		if at.Name.Space == "" && at.Name.Local == local {
			return at.Value, true
		}
	}

	return "", false
}

func (a attrs) required(elem, local string) (string, error) {
	v, ok := a.get(local)
	if !ok {
		return "", fmt.Errorf("%s: missing %s attribute", elem, local)
	}

	return v, nil
}

func (a attrs) uint32(elem, local string) (*uint32, error) {
	v, ok := a.get(local)
	if !ok {
		return nil, nil
	}

	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid %s attribute %q: %w", elem, local, v, err)
	}

	u := uint32(min(n, math.MaxUint32))

	return &u, nil
}

type inReginfo struct {
	XMLName       xml.Name         `xml:"urn:ietf:params:xml:ns:reginfo reginfo"`
	Attrs         attrs            `xml:",any,attr"`
	Registrations []inRegistration `xml:"urn:ietf:params:xml:ns:reginfo registration"`
}

type inRegistration struct {
	Attrs    attrs       `xml:",any,attr"`
	Contacts []inContact `xml:"urn:ietf:params:xml:ns:reginfo contact"`
}

type inContact struct {
	Attrs         attrs            `xml:",any,attr"`
	URI           []string         `xml:"urn:ietf:params:xml:ns:reginfo uri"`
	DisplayName   *inDisplayName   `xml:"urn:ietf:params:xml:ns:reginfo display-name"`
	UnknownParams []inUnknownParam `xml:"urn:ietf:params:xml:ns:reginfo unknown-param"`
}

type inDisplayName struct {
	Attrs attrs  `xml:",any,attr"`
	Value string `xml:",chardata"`
}

type inUnknownParam struct {
	Attrs attrs  `xml:",any,attr"`
	Value string `xml:",chardata"`
}

func Decode(b []byte) (Reginfo, error) {
	if len(b) > maxDocumentSize {
		return Reginfo{}, fmt.Errorf("decode reginfo: document of %d bytes exceeds %d", len(b), maxDocumentSize)
	}

	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.CharsetReader = charsetReader

	var doc inReginfo
	if err := dec.Decode(&doc); err != nil {
		return Reginfo{}, fmt.Errorf("decode reginfo: %w", err)
	}

	r, err := fromXML(doc)
	if err != nil {
		return Reginfo{}, fmt.Errorf("decode reginfo: %w", err)
	}

	return r, nil
}

func charsetReader(label string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "us-ascii", "ascii", "iso646-us", "ansi_x3.4-1968":
		return input, nil
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "l1":
		return latin1Reader(input)
	}

	return nil, fmt.Errorf("unsupported charset %q", label)
}

func latin1Reader(input io.Reader) (io.Reader, error) {
	in, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, len(in))
	for _, c := range in {
		out = utf8.AppendRune(out, rune(c))
	}

	return bytes.NewReader(out), nil
}

func fromXML(doc inReginfo) (Reginfo, error) {
	var r Reginfo

	v, err := doc.Attrs.required("reginfo", "version")
	if err != nil {
		return r, err
	}

	r.Version, err = strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return r, fmt.Errorf("reginfo: invalid version %q: %w", v, err)
	}

	if r.State, err = doc.Attrs.required("reginfo", "state"); err != nil {
		return r, err
	}

	if err := validateState(r.State); err != nil {
		return r, err
	}

	for _, xr := range doc.Registrations {
		reg, err := registrationFromXML(xr)
		if err != nil {
			continue
		}

		r.Registrations = append(r.Registrations, reg)
	}

	return r, nil
}

func registrationFromXML(xr inRegistration) (Registration, error) {
	var (
		reg Registration
		err error
	)

	if reg.AOR, err = xr.Attrs.required("registration", "aor"); err != nil {
		return reg, err
	}

	if reg.ID, err = xr.Attrs.required("registration", "id"); err != nil {
		return reg, err
	}

	if reg.State, err = xr.Attrs.required("registration", "state"); err != nil {
		return reg, err
	}

	if err := validateRegistration(reg); err != nil {
		return reg, err
	}

	for _, xc := range xr.Contacts {
		c, err := contactFromXML(xc)
		if err != nil {
			continue
		}

		reg.Contacts = append(reg.Contacts, c)
	}

	return reg, nil
}

func contactFromXML(xc inContact) (Contact, error) {
	var (
		c   Contact
		err error
	)

	if c.ID, err = xc.Attrs.required("contact", "id"); err != nil {
		return c, err
	}

	if c.State, err = xc.Attrs.required("contact", "state"); err != nil {
		return c, err
	}

	event, err := xc.Attrs.required("contact", "event")
	if err != nil {
		return c, err
	}

	c.Event = Event(event)

	if c.Expires, err = xc.Attrs.uint32("contact", "expires"); err != nil {
		return c, err
	}

	if c.RetryAfter, err = xc.Attrs.uint32("contact", "retry-after"); err != nil {
		return c, err
	}

	if c.CSeq, err = xc.Attrs.uint32("contact", "cseq"); err != nil {
		return c, err
	}

	c.CallID, _ = xc.Attrs.get("callid")
	c.Q, _ = xc.Attrs.get("q")

	if len(xc.URI) != 1 {
		return c, fmt.Errorf("contact %s: expected one uri element, got %d", c.ID, len(xc.URI))
	}

	c.URI = strings.TrimSpace(xc.URI[0])
	if c.URI == "" {
		return c, fmt.Errorf("contact %s: empty uri", c.ID)
	}

	if xc.DisplayName != nil {
		c.DisplayName = xc.DisplayName.Value

		for _, at := range xc.DisplayName.Attrs {
			if at.Name.Space == xmlNamespace && at.Name.Local == "lang" {
				c.DisplayLang = at.Value
			}
		}
	}

	for _, p := range xc.UnknownParams {
		name, err := p.Attrs.required("unknown-param", "name")
		if err != nil {
			return c, fmt.Errorf("contact %s: %w", c.ID, err)
		}

		c.UnknownParams = append(c.UnknownParams, UnknownParam{Name: name, Value: p.Value})
	}

	if err := validateContact(c); err != nil {
		return c, err
	}

	return c, nil
}
