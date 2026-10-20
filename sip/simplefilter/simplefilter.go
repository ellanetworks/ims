// Package simplefilter is the event notification filter document, application/simple-filter+xml (RFC 4661).
//
// Decode keeps the extension elements (RFC 4661 §4: the xs:any of other namespaces), and the elements it cannot
// decode, malformed or repeated, as Other, and the extension attributes (xs:anyAttribute) as Attr; Encode writes them
// back. The selections and references (§5) are kept as given: their evaluation is the notifier's.
package simplefilter

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
	ContentType = "application/simple-filter+xml"
	Namespace   = "urn:ietf:params:xml:ns:simple-filter"
)

// maxSize bounds a document: the filters of a subscription to a list of hundreds of resources fit.
const maxSize = 1 << 16

// FilterSet is the <filter-set> root element (§3.2): of one filter at least.
type FilterSet struct {
	// Package is the event package the filters apply to, empty without one.
	Package string
	// NSBindings bind the prefixes of the selections and references to namespaces (§3.3, §5): nil without
	// <ns-bindings>.
	NSBindings []NSBinding
	Filters    []Filter
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order: those of the simple-filter namespace unknown, malformed or
	// repeated, elements of other namespaces, which the schema does not allow there.
	Other []*xmltree.Element
}

// NSBinding is an <ns-binding> (§3.3).
type NSBinding struct {
	Prefix string
	URN    string
}

// Filter is a <filter> (§3.4).
type Filter struct {
	// ID is unique among the filters of the filter set.
	ID string
	// URI is that of the resource the filter applies to, empty without one: the resource of the subscription.
	URI string
	// Domain is that of the resources the filter applies to, empty without one; not with a URI.
	Domain string
	// Remove asks for the removal of the filter of the ID.
	Remove bool
	// Enabled is nil without an enabled attribute: enabled.
	Enabled  *bool
	What     *What
	Triggers []Trigger
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order: the extensions, and the elements of the simple-filter
	// namespace unknown, malformed or repeated.
	Other []*xmltree.Element
}

// What is a <what> (§3.5): the content to deliver.
type What struct {
	Includes []Selector
	Excludes []Selector
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// Selector is an <include> (§3.5.1) or <exclude> (§3.5.2).
type Selector struct {
	// Type is empty without a type attribute: XPath.
	Type SelectorType
	// Value is a selection (§5) or a namespace URI, as given, white space included.
	Value string
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
}

// SelectorType is the value of a type attribute (§3.5.3).
type SelectorType string

const (
	XPath          SelectorType = "xpath"
	NamespaceValue SelectorType = "namespace"
)

// Trigger is a <trigger> (§3.6): the changes all of which deliver the content, at least one.
type Trigger struct {
	Changed []Changed
	// Added and Removed are references (§5) to elements, as given.
	Added   []string
	Removed []string
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// Changed is a <changed> (§3.6.1).
type Changed struct {
	// Reference is the reference (§5) to the element or attribute, as given.
	Reference string
	// From and To are nil without their attributes.
	From, To *string
	// By is a decimal, empty without a by attribute; with one, From and To are decimals too (§3.6.1.4).
	By string
	// Attr are the attributes of other namespaces: the extensions.
	Attr []xml.Attr
}

// ErrValue is wrapped by the errors of the elements Decode does not decode, malformed (RFC 4661 §7): the document it
// returns is usable without them, and keeps them.
var ErrValue = errors.New("malformed element")

// Decode decodes the document b. A malformed element is kept in the Other of its parent, with an error wrapping
// ErrValue: a filter without ID or of an ID not unique, of both a URI and a domain (§3.4), a trigger without a
// change (§3.6), a change by an amount from or to values not decimals (§3.6.1.4). An element given more than the
// schema allows is kept the same way, the first counting, malformed or not. A filter set without a filter decoded
// (§3.2) is an error.
func Decode(b []byte) (FilterSet, error) {
	if len(b) > maxSize {
		return FilterSet{}, fmt.Errorf("simplefilter: document of %d bytes exceeds %d", len(b), maxSize)
	}

	root, err := xmltree.Parse(b)
	if err != nil {
		return FilterSet{}, fmt.Errorf("simplefilter: %w", err)
	}

	if root.Name != name("filter-set") {
		return FilterSet{}, fmt.Errorf("simplefilter: root element <%s> of %q", root.Name.Local, root.Name.Space)
	}

	var d decoder

	fs := FilterSet{Attr: extensions(root)}
	fs.Package, _ = root.AttrValue("", "package")
	hasBindings := false

	for _, e := range root.Elements() {
		switch known(e) {
		case "ns-bindings":
			if d.once(e, &hasBindings) {
				var ok bool
				if fs.NSBindings, ok = d.nsBindings(e); ok {
					continue
				}
			}
		case "filter":
			f, ok := d.filter(e)
			if ok && !slices.ContainsFunc(fs.Filters, func(o Filter) bool { return o.ID == f.ID }) {
				fs.Filters = append(fs.Filters, f)
				continue
			}

			if ok {
				d.fail("filter", "id %q twice", f.ID)
			}
		}

		fs.Other = append(fs.Other, e)
	}

	if len(fs.Filters) == 0 {
		return FilterSet{}, errors.New("simplefilter: <filter-set> without filter")
	}

	return fs, errors.Join(d.errs...)
}

type decoder struct {
	errs []error
}

func (d *decoder) fail(element, format string, a ...any) {
	d.errs = append(d.errs, fmt.Errorf("simplefilter: <%s>: %w: %s", element, ErrValue, fmt.Sprintf(format, a...)))
}

// once reports whether e is the first of its name among its siblings, which the schema allows once, given telling
// whether one was already: a repeated one is not, with an error.
func (d *decoder) once(e *xmltree.Element, given *bool) bool {
	if *given {
		d.fail(e.Name.Local, "given more than once")
		return false
	}

	*given = true

	return true
}

// known is the name of e in the simple-filter namespace, or "".
func known(e *xmltree.Element) string {
	if e.Name.Space != Namespace {
		return ""
	}

	return e.Name.Local
}

// extensions are the attributes of e of a namespace, other than simple-filter's: the extensions of the schema's
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

// nsBindings decodes an <ns-bindings>: one or more <ns-binding> of a prefix and a URN, and nothing else.
func (d *decoder) nsBindings(e *xmltree.Element) ([]NSBinding, bool) {
	var bs []NSBinding

	for _, c := range e.Elements() {
		prefix, hasPrefix := c.AttrValue("", "prefix")
		urn, hasURN := c.AttrValue("", "urn")

		if known(c) != "ns-binding" || !hasPrefix || !hasURN || c.HasElements() {
			d.fail("ns-bindings", "<%s>", c.Name.Local)
			return nil, false
		}

		bs = append(bs, NSBinding{Prefix: prefix, URN: xsd.Collapse(urn)})
	}

	if len(bs) == 0 {
		d.fail("ns-bindings", "no ns-binding")
		return nil, false
	}

	return bs, true
}

func (d *decoder) filter(e *xmltree.Element) (Filter, bool) {
	id, ok := e.AttrValue("", "id")
	if !ok {
		d.fail("filter", "without id")
		return Filter{}, false
	}

	f := Filter{ID: id, Attr: extensions(e)}

	uri, hasURI := e.AttrValue("", "uri")
	domain, hasDomain := e.AttrValue("", "domain")

	if hasURI && hasDomain {
		d.fail("filter", "id %q of a uri and a domain", id)
		return Filter{}, false
	}

	f.URI, f.Domain = xsd.Collapse(uri), domain

	if v, ok := e.AttrValue("", "remove"); ok {
		if f.Remove, ok = xsd.ParseBoolean(v); !ok {
			d.fail("filter", "remove %q", v)
			return Filter{}, false
		}
	}

	if v, ok := e.AttrValue("", "enabled"); ok {
		b, ok := xsd.ParseBoolean(v)
		if !ok {
			d.fail("filter", "enabled %q", v)
			return Filter{}, false
		}

		f.Enabled = &b
	}

	hasWhat := false

	for _, c := range e.Elements() {
		switch known(c) {
		case "what":
			if d.once(c, &hasWhat) {
				w := d.what(c)
				f.What = &w

				continue
			}
		case "trigger":
			if t, ok := d.trigger(c); ok {
				f.Triggers = append(f.Triggers, t)
				continue
			}
		}

		f.Other = append(f.Other, c)
	}

	return f, true
}

func (d *decoder) what(e *xmltree.Element) What {
	var w What

	for _, c := range e.Elements() {
		switch known(c) {
		case "include":
			if s, ok := d.selector(c); ok {
				w.Includes = append(w.Includes, s)
				continue
			}
		case "exclude":
			if s, ok := d.selector(c); ok {
				w.Excludes = append(w.Excludes, s)
				continue
			}
		}

		w.Other = append(w.Other, c)
	}

	return w
}

func (d *decoder) selector(e *xmltree.Element) (Selector, bool) {
	s := Selector{Attr: extensions(e)}

	if v, ok := e.AttrValue("", "type"); ok {
		if s.Type = SelectorType(xsd.Collapse(v)); s.Type != XPath && s.Type != NamespaceValue {
			d.fail(e.Name.Local, "type %q", v)
			return Selector{}, false
		}
	}

	if !d.text(e, &s.Value) {
		return Selector{}, false
	}

	return s, true
}

// trigger decodes a <trigger>: at least one change (§3.6).
func (d *decoder) trigger(e *xmltree.Element) (Trigger, bool) {
	var t Trigger

	for _, c := range e.Elements() {
		var s string

		switch known(c) {
		case "changed":
			if ch, ok := d.changed(c); ok {
				t.Changed = append(t.Changed, ch)
				continue
			}
		case "added":
			if d.text(c, &s) {
				t.Added = append(t.Added, s)
				continue
			}
		case "removed":
			if d.text(c, &s) {
				t.Removed = append(t.Removed, s)
				continue
			}
		}

		t.Other = append(t.Other, c)
	}

	if len(t.Changed)+len(t.Added)+len(t.Removed) == 0 {
		d.fail("trigger", "no change")
		return Trigger{}, false
	}

	return t, true
}

func (d *decoder) changed(e *xmltree.Element) (Changed, bool) {
	c := Changed{Attr: extensions(e)}

	if v, ok := e.AttrValue("", "from"); ok {
		c.From = &v
	}

	if v, ok := e.AttrValue("", "to"); ok {
		c.To = &v
	}

	if v, ok := e.AttrValue("", "by"); ok {
		if c.By = xsd.Collapse(v); !isDecimal(c.By) {
			d.fail("changed", "by %q", v)
			return Changed{}, false
		}
	}

	if err := c.check(); err != nil {
		d.fail("changed", "%v", err)
		return Changed{}, false
	}

	if !d.text(e, &c.Reference) {
		return Changed{}, false
	}

	return c, true
}

// check checks that, with a by, from and to are decimals, of the type of by (§3.6.1.4).
func (c *Changed) check() error {
	if c.By == "" {
		return nil
	}

	if !isDecimal(c.By) {
		return fmt.Errorf("by %q", c.By)
	}

	for _, v := range []*string{c.From, c.To} {
		if v != nil && !isDecimal(xsd.Collapse(*v)) {
			return fmt.Errorf("by %q from or to %q", c.By, *v)
		}
	}

	return nil
}

// isDecimal reports whether s is an xs:decimal (XML Schema Part 2 §3.2.3.1): a sign or not, digits and a period or
// not, at least one digit.
func isDecimal(s string) bool {
	if s != "" && (s[0] == '+' || s[0] == '-') {
		s = s[1:]
	}

	whole, frac, _ := strings.Cut(s, ".")

	return whole+frac != "" && strings.Trim(whole+frac, "0123456789") == ""
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

// Encode encodes fs, in the order of the schema (§7): the elements of Other after those decoded, where the schema's
// extension point is.
func Encode(fs FilterSet) ([]byte, error) {
	if len(fs.Filters) == 0 {
		return nil, errors.New("simplefilter: filter set without filter")
	}

	root := &xmltree.Element{Name: name("filter-set")}
	if fs.Package != "" {
		root.Attr = append(root.Attr, attr("package", fs.Package))
	}

	if err := setExtensions(root, fs.Attr); err != nil {
		return nil, err
	}

	if len(fs.NSBindings) > 0 {
		e := xmltree.New(Namespace, "ns-bindings")
		for _, b := range fs.NSBindings {
			e.Children = append(e.Children, &xmltree.Element{
				Name: name("ns-binding"), Attr: []xml.Attr{attr("prefix", b.Prefix), attr("urn", b.URN)},
			})
		}

		root.Children = append(root.Children, e)
	}

	var ids []string

	for _, f := range fs.Filters {
		if slices.Contains(ids, f.ID) {
			return nil, fmt.Errorf("simplefilter: filter id %q twice", f.ID)
		}

		ids = append(ids, f.ID)

		e, err := f.element()
		if err != nil {
			return nil, err
		}

		root.Children = append(root.Children, e)
	}

	root.Children = appendElements(root.Children, fs.Other)

	b, err := xmltree.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("simplefilter: %w", err)
	}

	return b, nil
}

func (f *Filter) element() (*xmltree.Element, error) {
	if f.URI != "" && f.Domain != "" {
		return nil, fmt.Errorf("simplefilter: filter %q of a uri and a domain", f.ID)
	}

	e := &xmltree.Element{Name: name("filter"), Attr: []xml.Attr{attr("id", f.ID)}}
	if f.URI != "" {
		e.Attr = append(e.Attr, attr("uri", f.URI))
	}

	if f.Domain != "" {
		e.Attr = append(e.Attr, attr("domain", f.Domain))
	}

	if f.Remove {
		e.Attr = append(e.Attr, attr("remove", "true"))
	}

	if f.Enabled != nil {
		e.Attr = append(e.Attr, attr("enabled", xsd.FormatBoolean(*f.Enabled)))
	}

	if err := setExtensions(e, f.Attr); err != nil {
		return nil, err
	}

	if w := f.What; w != nil {
		x := xmltree.New(Namespace, "what")

		for _, s := range []struct {
			local     string
			selectors []Selector
		}{{"include", w.Includes}, {"exclude", w.Excludes}} {
			for _, sel := range s.selectors {
				y := &xmltree.Element{Name: name(s.local), Children: text(sel.Value)}

				switch sel.Type {
				case "":
				case XPath, NamespaceValue:
					y.Attr = append(y.Attr, attr("type", string(sel.Type)))
				default:
					return nil, fmt.Errorf("simplefilter: filter %q: %s type %q", f.ID, s.local, sel.Type)
				}

				if err := setExtensions(y, sel.Attr); err != nil {
					return nil, err
				}

				x.Children = append(x.Children, y)
			}
		}

		x.Children = appendElements(x.Children, w.Other)
		e.Children = append(e.Children, x)
	}

	for _, t := range f.Triggers {
		if len(t.Changed)+len(t.Added)+len(t.Removed) == 0 {
			return nil, fmt.Errorf("simplefilter: filter %q: trigger without change", f.ID)
		}

		x := xmltree.New(Namespace, "trigger")

		for _, c := range t.Changed {
			if err := c.check(); err != nil {
				return nil, fmt.Errorf("simplefilter: filter %q: %w", f.ID, err)
			}

			y := &xmltree.Element{Name: name("changed"), Children: text(c.Reference)}
			if c.From != nil {
				y.Attr = append(y.Attr, attr("from", *c.From))
			}

			if c.To != nil {
				y.Attr = append(y.Attr, attr("to", *c.To))
			}

			if c.By != "" {
				y.Attr = append(y.Attr, attr("by", c.By))
			}

			if err := setExtensions(y, c.Attr); err != nil {
				return nil, err
			}

			x.Children = append(x.Children, y)
		}

		for _, s := range t.Added {
			x.Children = append(x.Children, &xmltree.Element{Name: name("added"), Children: text(s)})
		}

		for _, s := range t.Removed {
			x.Children = append(x.Children, &xmltree.Element{Name: name("removed"), Children: text(s)})
		}

		x.Children = appendElements(x.Children, t.Other)
		e.Children = append(e.Children, x)
	}

	e.Children = appendElements(e.Children, f.Other)

	return e, nil
}

// setExtensions sets the extension attributes as of e: of a namespace, other than simple-filter's.
func setExtensions(e *xmltree.Element, as []xml.Attr) error {
	for _, a := range as {
		if a.Name.Space == "" || a.Name.Space == Namespace {
			return fmt.Errorf("simplefilter: <%s>: extension attribute {%s}%s", e.Name.Local, a.Name.Space, a.Name.Local)
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

func appendElements(ns []xmltree.Node, es []*xmltree.Element) []xmltree.Node {
	for _, e := range es {
		ns = append(ns, e)
	}

	return ns
}

func name(local string) xml.Name { return xml.Name{Space: Namespace, Local: local} }
