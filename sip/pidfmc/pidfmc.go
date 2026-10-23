// Package pidfmc is the MCPTT extension of the presence information data format, of namespace
// urn:3gpp:ns:mcpttPresInfo:1.0 (TS 24.379 §9.3.1), on the documents of sip/pidf: per-user affiliation information,
// per-group affiliation information and per-group dynamic data information (§9.3.1.2).
//
// The three are of one schema (Table 9.3.1.2-1) and differ in what the entity, the tuple IDs and the attributes of
// <affiliation> identify. A procedure knows which it handles from the request, by its Request-URI and its event
// subscription (§9.2.2.2.3, §9.2.2.3.3, §9.2.2.3.9, §9.2.1.3, §9.2.1.6), not from the document, so they share the
// types here, and the entity and tuple IDs are those of pidf.Presence and pidf.Tuple.
//
// DecodePresence and DecodeStatus decode the extension elements of a <presence> and of a tuple's <status> from their
// Other; SetPresence and SetStatus replace the elements those decode and keep the others: of other extensions, of
// later releases, unknown, malformed or repeated, so a document decoded and set passes them on. An <affiliation> or
// <functionalAlias> with a child marked mustUnderstand, which the extension does not know, is unrecognized: kept, not
// decoded (RFC 3863 §4.2.3).
package pidfmc

import (
	"encoding/xml"
	"errors"
	"fmt"

	"github.com/ellanetworks/ims/sip/pidf"
	"github.com/ellanetworks/ims/sip/xmltree"
	"github.com/ellanetworks/ims/sip/xsd"
)

const Namespace = "urn:3gpp:ns:mcpttPresInfo:1.0"

// Presence is the extension of a <presence>.
type Presence struct {
	// PID identifies the PUBLISH of the document (§9.3.1.2 item 4): set by the client (§9.2.1.2 5f) and the
	// participating function (§9.2.2.2.6 step 7), and in the NOTIFY the PUBLISH triggered (§9.2.2.2.5 step 3d,
	// §9.2.2.3.5 step 3b); "" without one.
	PID string
	// GroupStatus and AdditionalData are of per-group dynamic data; nil without one.
	GroupStatus    *GroupStatus
	AdditionalData *AdditionalData
}

// Status is the extension of a tuple's <status>.
type Status struct {
	// Affiliations are, in per-user affiliation information, of the groups the user is interested in at the client
	// of the tuple, one per group (§9.3.1.2 item 7); in per-group information, of the clients at which the member of
	// the tuple is, one per client (items 7, 12).
	Affiliations []Affiliation
	// FunctionalAliases are those the member of the tuple activated, in per-group information, one per functional
	// alias (items 10, 15).
	FunctionalAliases []FunctionalAlias
}

// Affiliation is an <affiliation>.
type Affiliation struct {
	// Group is the MCPTT group ID, of per-user affiliation information (§9.3.1.2 item 8).
	Group string
	// Client is the MCPTT client ID, of per-group information (items 8, 13).
	Client string
	// Status is the affiliation status, "" without one: a client publishes none (§9.2.1.2 5e).
	Status AffiliationStatus
	// Expires is the expiration of the affiliation; nil without one: a client and a participating function publish
	// none (§9.2.1.2 5e, §9.2.2.2.6 step 10), the server owning the group notifies it (§9.2.2.3.5 step 3a).
	Expires *xsd.DateTime
	// Other are the child elements, of later releases.
	Other []*xmltree.Element
	// Attr are the attributes but group, client, status and expires.
	Attr []xml.Attr
}

// AffiliationStatus is the value of the "status" attribute of an <affiliation> (statusType).
type AffiliationStatus string

const (
	Affiliating   AffiliationStatus = "affiliating"
	Affiliated    AffiliationStatus = "affiliated"
	Deaffiliating AffiliationStatus = "deaffiliating"
)

// FunctionalAlias is a <functionalAlias>.
type FunctionalAlias struct {
	// ID is the functional alias ID (§9.3.1.2 items 10, 15).
	ID      string
	Expires *xsd.DateTime
	// Other are the child elements, of later releases.
	Other []*xmltree.Element
	// Attr are the attributes but functionalAliasID and expires.
	Attr []xml.Attr
}

// GroupStatus is a <groupStatus>: the status of the group (TS 23.280 §10.1.5.5), and the MCPTT ID of the user that
// last changed it (§9.2.2.3.2).
type GroupStatus struct {
	Value      GroupState
	ModifiedBy string
	// Attr are the attributes but ModifiedBy.
	Attr []xml.Attr
}

// GroupState is the value of a <groupStatus> (Table 9.3.1.2-3).
type GroupState string

const (
	InPeril   GroupState = "in-peril"
	Emergency GroupState = "emergency"
)

// AdditionalData is an <additionalData>.
type AdditionalData struct {
	// BroadcastAlias, RegroupAlias and CallOngoing are the groupBroadcastAlias, groupRegroupAlias and
	// groupCallOngoing attributes; "" and nil without one.
	BroadcastAlias string
	RegroupAlias   string
	CallOngoing    *bool
	// Attr are the other attributes.
	Attr []xml.Attr
}

// ErrValue is pidf.ErrValue: an element of the extension DecodePresence or DecodeStatus does not decode, malformed,
// is kept as one of the document is, and the result is usable without it.
var ErrValue = pidf.ErrValue

// DecodePresence decodes the extension of p from p.Other. Of an element given more than once, the first well-formed
// counts; the others, and the malformed, are not decoded, with an error wrapping ErrValue.
func DecodePresence(p pidf.Presence) (Presence, error) {
	x, _, err := decodePresence(p.Other)
	return x, err
}

// DecodeStatus decodes the extension of s from s.Other. Of an affiliation of a group and client given more than once,
// or of a functional alias, the first well-formed counts; the others, and the malformed, are not decoded, with an
// error wrapping ErrValue.
func DecodeStatus(s pidf.Status) (Status, error) {
	x, _, err := decodeStatus(s.Other)
	return x, err
}

// SetPresence replaces the elements of p.Other DecodePresence decodes with those of x, first, in the order of the
// schema; the others follow.
func SetPresence(p *pidf.Presence, x Presence) error {
	var es []*xmltree.Element

	if x.PID != "" {
		es = append(es, xmltree.New(Namespace, "p-id", xmltree.Text(x.PID)))
	}

	if g := x.GroupStatus; g != nil {
		e := &xmltree.Element{Name: name("groupStatus"), Children: []xmltree.Node{xmltree.Text(g.Value)}}

		var err error
		if e.Attr, err = attrs(g.Attr, "ModifiedBy", g.ModifiedBy); err != nil {
			return fmt.Errorf("pidfmc: <groupStatus>: %w", err)
		}

		es = append(es, e)
	}

	if a := x.AdditionalData; a != nil {
		var ongoing string
		if a.CallOngoing != nil {
			ongoing = xsd.FormatBoolean(*a.CallOngoing)
		}

		at, err := attrs(a.Attr, "groupBroadcastAlias", a.BroadcastAlias, "groupRegroupAlias", a.RegroupAlias,
			"groupCallOngoing", ongoing)
		if err != nil {
			return fmt.Errorf("pidfmc: <additionalData>: %w", err)
		}

		es = append(es, &xmltree.Element{Name: name("additionalData"), Attr: at})
	}

	_, used, _ := decodePresence(p.Other)
	p.Other = keep(es, p.Other, used)

	return nil
}

// SetStatus replaces the elements of s.Other DecodeStatus decodes with those of x, first, the affiliations then the
// functional aliases; the others follow.
func SetStatus(s *pidf.Status, x Status) error {
	var es []*xmltree.Element

	type key struct{ group, client string }

	seen := map[key]bool{}

	for _, a := range x.Affiliations {
		k := key{a.Group, a.Client}
		if seen[k] {
			return fmt.Errorf("pidfmc: affiliation of group %q and client %q twice", a.Group, a.Client)
		}

		seen[k] = true

		e, err := a.element()
		if err != nil {
			return err
		}

		es = append(es, e)
	}

	ids := map[string]bool{}

	for _, f := range x.FunctionalAliases {
		if ids[f.ID] {
			return fmt.Errorf("pidfmc: functional alias %q twice", f.ID)
		}

		ids[f.ID] = true

		e, err := f.element()
		if err != nil {
			return err
		}

		es = append(es, e)
	}

	_, used, _ := decodeStatus(s.Other)
	s.Other = keep(es, s.Other, used)

	return nil
}

// keep is es followed by the elements of old not used.
func keep(es, old []*xmltree.Element, used []bool) []*xmltree.Element {
	for i, e := range old {
		if !used[i] {
			es = append(es, e)
		}
	}

	return es
}

func (a *Affiliation) element() (*xmltree.Element, error) {
	switch a.Status {
	case "", Affiliating, Affiliated, Deaffiliating:
	default:
		return nil, fmt.Errorf("pidfmc: affiliation of group %q: status %q", a.Group, a.Status)
	}

	expires, err := dateTime(a.Expires)
	if err != nil {
		return nil, fmt.Errorf("pidfmc: affiliation of group %q: %w", a.Group, err)
	}

	at, err := attrs(a.Attr, "group", a.Group, "client", a.Client, "status", string(a.Status), "expires", expires)
	if err != nil {
		return nil, fmt.Errorf("pidfmc: affiliation of group %q: %w", a.Group, err)
	}

	return children("affiliation", at, a.Other)
}

func (f *FunctionalAlias) element() (*xmltree.Element, error) {
	expires, err := dateTime(f.Expires)
	if err != nil {
		return nil, fmt.Errorf("pidfmc: functional alias %q: %w", f.ID, err)
	}

	at, err := attrs(f.Attr, "functionalAliasID", f.ID, "expires", expires)
	if err != nil {
		return nil, fmt.Errorf("pidfmc: functional alias %q: %w", f.ID, err)
	}

	return children("functionalAlias", at, f.Other)
}

// children is the element local of attributes at and child elements other, of which none is to understand:
// DecodeStatus, which does not understand them, would not decode it.
func children(local string, at []xml.Attr, other []*xmltree.Element) (*xmltree.Element, error) {
	e := &xmltree.Element{Name: name(local), Attr: at}

	for _, c := range other {
		if pidf.MustUnderstand(c) {
			return nil, fmt.Errorf("pidfmc: <%s>: child <%s> to understand", local, c.Name.Local)
		}

		e.Children = append(e.Children, c)
	}

	return e, nil
}

func dateTime(d *xsd.DateTime) (string, error) {
	if d == nil {
		return "", nil
	}

	s := d.String()
	if s == "" {
		return "", fmt.Errorf("expires %s", d.Time)
	}

	return s, nil
}

// attrs is the attributes of the name and value pairs kv, of the values not empty, followed by other, which holds
// none of their names.
func attrs(other []xml.Attr, kv ...string) ([]xml.Attr, error) {
	var at []xml.Attr

	for i := 0; i < len(kv); i += 2 {
		if kv[i+1] != "" {
			at = append(at, xml.Attr{Name: xml.Name{Local: kv[i]}, Value: kv[i+1]})
		}
	}

	for _, a := range other {
		for i := 0; i < len(kv); i += 2 {
			if a.Name == (xml.Name{Local: kv[i]}) {
				return nil, fmt.Errorf("attribute %s in Attr", kv[i])
			}
		}
	}

	return append(at, other...), nil
}

func name(local string) xml.Name { return xml.Name{Space: Namespace, Local: local} }

type decoder struct {
	errs []error
}

func (d *decoder) fail(element, format string, a ...any) {
	d.errs = append(d.errs, fmt.Errorf("pidfmc: <%s>: %w: %s", element, ErrValue, fmt.Sprintf(format, a...)))
}

// known is the name of e in the namespace of the extension, or "".
func known(e *xmltree.Element) string {
	if e.Name.Space != Namespace {
		return ""
	}

	return e.Name.Local
}

// decodePresence decodes the extension of a presence from its elements es, and reports which it decoded.
func decodePresence(es []*xmltree.Element) (Presence, []bool, error) {
	var (
		d    decoder
		x    Presence
		seen = map[string]bool{}
		used = make([]bool, len(es))
	)

	for i, e := range es {
		n := known(e)

		switch n {
		case "p-id", "groupStatus", "additionalData":
		default:
			continue
		}

		if seen[n] {
			d.fail(n, "twice")
			continue
		}

		var ok bool

		switch n {
		case "p-id":
			// An empty <p-id> identifies no PUBLISH: kept, not decoded.
			if e.HasElements() {
				d.fail(n, "elements in a string")
			} else if x.PID = e.Text(); x.PID != "" {
				ok = true
			}
		case "groupStatus":
			x.GroupStatus = d.groupStatus(e)
			ok = x.GroupStatus != nil
		case "additionalData":
			x.AdditionalData = d.additionalData(e)
			ok = x.AdditionalData != nil
		}

		seen[n], used[i] = ok, ok
	}

	return x, used, errors.Join(d.errs...)
}

func (d *decoder) groupStatus(e *xmltree.Element) *GroupStatus {
	if e.HasElements() {
		d.fail("groupStatus", "elements in a string")
		return nil
	}

	g := &GroupStatus{Value: GroupState(e.Text())}

	for _, a := range e.Attr {
		if a.Name == (xml.Name{Local: "ModifiedBy"}) {
			g.ModifiedBy = xsd.Collapse(a.Value)
		} else {
			g.Attr = append(g.Attr, a)
		}
	}

	return g
}

// additionalData decodes an <additionalData>, of empty content (emptyType): not even white space.
func (d *decoder) additionalData(e *xmltree.Element) *AdditionalData {
	if len(e.Children) != 0 {
		d.fail("additionalData", "content in an empty element")
		return nil
	}

	x := &AdditionalData{}

	for _, a := range e.Attr {
		switch a.Name {
		case xml.Name{Local: "groupBroadcastAlias"}:
			x.BroadcastAlias = xsd.Collapse(a.Value)
		case xml.Name{Local: "groupRegroupAlias"}:
			x.RegroupAlias = xsd.Collapse(a.Value)
		case xml.Name{Local: "groupCallOngoing"}:
			v, ok := xsd.ParseBoolean(a.Value)
			if !ok {
				d.fail("additionalData", "groupCallOngoing %q", a.Value)
				return nil
			}

			x.CallOngoing = &v
		default:
			x.Attr = append(x.Attr, a)
		}
	}

	return x
}

// decodeStatus decodes the extension of a status from its elements es, and reports which it decoded.
func decodeStatus(es []*xmltree.Element) (Status, []bool, error) {
	type key struct{ group, client string }

	var (
		d       decoder
		x       Status
		groups  = map[key]bool{}
		aliases = map[string]bool{}
		used    = make([]bool, len(es))
	)

	for i, e := range es {
		switch known(e) {
		case "affiliation":
			a, ok := d.affiliation(e)
			if !ok {
				continue
			}

			k := key{a.Group, a.Client}
			if groups[k] {
				d.fail("affiliation", "of group %q and client %q twice", a.Group, a.Client)
				continue
			}

			groups[k] = true

			x.Affiliations, used[i] = append(x.Affiliations, a), true
		case "functionalAlias":
			f, ok := d.functionalAlias(e)
			if !ok {
				continue
			}

			if aliases[f.ID] {
				d.fail("functionalAlias", "%q twice", f.ID)
				continue
			}

			aliases[f.ID] = true
			x.FunctionalAliases, used[i] = append(x.FunctionalAliases, f), true
		}
	}

	return x, used, errors.Join(d.errs...)
}

func (d *decoder) affiliation(e *xmltree.Element) (Affiliation, bool) {
	other, ok := d.content(e)
	if !ok {
		return Affiliation{}, false
	}

	a := Affiliation{Other: other}

	for _, at := range e.Attr {
		switch at.Name {
		case xml.Name{Local: "group"}:
			a.Group = xsd.Collapse(at.Value)
		case xml.Name{Local: "client"}:
			a.Client = xsd.Collapse(at.Value)
		case xml.Name{Local: "status"}:
			// statusType restricts xs:string, of white space preserved.
			switch a.Status = AffiliationStatus(at.Value); a.Status {
			case Affiliating, Affiliated, Deaffiliating:
			default:
				d.fail("affiliation", "status %q", at.Value)
				return Affiliation{}, false
			}
		case xml.Name{Local: "expires"}:
			if a.Expires = d.dateTime("affiliation", at.Value); a.Expires == nil {
				return Affiliation{}, false
			}
		default:
			a.Attr = append(a.Attr, at)
		}
	}

	return a, true
}

func (d *decoder) functionalAlias(e *xmltree.Element) (FunctionalAlias, bool) {
	other, ok := d.content(e)
	if !ok {
		return FunctionalAlias{}, false
	}

	f := FunctionalAlias{Other: other}

	for _, at := range e.Attr {
		switch at.Name {
		case xml.Name{Local: "functionalAliasID"}:
			f.ID = xsd.Collapse(at.Value)
		case xml.Name{Local: "expires"}:
			if f.Expires = d.dateTime("functionalAlias", at.Value); f.Expires == nil {
				return FunctionalAlias{}, false
			}
		default:
			f.Attr = append(f.Attr, at)
		}
	}

	return f, true
}

// content is the child elements of e, of element-only content (xs:any): false if one is to understand, which it does
// not know, e unrecognized (RFC 3863 §4.2.3), or if e has character data.
func (d *decoder) content(e *xmltree.Element) ([]*xmltree.Element, bool) {
	other := e.Elements()

	for _, c := range other {
		if pidf.MustUnderstand(c) {
			return nil, false
		}
	}

	if xsd.Collapse(e.Text()) != "" {
		d.fail(e.Name.Local, "character data %q", e.Text())
		return nil, false
	}

	return other, true
}

// dateTime decodes an xs:dateTime; nil if malformed, or if of the year 10000 once 24:00:00 is the next day, which
// xsd.DateTime does not write.
func (d *decoder) dateTime(element, v string) *xsd.DateTime {
	t, err := xsd.ParseDateTime(v)
	if err != nil || t.String() == "" {
		d.fail(element, "expires %q", v)
		return nil
	}

	return &t
}
