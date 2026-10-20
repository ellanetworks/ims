package xmltree

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// xmlnsURL is the namespace of the xmlns prefix (Namespaces in XML 1.0 §3).
const xmlnsURL = "http://www.w3.org/2000/xmlns/"

// maxDepth bounds the nesting of a parsed document.
const maxDepth = 64

// Parse parses the document b into its root element: a well-formed XML 1.0 document (XML 1.0 §2.1) that is
// namespace-well-formed (Namespaces in XML 1.0 §7), in UTF-8, with or without a byte order mark (§4.3.3). A document
// type declaration is refused: no entity but the predefined ones (§4.6) is then declared. Comments and processing
// instructions are left out; line ends are normalized (§2.11), attribute values too (§3.3.3, as of type CDATA).
func Parse(b []byte) (*Element, error) {
	if !utf8.Valid(b) {
		return nil, errors.New("xmltree: not UTF-8")
	}

	s := strings.TrimPrefix(string(b), "\xef\xbb\xbf")
	if strings.ContainsRune(s, '\r') {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	}

	p := parser{s: s}

	root, err := p.document()
	if err != nil {
		return nil, fmt.Errorf("xmltree: offset %d: %w", p.pos, err)
	}

	return root, nil
}

type parser struct {
	s   string
	pos int
}

func (p *parser) rest() string { return p.s[p.pos:] }

func (p *parser) eat(prefix string) bool {
	if strings.HasPrefix(p.rest(), prefix) {
		p.pos += len(prefix)
		return true
	}

	return false
}

// space skips S (§2.3) and reports whether there was any.
func (p *parser) space() bool {
	start := p.pos
	for p.pos < len(p.s) && isSpace(p.s[p.pos]) {
		p.pos++
	}

	return p.pos > start
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// document is document ::= prolog element Misc* (§2.1), the prolog without a doctypedecl.
func (p *parser) document() (*Element, error) {
	if strings.HasPrefix(p.rest(), "<?xml") && len(p.rest()) > 5 && (isSpace(p.s[p.pos+5]) || p.s[p.pos+5] == '?') {
		if err := p.xmlDecl(); err != nil {
			return nil, err
		}
	}

	if err := p.misc(); err != nil {
		return nil, err
	}

	if strings.HasPrefix(p.rest(), "<!DOCTYPE") {
		return nil, errors.New("document type declaration refused")
	}

	if !strings.HasPrefix(p.rest(), "<") {
		return nil, errors.New("no root element")
	}

	root, err := p.element(scope{})
	if err != nil {
		return nil, err
	}

	if err := p.misc(); err != nil {
		return nil, err
	}

	if p.pos != len(p.s) {
		return nil, errors.New("content after the root element")
	}

	return root, nil
}

// xmlDecl is XMLDecl (§2.8, §4.3.3): version 1.x, an encoding of UTF-8, which US-ASCII is a subset of.
func (p *parser) xmlDecl() error {
	p.pos += len("<?xml")

	attrs := map[string]string{}

	var order []string

	for {
		hadSpace := p.space()
		if p.eat("?>") {
			break
		}

		if !hadSpace {
			return errors.New("malformed XML declaration")
		}

		name, err := p.name()
		if err != nil {
			return err
		}

		v, err := p.declValue()
		if err != nil {
			return err
		}

		if _, dup := attrs[name]; dup {
			return fmt.Errorf("XML declaration %s twice", name)
		}

		attrs[name] = v
		order = append(order, name)
	}

	want := []string{"version", "encoding", "standalone"}
	for _, n := range order {
		for len(want) > 0 && want[0] != n {
			want = want[1:]
		}

		if len(want) == 0 {
			return fmt.Errorf("XML declaration %s", n)
		}
	}

	v, ok := attrs["version"]
	if !ok || len(order) == 0 || order[0] != "version" || !strings.HasPrefix(v, "1.") || !isDigits(v[2:]) {
		return fmt.Errorf("XML version %q", v)
	}

	if enc, ok := attrs["encoding"]; ok {
		switch strings.ToLower(enc) {
		case "utf-8":
		case "us-ascii":
			for i := range len(p.s) {
				if p.s[i] >= utf8.RuneSelf {
					return errors.New("non-ASCII character in a US-ASCII document")
				}
			}
		default:
			return fmt.Errorf("encoding %q", enc)
		}
	}

	if sd, ok := attrs["standalone"]; ok && sd != "yes" && sd != "no" {
		return fmt.Errorf("standalone %q", sd)
	}

	return nil
}

func isDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return s != ""
}

// declValue is Eq and a quoted value of the XML declaration.
func (p *parser) declValue() (string, error) {
	p.space()

	if !p.eat("=") {
		return "", errors.New("malformed XML declaration")
	}

	p.space()

	if p.pos >= len(p.s) || p.s[p.pos] != '"' && p.s[p.pos] != '\'' {
		return "", errors.New("malformed XML declaration")
	}

	q := p.s[p.pos]
	p.pos++

	end := strings.IndexByte(p.rest(), q)
	if end < 0 {
		return "", errors.New("malformed XML declaration")
	}

	v := p.s[p.pos : p.pos+end]
	p.pos += end + 1

	return v, nil
}

// misc skips Misc* (§2.8): comments, processing instructions, white space.
func (p *parser) misc() error {
	for {
		p.space()

		switch {
		case strings.HasPrefix(p.rest(), "<!--"):
			if err := p.comment(); err != nil {
				return err
			}
		case strings.HasPrefix(p.rest(), "<?"):
			if err := p.pi(); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

// comment skips a Comment (§2.5).
func (p *parser) comment() error {
	p.pos += len("<!--")

	end := strings.Index(p.rest(), "--")
	if end < 0 || !strings.HasPrefix(p.rest()[end:], "-->") {
		return errors.New("malformed comment")
	}

	if err := checkChars(p.rest()[:end]); err != nil {
		return err
	}

	p.pos += end + len("-->")

	return nil
}

// pi skips a PI (§2.6); a target of "xml" in any case is reserved, an XML declaration after the start.
func (p *parser) pi() error {
	p.pos += len("<?")

	target, err := p.name()
	if err != nil {
		return err
	}

	if strings.EqualFold(target, "xml") {
		return errors.New("XML declaration not at the start")
	}

	if p.eat("?>") {
		return nil
	}

	if !p.space() {
		return errors.New("malformed processing instruction")
	}

	end := strings.Index(p.rest(), "?>")
	if end < 0 {
		return errors.New("malformed processing instruction")
	}

	if err := checkChars(p.rest()[:end]); err != nil {
		return err
	}

	p.pos += end + len("?>")

	return nil
}

// name is a Name (§2.3).
func (p *parser) name() (string, error) {
	start := p.pos

	for p.pos < len(p.s) {
		r, n := utf8.DecodeRuneInString(p.rest())
		if !isNameChar(r) || p.pos == start && !isNameStartChar(r) {
			break
		}

		p.pos += n
	}

	if p.pos == start {
		return "", errors.New("name expected")
	}

	return p.s[start:p.pos], nil
}

// scope is the namespaces in scope (Namespaces in XML 1.0 §6.1): the default one and the prefixes bound.
type scope struct {
	def    string
	prefix map[string]string
}

func (sc scope) resolve(prefix string) (string, bool) {
	switch prefix {
	case "":
		return sc.def, true
	case "xml":
		return XMLNamespace, true
	}

	ns, ok := sc.prefix[prefix]

	return ns, ok
}

// element parses an element (§3.1) and its content, iteratively: its depth is bounded, not the stack's.
func (p *parser) element(sc scope) (*Element, error) {
	type open struct {
		e     *Element
		qname string
		outer scope
	}

	var (
		stack []open
		root  *Element
	)

	for {
		e, qname, inner, empty, err := p.startTag(sc)
		if err != nil {
			return nil, err
		}

		if root == nil {
			root = e
		} else {
			parent := stack[len(stack)-1].e
			parent.Children = append(parent.Children, e)
		}

		if !empty {
			if len(stack) == maxDepth {
				return nil, fmt.Errorf("elements nested deeper than %d", maxDepth)
			}

			stack = append(stack, open{e, qname, sc})
			sc = inner
		}

		// The content to the next start tag, closing the elements whose end tags come first.
		for len(stack) > 0 {
			if err := p.charData(stack[len(stack)-1].e); err != nil {
				return nil, err
			}

			if !p.eat("</") {
				break
			}

			top := stack[len(stack)-1]

			end, err := p.name()
			if err != nil {
				return nil, err
			}

			p.space()

			if !p.eat(">") {
				return nil, fmt.Errorf("</%s>: malformed end tag", end)
			}

			if end != top.qname {
				return nil, fmt.Errorf("</%s> ends <%s>", end, top.qname)
			}

			sc = top.outer
			stack = stack[:len(stack)-1]
		}

		if len(stack) == 0 {
			return root, nil
		}
	}
}

// startTag parses a STag or EmptyElemTag (§3.1), and the namespaces in scope in the element (Namespaces in XML 1.0
// §5): its declarations apply to its own name and attributes.
func (p *parser) startTag(sc scope) (*Element, string, scope, bool, error) {
	type rawAttr struct{ qname, value string }

	p.pos++ // <

	qname, err := p.name()
	if err != nil {
		return nil, "", sc, false, err
	}

	var (
		attrs []rawAttr
		empty bool
	)

	for {
		hadSpace := p.space()

		if p.eat("/>") {
			empty = true
			break
		}

		if p.eat(">") {
			break
		}

		if !hadSpace {
			return nil, "", sc, false, fmt.Errorf("<%s>: malformed start tag", qname)
		}

		an, err := p.name()
		if err != nil {
			return nil, "", sc, false, err
		}

		p.space()

		if !p.eat("=") {
			return nil, "", sc, false, fmt.Errorf("<%s>: attribute %s without value", qname, an)
		}

		p.space()

		v, err := p.attValue()
		if err != nil {
			return nil, "", sc, false, fmt.Errorf("<%s>: attribute %s: %w", qname, an, err)
		}

		for _, a := range attrs {
			if a.qname == an {
				return nil, "", sc, false, fmt.Errorf("<%s>: attribute %s twice", qname, an)
			}
		}

		attrs = append(attrs, rawAttr{an, v})
	}

	inner := sc
	cloned := false

	for _, a := range attrs {
		var prefix string

		switch {
		case a.qname == "xmlns":
		case strings.HasPrefix(a.qname, "xmlns:"):
			prefix = a.qname[len("xmlns:"):]
		default:
			continue
		}

		if err := checkBinding(prefix, a.value); err != nil {
			return nil, "", sc, false, fmt.Errorf("<%s>: %w", qname, err)
		}

		switch prefix {
		case "":
			inner.def = a.value
		case "xml":
		default:
			if !cloned {
				inner.prefix = clone(inner.prefix)
				cloned = true
			}

			inner.prefix[prefix] = a.value
		}
	}

	name, err := inner.qname(qname, true)
	if err != nil {
		return nil, "", sc, false, err
	}

	e := &Element{Name: name}

	for _, a := range attrs {
		if a.qname == "xmlns" || strings.HasPrefix(a.qname, "xmlns:") {
			continue
		}

		an, err := inner.qname(a.qname, false)
		if err != nil {
			return nil, "", sc, false, fmt.Errorf("<%s>: %w", qname, err)
		}

		for _, prev := range e.Attr {
			if prev.Name == an {
				return nil, "", sc, false, fmt.Errorf("<%s>: attribute %s of an expanded name given before", qname, a.qname)
			}
		}

		e.Attr = append(e.Attr, xml.Attr{Name: an, Value: a.value})
	}

	return e, qname, inner, empty, nil
}

// checkBinding checks a namespace declaration (Namespaces in XML 1.0 §3, NSC Reserved Prefixes and Namespace Names,
// NSC No Prefix Undeclaring).
func checkBinding(prefix, ns string) error {
	switch {
	case prefix == "xmlns":
		return errors.New("the xmlns prefix declared")
	case prefix == "xml" && ns != XMLNamespace:
		return fmt.Errorf("the xml prefix bound to %q", ns)
	case prefix != "xml" && ns == XMLNamespace:
		return fmt.Errorf("prefix %q bound to the xml namespace", prefix)
	case ns == xmlnsURL:
		return fmt.Errorf("prefix %q bound to the xmlns namespace", prefix)
	case prefix != "" && ns == "":
		return fmt.Errorf("prefix %q undeclared", prefix)
	case prefix != "" && !isNCName(prefix):
		return fmt.Errorf("prefix %q", prefix)
	}

	return nil
}

// qname resolves a QName (Namespaces in XML 1.0 §4, §6.2): an element without a prefix is in the default namespace,
// an attribute in none.
func (sc scope) qname(q string, element bool) (xml.Name, error) {
	prefix, local, hasPrefix := strings.Cut(q, ":")
	if !hasPrefix {
		prefix, local = "", q
	}

	if hasPrefix && prefix == "" || !isNCName(local) {
		return xml.Name{}, fmt.Errorf("name %q", q)
	}

	if !hasPrefix && !element {
		return xml.Name{Local: local}, nil
	}

	if element && prefix == "xmlns" {
		return xml.Name{}, fmt.Errorf("element %q of the xmlns prefix", q)
	}

	ns, ok := sc.resolve(prefix)
	if !ok {
		return xml.Name{}, fmt.Errorf("prefix of %q not declared", q)
	}

	return xml.Name{Space: ns, Local: local}, nil
}

// attValue is an AttValue (§2.3), its references replaced and its value normalized (§3.3.3): each white space
// character, not of a character reference, a space.
func (p *parser) attValue() (string, error) {
	if p.pos >= len(p.s) || p.s[p.pos] != '"' && p.s[p.pos] != '\'' {
		return "", errors.New("value not quoted")
	}

	q := p.s[p.pos]
	p.pos++

	var b strings.Builder

	for {
		if p.pos >= len(p.s) {
			return "", errors.New("value not closed")
		}

		c := p.s[p.pos]

		switch c {
		case q:
			p.pos++
			return b.String(), nil
		case '<':
			return "", errors.New("< in a value")
		case '&':
			r, err := p.reference()
			if err != nil {
				return "", err
			}

			b.WriteRune(r)
		case '\t', '\n':
			b.WriteByte(' ')

			p.pos++
		default:
			r, n := utf8.DecodeRuneInString(p.rest())
			if !isChar(r) {
				return "", fmt.Errorf("character %U", r)
			}

			b.WriteRune(r)

			p.pos += n
		}
	}
}

// reference is a CharRef or the reference of a predefined entity (§4.1, §4.6).
func (p *parser) reference() (rune, error) {
	end := strings.IndexByte(p.rest(), ';')
	if end < 0 {
		return 0, errors.New("reference not closed")
	}

	ref := p.s[p.pos+1 : p.pos+end]
	p.pos += end + 1

	switch ref {
	case "lt":
		return '<', nil
	case "gt":
		return '>', nil
	case "amp":
		return '&', nil
	case "apos":
		return '\'', nil
	case "quot":
		return '"', nil
	}

	digits, base := "", 10

	switch {
	case strings.HasPrefix(ref, "#x"):
		digits, base = ref[2:], 16
	case strings.HasPrefix(ref, "#"):
		digits = ref[1:]
	default:
		return 0, fmt.Errorf("entity %q not declared", ref)
	}

	if digits == "" || len(digits) > 8 {
		return 0, fmt.Errorf("character reference %q", ref)
	}

	var r rune

	for i := range len(digits) {
		d := digitValue(digits[i])
		if d < 0 || d >= base {
			return 0, fmt.Errorf("character reference %q", ref)
		}

		r = r*rune(base) + rune(d)
	}

	if !isChar(r) {
		return 0, fmt.Errorf("character reference %q", ref)
	}

	return r, nil
}

func digitValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}

	return -1
}

// charData parses the content of e (§3.1) to its next start tag or end tag: character data and references, CDATA
// sections; comments and processing instructions, left out, the text around them one Text.
func (p *parser) charData(e *Element) error {
	var b strings.Builder

	for {
		if p.pos >= len(p.s) {
			return errors.New("element not closed")
		}

		switch r := p.rest(); {
		case strings.HasPrefix(r, "<![CDATA["):
			p.pos += len("<![CDATA[")

			end := strings.Index(p.rest(), "]]>")
			if end < 0 {
				return errors.New("CDATA section not closed")
			}

			if err := checkChars(p.rest()[:end]); err != nil {
				return err
			}

			b.WriteString(p.rest()[:end])
			p.pos += end + len("]]>")
		case strings.HasPrefix(r, "<!--"):
			if err := p.comment(); err != nil {
				return err
			}
		case strings.HasPrefix(r, "<?"):
			if err := p.pi(); err != nil {
				return err
			}
		case strings.HasPrefix(r, "<!"):
			return errors.New("declaration in content")
		case r[0] == '<':
			if b.Len() > 0 {
				e.Children = append(e.Children, Text(b.String()))
			}

			return nil
		case r[0] == '&':
			c, err := p.reference()
			if err != nil {
				return err
			}

			b.WriteRune(c)
		case strings.HasPrefix(r, "]]>"):
			return errors.New("]]> in character data")
		default:
			c, n := utf8.DecodeRuneInString(r)
			if !isChar(c) {
				return fmt.Errorf("character %U", c)
			}

			b.WriteRune(c)

			p.pos += n
		}
	}
}

func checkChars(s string) error {
	for _, r := range s {
		if !isChar(r) {
			return fmt.Errorf("character %U", r)
		}
	}

	return nil
}
