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

type Presence struct {
	PID            string
	GroupStatus    *GroupStatus
	AdditionalData *AdditionalData
}

type Status struct {
	Affiliations      []Affiliation
	FunctionalAliases []FunctionalAlias
}

type Affiliation struct {
	Group   string
	Client  string
	Status  AffiliationStatus
	Expires *xsd.DateTime
	Other   []*xmltree.Element
	Attr    []xml.Attr
}

type AffiliationStatus string

const (
	Affiliating   AffiliationStatus = "affiliating"
	Affiliated    AffiliationStatus = "affiliated"
	Deaffiliating AffiliationStatus = "deaffiliating"
)

type FunctionalAlias struct {
	ID      string
	Expires *xsd.DateTime
	Other   []*xmltree.Element
	Attr    []xml.Attr
}

// TS 23.280 clause 10.1.5.5
type GroupStatus struct {
	Value      GroupState
	ModifiedBy string
	Attr       []xml.Attr
}

type GroupState string

const (
	InPeril   GroupState = "in-peril"
	Emergency GroupState = "emergency"
)

type AdditionalData struct {
	BroadcastAlias string
	RegroupAlias   string
	CallOngoing    *bool
	Attr           []xml.Attr
}

var ErrValue = pidf.ErrValue

func DecodePresence(p pidf.Presence) (Presence, error) {
	x, _, err := decodePresence(p.Other)
	return x, err
}

func DecodeStatus(s pidf.Status) (Status, error) {
	x, _, err := decodeStatus(s.Other)
	return x, err
}

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

func known(e *xmltree.Element) string {
	if e.Name.Space != Namespace {
		return ""
	}

	return e.Name.Local
}

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

// RFC 3863 section 4.2.3
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

func (d *decoder) dateTime(element, v string) *xsd.DateTime {
	t, err := xsd.ParseDateTime(v)
	if err != nil || t.String() == "" {
		d.fail(element, "expires %q", v)
		return nil
	}

	return &t
}
