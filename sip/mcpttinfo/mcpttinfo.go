// Package mcpttinfo is the MCPTT information document, application/vnd.3gpp.mcptt-info+xml (TS 24.379 Annex F.1).
//
// Decode keeps what it does not know, elements of other namespaces, of later releases, and attributes, and Encode
// writes it back, so a function that sets an element of a document it relays passes the others on (F.1.3); so it
// does of the elements it cannot decode, malformed or repeated. A body relayed unchanged is copied as received, not
// decoded: so is a signed one (TS 24.379 §4.8), whose signature an encoding would break.
package mcpttinfo

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strings"

	"github.com/ellanetworks/ims/sip/xmltree"
)

const (
	ContentType = "application/vnd.3gpp.mcptt-info+xml"
	Namespace   = "urn:3gpp:ns:mcpttInfo:1.0"
)

// encryptedData is the element of encrypted content (F.1.3 3b).
var encryptedData = xml.Name{Space: "http://www.w3.org/2001/04/xmlenc#", Local: "EncryptedData"}

// maxSize bounds a document: an access token (TS 24.482) and encrypted elements fit.
const maxSize = 1 << 16

// SessionType is the value of <session-type> (F.1.3).
type SessionType string

const (
	SessionChat             SessionType = "chat"
	SessionPrearranged      SessionType = "prearranged"
	SessionPrivate          SessionType = "private"
	SessionFirstToAnswer    SessionType = "first-to-answer"
	SessionAmbientListening SessionType = "ambient-listening"
	SessionAdhoc            SessionType = "adhoc"
)

// FloorState is the value of <floor-state> (F.1.3).
type FloorState string

const (
	FloorIdle  FloorState = "floor-idle"
	FloorTaken FloorState = "floor-taken"
)

// Info is the <mcpttinfo> root element.
type Info struct {
	Params *Params
	// Other are the child elements not decoded, in order: of other namespaces, unknown, or repeated.
	Other []*xmltree.Element
	// Ext are the children of <anyExt>.
	Ext  []*xmltree.Element
	Attr []xml.Attr
}

// Params is the <mcptt-Params> element. A nil or empty field is an absent element.
type Params struct {
	AccessToken       *Content
	SessionType       SessionType
	RequestURI        *Content
	CallingUserID     *Content
	CalledPartyID     *Content
	CallingGroupID    *Content
	Required          *Content
	EmergencyInd      *Content
	AlertInd          *Content
	ImminentPerilInd  *Content
	BroadcastInd      *bool
	MCOrg             string
	FloorState        FloorState
	AssociatedGroupID *Content
	OriginatedBy      *Content
	// MKFCGKTPs is the <MKFC-GKTPs> element, kept as received: its content is TS 24.481 §7.4 security.
	MKFCGKTPs    *xmltree.Element
	ClientID     *Content
	AlertIndRcvd *Content
	GWMCPTTUsage *bool
	// Other are the child elements not decoded, in order: of other namespaces, unknown, malformed or repeated.
	Other []*xmltree.Element
	// Ext are the children of <anyExt>.
	Ext  []*xmltree.Element
	Attr []xml.Attr
}

// Protection is the "type" attribute of a contentType element.
type Protection string

const (
	Normal    Protection = "Normal"
	Encrypted Protection = "Encrypted"
)

// Kind is the choice of a contentType element.
type Kind int

const (
	KindNone Kind = iota
	KindURI
	KindString
	KindBoolean
	// KindOther is an element of another namespace or <anyExt>: <xenc:EncryptedData> for encrypted content, <anyExt>
	// for a value of a later release in the clear.
	KindOther
)

var kindNames = [...]string{KindURI: "mcpttURI", KindString: "mcpttString", KindBoolean: "mcpttBoolean"}

// Content is an element of contentType: a value as an <mcpttURI>, <mcpttString> or <mcpttBoolean>, or encrypted.
type Content struct {
	// Type is Normal or Encrypted; Decode takes content without it as in the clear, and Encode writes it so.
	Type Protection
	Kind Kind
	// Value is the text of an <mcpttURI> or <mcpttString>, "true" or "false" for an <mcpttBoolean>.
	Value string
	// Element is the element of KindOther.
	Element *xmltree.Element
	// Attr are the attributes but "type".
	Attr []xml.Attr
}

// URI is content of an <mcpttURI> in the clear.
func URI(v string) *Content { return &Content{Type: Normal, Kind: KindURI, Value: v} }

// Text is content of an <mcpttString> in the clear.
func Text(v string) *Content { return &Content{Type: Normal, Kind: KindString, Value: v} }

// Bool is content of an <mcpttBoolean> in the clear.
func Bool(v bool) *Content {
	return &Content{Type: Normal, Kind: KindBoolean, Value: boolText(v)}
}

// EncryptedContent is encrypted content: el is its <xenc:EncryptedData> (F.1.3 3).
func EncryptedContent(el *xmltree.Element) *Content {
	return &Content{Type: Encrypted, Kind: KindOther, Element: el}
}

// ExtContent is the element of contentType local of c, for the <anyExt> of mcptt-Params: an element of a later
// release, such as <multiple-devices-ind> (F.1.2, F.1.3).
func ExtContent(local string, c *Content) *xmltree.Element {
	return c.element(local)
}

// URI is the value of an <mcpttURI> in the clear.
func (c *Content) URI() (string, bool) { return c.clear(KindURI) }

// Text is the value of an <mcpttString> in the clear.
func (c *Content) Text() (string, bool) { return c.clear(KindString) }

// Bool is the value of an <mcpttBoolean> in the clear: an absent element is not one of "false", as an
// <emergency-ind> cancelling an emergency call (F.1.3 8b).
func (c *Content) Bool() (v, ok bool) {
	t, ok := c.clear(KindBoolean)
	return t == "true", ok
}

func (c *Content) clear(k Kind) (string, bool) {
	if c == nil || c.Kind != k || c.Type == Encrypted {
		return "", false
	}

	return c.Value, true
}

// ErrValue is wrapped by the errors of the elements Decode does not decode, malformed: the document it returns is
// usable without them, and keeps them.
var ErrValue = errors.New("malformed element")

// Decode decodes the document b. A malformed element of mcptt-Params is kept in Other, with an error wrapping
// ErrValue; of an element given twice, the first well-formed counts and the others are kept in Other.
func Decode(b []byte) (Info, error) {
	if len(b) > maxSize {
		return Info{}, fmt.Errorf("mcpttinfo: document of %d bytes exceeds %d", len(b), maxSize)
	}

	root, err := xmltree.Parse(b)
	if err != nil {
		return Info{}, fmt.Errorf("mcpttinfo: %w", err)
	}

	if root.Name != name("mcpttinfo") {
		return Info{}, fmt.Errorf("mcpttinfo: root element <%s> of %q", root.Name.Local, root.Name.Space)
	}

	var d decoder

	info := Info{Attr: root.Attr}

	for _, e := range root.Elements() {
		switch known(e) {
		case "mcptt-Params":
			if info.Params == nil {
				info.Params = d.params(e)
			} else {
				info.Other = append(info.Other, e)
			}
		case "anyExt":
			info.Ext = append(info.Ext, e.Elements()...)
		default:
			info.Other = append(info.Other, e)
		}
	}

	return info, errors.Join(d.errs...)
}

type decoder struct {
	errs []error
}

// known is the name of e in the namespace of the document, or "".
func known(e *xmltree.Element) string {
	if e.Name.Space != Namespace {
		return ""
	}

	return e.Name.Local
}

func (d *decoder) fail(name, format string, a ...any) {
	d.errs = append(d.errs, fmt.Errorf("mcpttinfo: <%s>: %w: %s", name, ErrValue, fmt.Sprintf(format, a...)))
}

func (d *decoder) params(e *xmltree.Element) *Params {
	p := &Params{Attr: e.Attr}
	seen := map[string]bool{}

	for _, c := range e.Elements() {
		name := known(c)
		if name == "anyExt" {
			p.Ext = append(p.Ext, c.Elements()...)
			continue
		}

		f, ok := paramFields[name]
		if !ok {
			p.Other = append(p.Other, c)
			continue
		}

		if seen[name] || !d.field(p, f, c) {
			p.Other = append(p.Other, c)
			continue
		}

		seen[name] = true
	}

	return p
}

// field decodes c as f into p, and reports whether it did: c is malformed otherwise.
func (d *decoder) field(p *Params, f any, c *xmltree.Element) bool {
	switch f := f.(type) {
	case contentField:
		v := d.content(c, f.kind)
		if v == nil {
			return false
		}

		*f.get(p) = v
	case stringField:
		if c.HasElements() {
			d.fail(c.Name.Local, "elements in a string")
			return false
		}

		f.set(p, c.Text())
	case boolField:
		v, ok := d.boolean(c)
		if !ok {
			return false
		}

		*f.get(p) = &v
	case elementField:
		*f.get(p) = c
	}

	return true
}

// content decodes c as contentType, of kind want in the clear (F.1.3 2), any for KindNone.
func (d *decoder) content(c *xmltree.Element, want Kind) *Content {
	name := c.Name.Local
	v := &Content{}

	for _, a := range c.Attr {
		if a.Name.Space == "" && a.Name.Local == "type" {
			v.Type = Protection(a.Value)
		} else {
			v.Attr = append(v.Attr, a)
		}
	}

	switch v.Type {
	case "":
		v.Type = Normal
	case Normal, Encrypted:
	default:
		d.fail(name, "type %q", v.Type)
		return nil
	}

	children := c.Elements()
	if len(children) > 1 {
		d.fail(name, "%d choices", len(children))
		return nil
	}

	if len(children) == 1 {
		ch := children[0]

		switch known(ch) {
		case "mcpttURI":
			v.Kind = KindURI
		case "mcpttString":
			v.Kind = KindString
		case "mcpttBoolean":
			v.Kind = KindBoolean
		default:
			v.Kind, v.Element = KindOther, ch
		}

		if v.Kind != KindOther {
			if ch.HasElements() {
				d.fail(name, "elements in <%s>", ch.Name.Local)
				return nil
			}

			v.Value = ch.Text()
		}

		if v.Kind == KindURI {
			v.Value = collapse(v.Value)
		}

		if v.Kind == KindBoolean {
			b, ok := parseBoolean(v.Value)
			if !ok {
				d.fail(name, "mcpttBoolean %q", v.Value)
				return nil
			}

			v.Value = boolText(b)
		}
	}

	if err := v.check(want); err != nil {
		d.fail(name, "%v", err)
		return nil
	}

	return v
}

func (d *decoder) boolean(c *xmltree.Element) (bool, bool) {
	b, ok := parseBoolean(c.Text())
	if !ok || c.HasElements() {
		d.fail(c.Name.Local, "boolean %q", c.Text())
	}

	return b, ok && !c.HasElements()
}

// parseBoolean parses an xs:boolean, its whitespace collapsed.
func parseBoolean(s string) (bool, bool) {
	switch collapse(s) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}

	return false, false
}

// collapse is s without the leading and trailing white space the whiteSpace facet collapse of XML Schema Part 2
// §4.3.6 removes from xs:anyURI and xs:boolean values: #x20, #x9, #xA and #xD, not other Unicode spaces.
func collapse(s string) string { return strings.Trim(s, " \t\n\r") }

func boolText(b bool) string {
	if b {
		return "true"
	}

	return "false"
}

// check verifies c against the rules of F.1.3 2 and 3 for an element whose value in the clear is of kind want, any
// kind for KindNone. A Content without the optional type is taken as in the clear.
func (c *Content) check(want Kind) error {
	if c.Kind == KindOther && c.Element == nil {
		return errors.New("no element")
	}

	switch {
	case c.Type != "" && c.Type != Normal && c.Type != Encrypted:
		return fmt.Errorf("type %q", c.Type)
	case c.Type == Encrypted:
		if c.Kind != KindOther || c.Element.Name != encryptedData {
			return errors.New("encrypted without <xenc:EncryptedData>")
		}
	case c.Kind == KindOther && c.Element.Name == name("anyExt"):
		// The extension of contentType (F.1.2), for a value of a later release.
	case want != KindNone && c.Kind != want:
		if c.Kind == KindOther {
			return fmt.Errorf("<%s> in the clear, want %s", c.Element.Name.Local, kindNames[want])
		}

		if c.Kind == KindNone {
			return fmt.Errorf("empty, want %s", kindNames[want])
		}

		return fmt.Errorf("%s, want %s", kindNames[c.Kind], kindNames[want])
	}

	if c.Kind == KindBoolean && c.Value != "true" && c.Value != "false" {
		return fmt.Errorf("mcpttBoolean %q", c.Value)
	}

	return nil
}

// Encode encodes info, mcptt-Params in the order of the schema, then the elements of other namespaces, then anyExt.
func Encode(info Info) ([]byte, error) {
	root := &xmltree.Element{Name: name("mcpttinfo"), Attr: info.Attr}

	if p := info.Params; p != nil {
		e, err := p.element()
		if err != nil {
			return nil, err
		}

		root.Children = append(root.Children, e)
	}

	root.Children = appendElements(root.Children, info.Other)
	root.Children = appendExt(root.Children, info.Ext)

	b, err := xmltree.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("mcpttinfo: %w", err)
	}

	return b, nil
}

func (p *Params) element() (*xmltree.Element, error) {
	e := &xmltree.Element{Name: name("mcptt-Params"), Attr: p.Attr}

	for _, n := range paramOrder {
		var c *xmltree.Element

		switch f := paramFields[n].(type) {
		case contentField:
			v := *f.get(p)
			if v == nil {
				continue
			}

			if err := v.check(f.kind); err != nil {
				return nil, fmt.Errorf("mcpttinfo: <%s>: %w", n, err)
			}

			c = v.element(n)
		case stringField:
			if s := f.value(p); s != "" {
				c = xmltree.New(Namespace, n, xmltree.Text(s))
			}
		case boolField:
			if v := *f.get(p); v != nil {
				c = xmltree.New(Namespace, n, xmltree.Text(boolText(*v)))
			}
		case elementField:
			if v := *f.get(p); v != nil {
				c = &xmltree.Element{Name: name(n), Attr: v.Attr, Children: v.Children}
			}
		}

		if c != nil {
			e.Children = append(e.Children, c)
		}
	}

	e.Children = appendElements(e.Children, p.Other)
	e.Children = appendExt(e.Children, p.Ext)

	return e, nil
}

func (c *Content) element(n string) *xmltree.Element {
	t := c.Type
	if t == "" {
		t = Normal
	}

	e := &xmltree.Element{Name: name(n), Attr: append([]xml.Attr{{Name: xml.Name{Local: "type"}, Value: string(t)}}, c.Attr...)}

	switch c.Kind {
	case KindURI, KindString, KindBoolean:
		e.Children = []xmltree.Node{xmltree.New(Namespace, kindNames[c.Kind], xmltree.Text(c.Value))}
	case KindOther:
		e.Children = []xmltree.Node{c.Element}
	}

	return e
}

func appendElements(ns []xmltree.Node, es []*xmltree.Element) []xmltree.Node {
	for _, e := range es {
		ns = append(ns, e)
	}

	return ns
}

func appendExt(ns []xmltree.Node, ext []*xmltree.Element) []xmltree.Node {
	if len(ext) == 0 {
		return ns
	}

	return append(ns, &xmltree.Element{Name: name("anyExt"), Children: appendElements(nil, ext)})
}

func name(local string) xml.Name { return xml.Name{Space: Namespace, Local: local} }

// The elements of mcptt-Params, in the order of the schema, and the kind of value of each in the clear (F.1.3 2).
type (
	contentField struct {
		kind Kind
		get  func(*Params) **Content
	}
	stringField struct {
		value func(*Params) string
		set   func(*Params, string)
	}
	boolField    struct{ get func(*Params) **bool }
	elementField struct {
		get func(*Params) **xmltree.Element
	}
)

var paramOrder = []string{
	"mcptt-access-token", "session-type", "mcptt-request-uri", "mcptt-calling-user-id", "mcptt-called-party-id",
	"mcptt-calling-group-id", "required", "emergency-ind", "alert-ind", "imminentperil-ind", "broadcast-ind", "mc-org",
	"floor-state", "associated-group-id", "originated-by", "MKFC-GKTPs", "mcptt-client-id", "alert-ind-rcvd",
	"gw-mcptt-usage",
}

var paramFields = map[string]any{
	"mcptt-access-token":     contentField{KindString, func(p *Params) **Content { return &p.AccessToken }},
	"session-type":           stringField{func(p *Params) string { return string(p.SessionType) }, func(p *Params, s string) { p.SessionType = SessionType(collapse(s)) }},
	"mcptt-request-uri":      contentField{KindURI, func(p *Params) **Content { return &p.RequestURI }},
	"mcptt-calling-user-id":  contentField{KindURI, func(p *Params) **Content { return &p.CallingUserID }},
	"mcptt-called-party-id":  contentField{KindURI, func(p *Params) **Content { return &p.CalledPartyID }},
	"mcptt-calling-group-id": contentField{KindURI, func(p *Params) **Content { return &p.CallingGroupID }},
	"required":               contentField{KindNone, func(p *Params) **Content { return &p.Required }},
	"emergency-ind":          contentField{KindBoolean, func(p *Params) **Content { return &p.EmergencyInd }},
	"alert-ind":              contentField{KindBoolean, func(p *Params) **Content { return &p.AlertInd }},
	"imminentperil-ind":      contentField{KindBoolean, func(p *Params) **Content { return &p.ImminentPerilInd }},
	"broadcast-ind":          boolField{func(p *Params) **bool { return &p.BroadcastInd }},
	"mc-org":                 stringField{func(p *Params) string { return p.MCOrg }, func(p *Params, s string) { p.MCOrg = s }},
	"floor-state":            stringField{func(p *Params) string { return string(p.FloorState) }, func(p *Params, s string) { p.FloorState = FloorState(collapse(s)) }},
	"associated-group-id":    contentField{KindURI, func(p *Params) **Content { return &p.AssociatedGroupID }},
	"originated-by":          contentField{KindURI, func(p *Params) **Content { return &p.OriginatedBy }},
	"MKFC-GKTPs":             elementField{func(p *Params) **xmltree.Element { return &p.MKFCGKTPs }},
	"mcptt-client-id":        contentField{KindString, func(p *Params) **Content { return &p.ClientID }},
	"alert-ind-rcvd":         contentField{KindBoolean, func(p *Params) **Content { return &p.AlertIndRcvd }},
	"gw-mcptt-usage":         boolField{func(p *Params) **bool { return &p.GWMCPTTUsage }},
}
