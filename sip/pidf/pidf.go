// Package pidf is the Presence Information Data Format, application/pidf+xml (RFC 3863).
//
// Decode keeps the extension elements (RFC 3863 §4.2), and the elements it cannot decode, malformed or repeated, as
// Other, which Encode writes back: an extension, such as TS 24.379 §9.3.1's, decodes from them and sets them.
package pidf

import (
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/sip/xmltree"
)

const (
	ContentType = "application/pidf+xml"
	Namespace   = "urn:ietf:params:xml:ns:pidf"
)

// maxSize bounds a document: the affiliations of a user's clients and groups (TS 24.379 §9.3.1) fit.
const maxSize = 1 << 16

// Presence is the <presence> root element (§4.1.1).
type Presence struct {
	// Entity is the presentity's URI.
	Entity string
	Tuples []Tuple
	Notes  []Note
	// Other are the child elements not decoded, in order: the extensions of other namespaces, and the elements of
	// the PIDF namespace unknown, malformed or repeated.
	Other []*xmltree.Element
}

// Tuple is a <tuple> (§4.1.2).
type Tuple struct {
	// ID is unique among the presentity's tuples: an arbitrary string (§4.1.2), not the xs:ID of the schema, which
	// TS 24.379 §9.3.1.2's MCPTT client IDs, URNs, are not (§4.4: the schema is not for validation on the fly).
	ID     string
	Status Status
	// Other are the child elements not decoded, in order: the extensions, between <status> and <contact>.
	Other     []*xmltree.Element
	Contact   *Contact
	Notes     []Note
	Timestamp *time.Time
}

// Status is a <status> (§4.1.3): <basic>, the status extensions, at least one.
type Status struct {
	Basic Basic
	// Other are the status values not decoded, in order: the extensions.
	Other []*xmltree.Element
}

// Basic is the value of <basic> (§4.1.4); empty without one.
type Basic string

const (
	Open   Basic = "open"
	Closed Basic = "closed"
)

// Contact is a <contact> (§4.1.5).
type Contact struct {
	URI string
	// Priority is the priority in thousandths, 0 to 1000; nil without one, the lowest.
	Priority *uint16
}

// Note is a <note> (§4.1.6), its language the xml:lang of the element, if any.
type Note struct {
	Text string
	Lang string
}

// ErrValue is wrapped by the errors of the elements Decode does not decode, malformed (RFC 3863 §4.4): the document it
// returns is usable without them, and keeps them.
var ErrValue = errors.New("malformed element")

// Decode decodes the document b. A malformed element is kept in the Other of its parent, with an error wrapping
// ErrValue: a <tuple> of an ID not unique or without <status> in that of the presence. An element given more than
// the schema allows is kept the same way, the first counting.
func Decode(b []byte) (Presence, error) {
	if len(b) > maxSize {
		return Presence{}, fmt.Errorf("pidf: document of %d bytes exceeds %d", len(b), maxSize)
	}

	root, err := xmltree.Parse(b)
	if err != nil {
		return Presence{}, fmt.Errorf("pidf: %w", err)
	}

	if root.Name != name("presence") {
		return Presence{}, fmt.Errorf("pidf: root element <%s> of %q", root.Name.Local, root.Name.Space)
	}

	entity, ok := root.AttrValue("", "entity")
	if entity = collapse(entity); !ok || entity == "" {
		return Presence{}, errors.New("pidf: <presence> without entity")
	}

	var d decoder

	p := Presence{Entity: entity}

	for _, e := range root.Elements() {
		switch known(e) {
		case "tuple":
			t, ok := d.tuple(e)
			if ok && !slices.ContainsFunc(p.Tuples, func(o Tuple) bool { return o.ID == t.ID }) {
				p.Tuples = append(p.Tuples, t)
				continue
			}

			if ok {
				d.fail("tuple", "id %q twice", t.ID)
			}
		case "note":
			if n, ok := d.note(e); ok {
				p.Notes = append(p.Notes, n)
				continue
			}
		}

		p.Other = append(p.Other, e)
	}

	return p, errors.Join(d.errs...)
}

type decoder struct {
	errs []error
}

func (d *decoder) fail(element, format string, a ...any) {
	d.errs = append(d.errs, fmt.Errorf("pidf: <%s>: %w: %s", element, ErrValue, fmt.Sprintf(format, a...)))
}

// known is the name of e in the PIDF namespace, or "".
func known(e *xmltree.Element) string {
	if e.Name.Space != Namespace {
		return ""
	}

	return e.Name.Local
}

func (d *decoder) tuple(e *xmltree.Element) (Tuple, bool) {
	id, _ := e.AttrValue("", "id")
	if id = collapse(id); id == "" {
		d.fail("tuple", "without id")
		return Tuple{}, false
	}

	t := Tuple{ID: id}
	hasStatus := false

	for _, c := range e.Elements() {
		switch known(c) {
		case "status":
			if !hasStatus {
				if t.Status, hasStatus = d.status(c); hasStatus {
					continue
				}
			}
		case "contact":
			if t.Contact == nil {
				if t.Contact = d.contact(c); t.Contact != nil {
					continue
				}
			}
		case "note":
			if n, ok := d.note(c); ok {
				t.Notes = append(t.Notes, n)
				continue
			}
		case "timestamp":
			if t.Timestamp == nil {
				if t.Timestamp = d.timestamp(c); t.Timestamp != nil {
					continue
				}
			}
		}

		t.Other = append(t.Other, c)
	}

	if !hasStatus {
		d.fail("tuple", "id %q without status", id)
		return Tuple{}, false
	}

	return t, true
}

// status decodes a <status>: at least one status value (§4.1.3).
func (d *decoder) status(e *xmltree.Element) (Status, bool) {
	var s Status

	for _, c := range e.Elements() {
		if known(c) == "basic" && s.Basic == "" && !c.HasElements() {
			if v := Basic(collapse(c.Text())); v == Open || v == Closed {
				s.Basic = v
				continue
			}

			d.fail("basic", "%q", c.Text())
		}

		s.Other = append(s.Other, c)
	}

	if s.Basic == "" && len(s.Other) == 0 {
		d.fail("status", "no status value")
		return Status{}, false
	}

	return s, true
}

// contact decodes a <contact>; a priority out of range is ignored (§4.1.5).
func (d *decoder) contact(e *xmltree.Element) *Contact {
	if e.HasElements() {
		d.fail("contact", "elements in a URI")
		return nil
	}

	c := &Contact{URI: collapse(e.Text())}

	if v, ok := e.AttrValue("", "priority"); ok {
		c.Priority = parseQValue(collapse(v))
	}

	return c
}

// parseQValue parses a qvalue of the schema (§4.4): 0 to 1, at most 3 decimals, in thousandths.
func parseQValue(s string) *uint16 {
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole != "0" && whole != "1" || len(frac) > 3 || hasFrac && strings.Trim(frac, "0123456789") != "" {
		return nil
	}

	frac += strings.Repeat("0", 3-len(frac))

	n, _ := strconv.Atoi(frac)
	if whole == "1" && n != 0 {
		return nil
	}

	q := uint16(n)
	if whole == "1" {
		q = 1000
	}

	return &q
}

func (d *decoder) note(e *xmltree.Element) (Note, bool) {
	if e.HasElements() {
		d.fail("note", "elements in a string")
		return Note{}, false
	}

	lang, _ := e.AttrValue(xmltree.XMLNamespace, "lang")

	return Note{Text: e.Text(), Lang: lang}, true
}

// timestamp decodes a <timestamp>, an RFC 3339 date and time (§4.1.7).
func (d *decoder) timestamp(e *xmltree.Element) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, collapse(e.Text()))
	if err != nil || e.HasElements() {
		d.fail("timestamp", "%q", e.Text())
		return nil
	}

	return &t
}

// collapse is s without the leading and trailing white space the whiteSpace facet collapse of XML Schema Part 2
// §4.3.6 removes: #x20, #x9, #xA and #xD.
func collapse(s string) string { return strings.Trim(s, " \t\n\r") }

// MustUnderstand reports whether e carries mustUnderstand="true" or "1" (§4.2.3): an extension that does not know e
// treats the element of its own that contains it as unrecognized.
func MustUnderstand(e *xmltree.Element) bool {
	v, _ := e.AttrValue(Namespace, "mustUnderstand")
	v = collapse(v)

	return v == "true" || v == "1"
}

// Encode encodes p, in the order of the schema (§4.4): the extensions of a tuple after its <status>, the elements of
// Other of the PIDF namespace, not decoded, after those decoded.
func Encode(p Presence) ([]byte, error) {
	if collapse(p.Entity) == "" {
		return nil, errors.New("pidf: presence without entity")
	}

	root := &xmltree.Element{Name: name("presence"), Attr: []xml.Attr{{Name: xml.Name{Local: "entity"}, Value: p.Entity}}}

	var ids []string

	for _, t := range p.Tuples {
		if collapse(t.ID) == "" || slices.Contains(ids, t.ID) {
			return nil, fmt.Errorf("pidf: tuple id %q", t.ID)
		}

		ids = append(ids, t.ID)

		e, err := t.element()
		if err != nil {
			return nil, err
		}

		root.Children = append(root.Children, e)
	}

	root.Children = appendNotes(root.Children, p.Notes)
	root.Children = appendElements(root.Children, p.Other)

	b, err := xmltree.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("pidf: %w", err)
	}

	return b, nil
}

func (t *Tuple) element() (*xmltree.Element, error) {
	s := t.Status
	if s.Basic == "" && len(s.Other) == 0 {
		return nil, fmt.Errorf("pidf: tuple %q without status value", t.ID)
	}

	if s.Basic != "" && s.Basic != Open && s.Basic != Closed {
		return nil, fmt.Errorf("pidf: tuple %q: basic %q", t.ID, s.Basic)
	}

	status := &xmltree.Element{Name: name("status")}
	if s.Basic != "" {
		status.Children = append(status.Children, xmltree.New(Namespace, "basic", xmltree.Text(s.Basic)))
	}

	status.Children = appendElements(status.Children, s.Other)

	// The extensions between <status> and <contact>; the PIDF elements not decoded last, after those decoded.
	var ext, own []*xmltree.Element

	for _, o := range t.Other {
		if o.Name.Space == Namespace {
			own = append(own, o)
		} else {
			ext = append(ext, o)
		}
	}

	e := &xmltree.Element{Name: name("tuple"), Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: t.ID}}}
	e.Children = append(e.Children, status)
	e.Children = appendElements(e.Children, ext)

	if c := t.Contact; c != nil {
		ce := xmltree.New(Namespace, "contact", xmltree.Text(c.URI))

		if c.Priority != nil {
			if *c.Priority > 1000 {
				return nil, fmt.Errorf("pidf: tuple %q: priority %d", t.ID, *c.Priority)
			}

			ce.Attr = []xml.Attr{{Name: xml.Name{Local: "priority"}, Value: formatQValue(*c.Priority)}}
		}

		e.Children = append(e.Children, ce)
	}

	e.Children = appendNotes(e.Children, t.Notes)

	if t.Timestamp != nil {
		e.Children = append(e.Children, xmltree.New(Namespace, "timestamp", xmltree.Text(t.Timestamp.Format(time.RFC3339Nano))))
	}

	e.Children = appendElements(e.Children, own)

	return e, nil
}

func formatQValue(q uint16) string {
	if q == 1000 {
		return "1"
	}

	s := strings.TrimRight(fmt.Sprintf("0.%03d", q), "0")

	return strings.TrimSuffix(s, ".")
}

func appendNotes(ns []xmltree.Node, notes []Note) []xmltree.Node {
	for _, n := range notes {
		e := xmltree.New(Namespace, "note", xmltree.Text(n.Text))
		if n.Lang != "" {
			e.Attr = []xml.Attr{{Name: xml.Name{Space: xmltree.XMLNamespace, Local: "lang"}, Value: n.Lang}}
		}

		ns = append(ns, e)
	}

	return ns
}

func appendElements(ns []xmltree.Node, es []*xmltree.Element) []xmltree.Node {
	for _, e := range es {
		ns = append(ns, e)
	}

	return ns
}

func name(local string) xml.Name { return xml.Name{Space: Namespace, Local: local} }
