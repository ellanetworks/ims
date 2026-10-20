// Package resourcelists is the resource lists document, application/resource-lists+xml (RFC 4826 §3), with the
// copy control attributes of RFC 5364 §4: the URI list of a URI-list service (RFC 5366 §4).
//
// Decode keeps the extension elements (RFC 4826 §6: the xs:any of other namespaces), and the elements it cannot
// decode, malformed or repeated, as Other, and the extension attributes (xs:anyAttribute) as Attr; Encode writes them
// back. An extension, such as TS 24.379 §17.5's <functional-alias> of an <entry>, decodes from them and sets them.
//
// The keys of the members of a list, unique among their siblings (§3.1), are not to be so in a document Decode
// decodes: RFC 5364 §4 gives the handling of the entries of one URI. Encode requires them unique.
//
// A <display-name> of no text nor language decodes as none, and the zero value encodes as no element.
package resourcelists

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
	ContentType = "application/resource-lists+xml"
	Namespace   = "urn:ietf:params:xml:ns:resource-lists"
	// CopyControlNamespace is that of the copy control attributes (RFC 5364 §5, §9.2).
	CopyControlNamespace = "urn:ietf:params:xml:ns:copycontrol"
	// AnonymousURI is the URI of the anonymized entries of a recipient-history list (RFC 5364 §4).
	AnonymousURI = "sip:anonymous@anonymous.invalid"
)

// maxSize bounds a document: a list of thousands of URIs fits.
const maxSize = 1 << 18

// ResourceLists is the <resource-lists> root element (§3.1).
type ResourceLists struct {
	Lists []List
	// Other are the child elements not decoded, in order: elements other than lists, malformed lists.
	Other []*xmltree.Element
}

// List is a <list> (§3.1): a resource list.
type List struct {
	// Name is the handle of the list, empty without one.
	Name        string
	DisplayName DisplayName
	// Members are the entries, entry references, external lists and lists of the list, in order.
	Members []Member
	Copy
	// Attr are the attributes of other namespaces but the copy control ones: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order: the extensions, and the elements of the resource-lists
	// namespace unknown, malformed or repeated.
	Other []*xmltree.Element
}

// Member is a member of a list: an *Entry, *EntryRef, *External or *List.
type Member interface {
	element() (*xmltree.Element, error)
}

// Entries are the entries of l, without those of its lists.
func (l *List) Entries() []*Entry {
	var es []*Entry

	for _, m := range l.Members {
		if e, ok := m.(*Entry); ok {
			es = append(es, e)
		}
	}

	return es
}

// Entry is an <entry> (§3.1): a resource.
type Entry struct {
	URI         string
	DisplayName DisplayName
	Copy
	// Attr are the attributes of other namespaces but the copy control ones: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// EntryRef is an <entry-ref> (§3.1): an entry by reference, meaningful in a document of XCAP.
type EntryRef struct {
	// Ref is the relative path, from the XCAP root, of an entry.
	Ref         string
	DisplayName DisplayName
	Copy
	// Attr are the attributes of other namespaces but the copy control ones: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// External is an <external> (§3.1): a list by reference, meaningful in a document of XCAP.
type External struct {
	// Anchor is the absolute HTTP URI of the list, mandatory (§3.1; the schema of §3.2 leaves it optional).
	Anchor      string
	DisplayName DisplayName
	Copy
	// Attr are the attributes of other namespaces but the copy control ones: the extensions.
	Attr []xml.Attr
	// Other are the child elements not decoded, in order.
	Other []*xmltree.Element
}

// DisplayName is a <display-name>, its language the xml:lang of the element, if any; zero without one.
type DisplayName struct {
	Text string
	Lang string
}

// Copy are the copy control attributes of a member of a list (RFC 5364 §4).
type Copy struct {
	// CopyControl is the copy control level, empty without a copyControl attribute: bcc.
	CopyControl CopyControl
	// Anonymize is the anonymize attribute: whether a URI-list server hides the URI in recipient-history lists.
	Anonymize bool
	// Count is the number of equal URIs: nil without a count attribute, 1.
	Count *uint64
}

// CopyControl is the value of a copyControl attribute.
type CopyControl string

const (
	To  CopyControl = "to"
	CC  CopyControl = "cc"
	BCC CopyControl = "bcc"
)

// ErrValue is wrapped by the errors of the elements Decode does not decode, malformed (RFC 4826 §3.2): the document
// it returns is usable without them, and keeps them.
var ErrValue = errors.New("malformed element")

// Decode decodes the document b. A malformed element is kept in the Other of its parent, with an error wrapping
// ErrValue: a member without its URI attribute, or of a copy control attribute malformed. An element given more than
// the schema allows is kept the same way, the first counting, malformed or not, but a <display-name> of no text nor
// language, as none. Members of a key given twice are kept, as members.
func Decode(b []byte) (ResourceLists, error) {
	if len(b) > maxSize {
		return ResourceLists{}, fmt.Errorf("resourcelists: document of %d bytes exceeds %d", len(b), maxSize)
	}

	root, err := xmltree.Parse(b)
	if err != nil {
		return ResourceLists{}, fmt.Errorf("resourcelists: %w", err)
	}

	if root.Name != name("resource-lists") {
		return ResourceLists{}, fmt.Errorf("resourcelists: root element <%s> of %q", root.Name.Local, root.Name.Space)
	}

	var (
		d decoder
		r ResourceLists
	)

	for _, e := range root.Elements() {
		if known(e) == "list" {
			if l, ok := d.list(e); ok {
				r.Lists = append(r.Lists, l)
				continue
			}
		}

		r.Other = append(r.Other, e)
	}

	return r, errors.Join(d.errs...)
}

type decoder struct {
	errs []error
}

func (d *decoder) fail(element, format string, a ...any) {
	d.errs = append(d.errs, fmt.Errorf("resourcelists: <%s>: %w: %s", element, ErrValue, fmt.Sprintf(format, a...)))
}

// known is the name of e in the resource-lists namespace, or "".
func known(e *xmltree.Element) string {
	if e.Name.Space != Namespace {
		return ""
	}

	return e.Name.Local
}

func (d *decoder) list(e *xmltree.Element) (List, bool) {
	var l List

	l.Name, _ = e.AttrValue("", "name")

	var ok bool
	if l.Copy, l.Attr, ok = d.attributes(e); !ok {
		return List{}, false
	}

	hasName := false

	for _, c := range e.Elements() {
		var m Member

		switch known(c) {
		case "display-name":
			if d.displayName(c, &l.DisplayName, &hasName) {
				continue
			}
		case "list":
			if x, ok := d.list(c); ok {
				m = &x
			}
		case "entry":
			if x, ok := d.entry(c); ok {
				m = &x
			}
		case "entry-ref":
			if x, ok := d.entryRef(c); ok {
				m = &x
			}
		case "external":
			if x, ok := d.external(c); ok {
				m = &x
			}
		}

		if m != nil {
			l.Members = append(l.Members, m)
			continue
		}

		l.Other = append(l.Other, c)
	}

	return l, true
}

func (d *decoder) entry(e *xmltree.Element) (Entry, bool) {
	uri, ok := e.AttrValue("", "uri")
	if !ok {
		d.fail("entry", "without uri")
		return Entry{}, false
	}

	x := Entry{URI: xsd.Collapse(uri)}
	if x.Copy, x.Attr, ok = d.attributes(e); !ok {
		return Entry{}, false
	}

	x.DisplayName, x.Other = d.children(e)

	return x, true
}

func (d *decoder) entryRef(e *xmltree.Element) (EntryRef, bool) {
	ref, ok := e.AttrValue("", "ref")
	if !ok {
		d.fail("entry-ref", "without ref")
		return EntryRef{}, false
	}

	x := EntryRef{Ref: xsd.Collapse(ref)}
	if x.Copy, x.Attr, ok = d.attributes(e); !ok {
		return EntryRef{}, false
	}

	x.DisplayName, x.Other = d.children(e)

	return x, true
}

func (d *decoder) external(e *xmltree.Element) (External, bool) {
	anchor, ok := e.AttrValue("", "anchor")
	if !ok {
		d.fail("external", "without anchor")
		return External{}, false
	}

	x := External{Anchor: xsd.Collapse(anchor)}
	if x.Copy, x.Attr, ok = d.attributes(e); !ok {
		return External{}, false
	}

	x.DisplayName, x.Other = d.children(e)

	return x, true
}

// children decodes the content of an entry, entry reference or external list: a display name, the extensions.
func (d *decoder) children(e *xmltree.Element) (DisplayName, []*xmltree.Element) {
	var (
		n       DisplayName
		hasName bool
		other   []*xmltree.Element
	)

	for _, c := range e.Elements() {
		if known(c) == "display-name" && d.displayName(c, &n, &hasName) {
			continue
		}

		other = append(other, c)
	}

	return n, other
}

// displayName decodes the <display-name> of a list or member into v, given telling whether one was already: the
// first counts, malformed or not, but one of no text nor language, which Encode does not write, as none.
func (d *decoder) displayName(e *xmltree.Element, v *DisplayName, given *bool) bool {
	if *given {
		d.fail("display-name", "given more than once")
		return false
	}

	*given = true

	if e.HasElements() {
		d.fail("display-name", "elements in a string")
		return false
	}

	lang, _ := e.AttrValue(xmltree.XMLNamespace, "lang")
	*v = DisplayName{Text: e.Text(), Lang: lang}
	*given = *v != DisplayName{}

	return true
}

// attributes decodes the copy control attributes of a member of a list, and its other extension attributes.
func (d *decoder) attributes(e *xmltree.Element) (Copy, []xml.Attr, bool) {
	var (
		c  Copy
		as []xml.Attr
		ok bool
	)

	for _, a := range e.Attr {
		v := xsd.Collapse(a.Value)

		switch {
		case a.Name.Space == "" || a.Name.Space == Namespace:
		case a.Name.Space != CopyControlNamespace:
			as = append(as, a)
		case a.Name.Local == "copyControl":
			if c.CopyControl = CopyControl(v); c.CopyControl != To && c.CopyControl != CC && c.CopyControl != BCC {
				d.fail(e.Name.Local, "copyControl %q", a.Value)
				return Copy{}, nil, false
			}
		case a.Name.Local == "anonymize":
			if c.Anonymize, ok = xsd.ParseBoolean(v); !ok {
				d.fail(e.Name.Local, "anonymize %q", a.Value)
				return Copy{}, nil, false
			}
		case a.Name.Local == "count":
			n, ok := parseNonNegativeInteger(v)
			if !ok {
				d.fail(e.Name.Local, "count %q", a.Value)
				return Copy{}, nil, false
			}

			c.Count = &n
		default:
			as = append(as, a)
		}
	}

	return c, as, true
}

// parseNonNegativeInteger parses an xs:nonNegativeInteger (XML Schema Part 2 §3.3.20.1), of 64 bits: decimal
// digits, after a "+" or not, or after a "-" of zero.
func parseNonNegativeInteger(s string) (uint64, bool) {
	s = xsd.Collapse(s)

	digits := strings.TrimPrefix(s, "+")
	if rest, ok := strings.CutPrefix(s, "-"); ok {
		if digits = rest; strings.Trim(digits, "0") != "" {
			return 0, false
		}
	}

	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, false
	}

	n, err := strconv.ParseUint(digits, 10, 64)

	return n, err == nil
}

// errDuplicate is wrapped by the errors of Encode of a key not unique among its siblings.
var errDuplicate = errors.New("not unique")

// Encode encodes r, in the order of the schema (§3.2): the elements of Other after those decoded, where the schema's
// extension point is. The keys of the members of a list are to be unique among their siblings (§3.1).
func Encode(r ResourceLists) ([]byte, error) {
	root := &xmltree.Element{Name: name("resource-lists")}

	lists := make([]Member, len(r.Lists))
	for i := range r.Lists {
		lists[i] = &r.Lists[i]
	}

	if err := unique(lists); err != nil {
		return nil, fmt.Errorf("resourcelists: <resource-lists>: %w", err)
	}

	for _, l := range r.Lists {
		e, err := l.element()
		if err != nil {
			return nil, err
		}

		root.Children = append(root.Children, e)
	}

	root.Children = appendElements(root.Children, r.Other)

	b, err := xmltree.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("resourcelists: %w", err)
	}

	return b, nil
}

func (l *List) element() (*xmltree.Element, error) {
	if l == nil {
		return nil, errors.New("resourcelists: nil list")
	}

	e := &xmltree.Element{Name: name("list")}
	if l.Name != "" {
		e.Attr = []xml.Attr{{Name: xml.Name{Local: "name"}, Value: l.Name}}
	}

	if err := setAttributes(e, l.Copy, l.Attr); err != nil {
		return nil, err
	}

	e.Children = appendDisplayName(e.Children, l.DisplayName)

	if err := unique(l.Members); err != nil {
		return nil, fmt.Errorf("resourcelists: list %q: %w", l.Name, err)
	}

	for _, m := range l.Members {
		if m == nil {
			return nil, fmt.Errorf("resourcelists: list %q: nil member", l.Name)
		}

		x, err := m.element()
		if err != nil {
			return nil, err
		}

		e.Children = append(e.Children, x)
	}

	e.Children = appendElements(e.Children, l.Other)

	return e, nil
}

// unique checks that the keys of the members ms of a list are unique among them (§3.1), by case-sensitive string
// comparison: the names of lists, of those of a name, the URIs of entries, the refs of entry references, the anchors
// of external lists. The entries of AnonymousURI are keyed by their copy control level too: a recipient-history list
// has one per level (RFC 5364 §6, Figure 4).
func unique(ms []Member) error {
	keys := map[[2]string]bool{}

	for _, m := range ms {
		var key [2]string

		switch x := m.(type) {
		case *List:
			if x == nil || x.Name == "" {
				continue
			}

			key = [2]string{"list name", x.Name}
		case *Entry:
			if x == nil {
				continue
			}

			key = [2]string{"entry uri", x.URI}

			if level := x.CopyControl; x.URI == AnonymousURI {
				if level == "" {
					level = BCC
				}

				key[1] += " of " + string(level)
			}
		case *EntryRef:
			if x == nil {
				continue
			}

			key = [2]string{"entry-ref ref", x.Ref}
		case *External:
			if x == nil {
				continue
			}

			key = [2]string{"external anchor", x.Anchor}
		default:
			continue
		}

		if keys[key] {
			return fmt.Errorf("%s %q: %w", key[0], key[1], errDuplicate)
		}

		keys[key] = true
	}

	return nil
}

func (x *Entry) element() (*xmltree.Element, error) {
	if x == nil {
		return nil, errors.New("resourcelists: nil entry")
	}

	return member("entry", "uri", x.URI, x.DisplayName, x.Copy, x.Attr, x.Other)
}

func (x *EntryRef) element() (*xmltree.Element, error) {
	if x == nil {
		return nil, errors.New("resourcelists: nil entry-ref")
	}

	return member("entry-ref", "ref", x.Ref, x.DisplayName, x.Copy, x.Attr, x.Other)
}

func (x *External) element() (*xmltree.Element, error) {
	if x == nil {
		return nil, errors.New("resourcelists: nil external")
	}

	return member("external", "anchor", x.Anchor, x.DisplayName, x.Copy, x.Attr, x.Other)
}

// member is the element of an entry, entry reference or external list.
func member(local, attr, value string, n DisplayName, c Copy, as []xml.Attr, other []*xmltree.Element) (*xmltree.Element, error) {
	e := &xmltree.Element{Name: name(local), Attr: []xml.Attr{{Name: xml.Name{Local: attr}, Value: value}}}
	if err := setAttributes(e, c, as); err != nil {
		return nil, err
	}

	e.Children = appendDisplayName(e.Children, n)
	e.Children = appendElements(e.Children, other)

	return e, nil
}

// setAttributes sets the copy control attributes c of e, and the extension attributes as: of a namespace, other than
// resource-lists's, not of copy control.
func setAttributes(e *xmltree.Element, c Copy, as []xml.Attr) error {
	cc := func(local, value string) xml.Attr {
		return xml.Attr{Name: xml.Name{Space: CopyControlNamespace, Local: local}, Value: value}
	}

	switch c.CopyControl {
	case "":
	case To, CC, BCC:
		e.Attr = append(e.Attr, cc("copyControl", string(c.CopyControl)))
	default:
		return fmt.Errorf("resourcelists: <%s>: copyControl %q", e.Name.Local, c.CopyControl)
	}

	if c.Anonymize {
		e.Attr = append(e.Attr, cc("anonymize", "true"))
	}

	if c.Count != nil {
		e.Attr = append(e.Attr, cc("count", strconv.FormatUint(*c.Count, 10)))
	}

	for _, a := range as {
		if a.Name.Space == "" || a.Name.Space == Namespace || a.Name.Space == CopyControlNamespace &&
			(a.Name.Local == "copyControl" || a.Name.Local == "anonymize" || a.Name.Local == "count") {
			return fmt.Errorf("resourcelists: <%s>: extension attribute {%s}%s", e.Name.Local, a.Name.Space, a.Name.Local)
		}
	}

	e.Attr = append(e.Attr, as...)

	return nil
}

func appendDisplayName(ns []xmltree.Node, n DisplayName) []xmltree.Node {
	if n == (DisplayName{}) {
		return ns
	}

	e := xmltree.New(Namespace, "display-name")
	if n.Text != "" {
		e.Children = []xmltree.Node{xmltree.Text(n.Text)}
	}

	if n.Lang != "" {
		e.Attr = []xml.Attr{{Name: xml.Name{Space: xmltree.XMLNamespace, Local: "lang"}, Value: n.Lang}}
	}

	return append(ns, e)
}

func appendElements(ns []xmltree.Node, es []*xmltree.Element) []xmltree.Node {
	for _, e := range es {
		ns = append(ns, e)
	}

	return ns
}

func name(local string) xml.Name { return xml.Name{Space: Namespace, Local: local} }
