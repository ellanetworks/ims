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

var encryptedData = xml.Name{Space: "http://www.w3.org/2001/04/xmlenc#", Local: "EncryptedData"}

const maxSize = 1 << 16

type SessionType string

const (
	SessionChat             SessionType = "chat"
	SessionPrearranged      SessionType = "prearranged"
	SessionPrivate          SessionType = "private"
	SessionFirstToAnswer    SessionType = "first-to-answer"
	SessionAmbientListening SessionType = "ambient-listening"
	SessionAdhoc            SessionType = "adhoc"
)

type FloorState string

const (
	FloorIdle  FloorState = "floor-idle"
	FloorTaken FloorState = "floor-taken"
)

type Info struct {
	Params *Params
	Other  []*xmltree.Element
	Ext    []*xmltree.Element
	Attr   []xml.Attr
}

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
	MKFCGKTPs         *xmltree.Element
	ClientID          *Content
	AlertIndRcvd      *Content
	GWMCPTTUsage      *bool
	Other             []*xmltree.Element
	Ext               []*xmltree.Element
	Attr              []xml.Attr
}

type Protection string

const (
	Normal    Protection = "Normal"
	Encrypted Protection = "Encrypted"
)

type Kind int

const (
	KindNone Kind = iota
	KindURI
	KindString
	KindBoolean
	KindOther
)

var kindNames = [...]string{KindURI: "mcpttURI", KindString: "mcpttString", KindBoolean: "mcpttBoolean"}

type Content struct {
	Type    Protection
	Kind    Kind
	Value   string
	Element *xmltree.Element
	Attr    []xml.Attr
}

func URI(v string) *Content { return &Content{Type: Normal, Kind: KindURI, Value: v} }

func Text(v string) *Content { return &Content{Type: Normal, Kind: KindString, Value: v} }

func Bool(v bool) *Content {
	return &Content{Type: Normal, Kind: KindBoolean, Value: boolText(v)}
}

func EncryptedContent(el *xmltree.Element) *Content {
	return &Content{Type: Encrypted, Kind: KindOther, Element: el}
}

func ExtContent(local string, c *Content) *xmltree.Element {
	return c.element(local)
}

func (c *Content) URI() (string, bool) { return c.clear(KindURI) }

func (c *Content) Text() (string, bool) { return c.clear(KindString) }

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

var ErrValue = errors.New("malformed element")

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

func parseBoolean(s string) (bool, bool) {
	switch collapse(s) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}

	return false, false
}

// XML Schema Part 2 section 4.3.6
func collapse(s string) string { return strings.Trim(s, " \t\n\r") }

func boolText(b bool) string {
	if b {
		return "true"
	}

	return "false"
}

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
