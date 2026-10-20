// Package xmltree is an XML element tree for the documents of SIP bodies: it keeps the elements, attributes and
// namespaces a document format does not know, its extensions and those of later versions, so that a document decoded
// encodes with them (RFC 3863 §4.2, TS 24.379 F.1.3), and an extension decodes from them.
//
// Names carry namespace URIs, not prefixes: Parse resolves the prefixes of a document and Marshal declares its own.
// Character data and attribute values are as XML 1.0 gives them to the application: references replaced, line ends
// and attribute values normalized.
package xmltree

import (
	"encoding/xml"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Node is a child of an element: an *Element or Text.
type Node interface{ node() }

// Text is character data.
type Text string

func (Text) node() {}

// Element is an XML element. Attr holds no namespace declarations: names hold the namespaces they resolve to.
type Element struct {
	Name     xml.Name
	Attr     []xml.Attr
	Children []Node
}

func (*Element) node() {}

// New returns the element space:local with children.
func New(space, local string, children ...Node) *Element {
	return &Element{Name: xml.Name{Space: space, Local: local}, Children: children}
}

// Text is the character data of e's children, without that of their descendants.
func (e *Element) Text() string {
	var b strings.Builder

	for _, c := range e.Children {
		if t, ok := c.(Text); ok {
			b.WriteString(string(t))
		}
	}

	return b.String()
}

// Elements are e's child elements, in order.
func (e *Element) Elements() []*Element {
	var es []*Element

	for _, c := range e.Children {
		if c, ok := c.(*Element); ok {
			es = append(es, c)
		}
	}

	return es
}

// HasElements reports whether e has child elements.
func (e *Element) HasElements() bool {
	for _, c := range e.Children {
		if _, ok := c.(*Element); ok {
			return true
		}
	}

	return false
}

// AttrValue is the value of e's attribute space:local.
func (e *Element) AttrValue(space, local string) (string, bool) {
	for _, a := range e.Attr {
		if a.Name.Space == space && a.Name.Local == local {
			return a.Value, true
		}
	}

	return "", false
}

// Clone is a deep copy of e.
func (e *Element) Clone() *Element {
	c := &Element{Name: e.Name, Attr: append([]xml.Attr(nil), e.Attr...)}

	for _, n := range e.Children {
		if el, ok := n.(*Element); ok {
			n = el.Clone()
		}

		c.Children = append(c.Children, n)
	}

	return c
}

// XMLNamespace is the namespace of the xml prefix (Namespaces in XML 1.0 §3).
const XMLNamespace = "http://www.w3.org/XML/1998/namespace"

// Marshal is the document of root, in UTF-8 with an XML declaration. Each element declares its namespace as the
// default one where it changes; attributes of other namespaces get prefixes Marshal chooses. An element of the xml
// or xmlns namespace, an attribute of the xmlns namespace or named xmlns, two attributes of one name, or a name or
// character XML does not allow is an error.
func Marshal(root *Element) ([]byte, error) {
	w := writer{buf: []byte(xml.Header)}
	if err := w.element(root, scope{}); err != nil {
		return nil, fmt.Errorf("xmltree: %w", err)
	}

	return w.buf, nil
}

type writer struct {
	buf      []byte
	prefixes int
}

func (w *writer) element(e *Element, sc scope) error {
	if !isNCName(e.Name.Local) {
		return fmt.Errorf("element name %q", e.Name.Local)
	}

	if e.Name.Space == XMLNamespace || e.Name.Space == xmlnsURL || !validChars(e.Name.Space) {
		return fmt.Errorf("element <%s> of namespace %q", e.Name.Local, e.Name.Space)
	}

	w.buf = append(w.buf, '<')
	w.buf = append(w.buf, e.Name.Local...)

	if e.Name.Space != sc.def {
		sc.def = e.Name.Space
		w.attr("xmlns", sc.def)
	}

	declared := false

	for i, a := range e.Attr {
		if !isNCName(a.Name.Local) || a.Name.Space == "" && a.Name.Local == "xmlns" || a.Name.Space == xmlnsURL ||
			!validChars(a.Name.Space) {
			return fmt.Errorf("attribute {%s}%s of <%s>", a.Name.Space, a.Name.Local, e.Name.Local)
		}

		for _, prev := range e.Attr[:i] {
			if prev.Name == a.Name {
				return fmt.Errorf("attribute {%s}%s of <%s> twice", a.Name.Space, a.Name.Local, e.Name.Local)
			}
		}

		name := a.Name.Local

		switch a.Name.Space {
		case "":
		case XMLNamespace:
			name = "xml:" + name
		default:
			p, ok := sc.prefix[a.Name.Space]
			if !ok {
				if !declared {
					sc.prefix = clone(sc.prefix)
					declared = true
				}

				p = fmt.Sprintf("ns%d", w.prefixes)
				w.prefixes++
				sc.prefix[a.Name.Space] = p
				w.attr("xmlns:"+p, a.Name.Space)
			}

			name = p + ":" + name
		}

		if !validChars(a.Value) {
			return fmt.Errorf("attribute %s of <%s>: invalid character", a.Name.Local, e.Name.Local)
		}

		w.attr(name, a.Value)
	}

	if len(e.Children) == 0 {
		w.buf = append(w.buf, "/>"...)
		return nil
	}

	w.buf = append(w.buf, '>')

	for _, c := range e.Children {
		switch c := c.(type) {
		case Text:
			if !validChars(string(c)) {
				return fmt.Errorf("text of <%s>: invalid character", e.Name.Local)
			}

			w.escape(string(c), false)
		case *Element:
			if err := w.element(c, sc); err != nil {
				return err
			}
		}
	}

	w.buf = append(w.buf, "</"...)
	w.buf = append(w.buf, e.Name.Local...)
	w.buf = append(w.buf, '>')

	return nil
}

// clone is a copy of the prefixes of a scope, keyed by prefix in Parse and by namespace in Marshal.
func clone(m map[string]string) map[string]string {
	c := make(map[string]string, len(m)+1)
	for k, v := range m {
		c[k] = v
	}

	return c
}

func (w *writer) attr(name, value string) {
	w.buf = append(w.buf, ' ')
	w.buf = append(w.buf, name...)
	w.buf = append(w.buf, `="`...)
	w.escape(value, true)
	w.buf = append(w.buf, '"')
}

// escape writes s with the characters that markup, line-end handling (§2.11) or attribute-value normalization
// (§3.3.3) would change as references.
func (w *writer) escape(s string, attr bool) {
	for _, r := range s {
		switch {
		case r == '&':
			w.buf = append(w.buf, "&amp;"...)
		case r == '<':
			w.buf = append(w.buf, "&lt;"...)
		case r == '>':
			w.buf = append(w.buf, "&gt;"...)
		case r == '\r':
			w.buf = append(w.buf, "&#xD;"...)
		case attr && r == '"':
			w.buf = append(w.buf, "&quot;"...)
		case attr && r == '\n':
			w.buf = append(w.buf, "&#xA;"...)
		case attr && r == '\t':
			w.buf = append(w.buf, "&#x9;"...)
		default:
			w.buf = utf8.AppendRune(w.buf, r)
		}
	}
}

// validChars reports whether s is UTF-8 of the characters of XML 1.0 §2.2.
func validChars(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}

	for _, r := range s {
		if !isChar(r) {
			return false
		}
	}

	return true
}

// isChar is the Char production (§2.2).
func isChar(r rune) bool {
	return r == 0x09 || r == 0x0A || r == 0x0D || r >= 0x20 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD ||
		r >= 0x10000 && r <= 0x10FFFF
}

// isNameStartChar is the NameStartChar production (§2.3).
func isNameStartChar(r rune) bool {
	switch {
	case r == ':' || r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z':
		return true
	case r < 0xC0:
		return false
	}

	for _, rg := range nameStartRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}

	return false
}

var nameStartRanges = [][2]rune{
	{0xC0, 0xD6},
	{0xD8, 0xF6},
	{0xF8, 0x2FF},
	{0x370, 0x37D},
	{0x37F, 0x1FFF},
	{0x200C, 0x200D},
	{0x2070, 0x218F},
	{0x2C00, 0x2FEF},
	{0x3001, 0xD7FF},
	{0xF900, 0xFDCF},
	{0xFDF0, 0xFFFD},
	{0x10000, 0xEFFFF},
}

// isNameChar is the NameChar production (§2.3).
func isNameChar(r rune) bool {
	return isNameStartChar(r) || r == '-' || r == '.' || r >= '0' && r <= '9' || r == 0xB7 ||
		r >= 0x300 && r <= 0x36F || r == 0x203F || r == 0x2040
}

// isNCName is the NCName production of Namespaces in XML 1.0 §3: a Name without a colon.
func isNCName(s string) bool {
	for i, r := range s {
		if r == ':' || !isNameChar(r) || i == 0 && !isNameStartChar(r) {
			return false
		}
	}

	return s != ""
}
