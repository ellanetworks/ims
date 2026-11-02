package pocsettings

import (
	"encoding/xml"
	"errors"
	"fmt"
	"slices"

	"github.com/ellanetworks/ims/sip/xmltree"
	"github.com/ellanetworks/ims/sip/xsd"
)

const (
	ContentType = "application/poc-settings+xml"
	Namespace   = "urn:oma:params:xml:ns:poc:poc-settings"
	// RFC 4354 section 5.1
	Event = "poc-settings"
)

const maxSize = 1 << 16

// RFC 4354 section 6
type Settings struct {
	Entities []Entity
	Attr     []xml.Attr
	Other    []*xmltree.Element
}

// RFC 4354 section 6
type Entity struct {
	ID                           string
	IncomingSessionBarring       *Toggle
	AnswerMode                   *AnswerModeSettings
	IncomingPersonalAlertBarring *Toggle
	SimultaneousSessionsSupport  *Toggle
	Attr                         []xml.Attr
	Other                        []*xmltree.Element
}

type Toggle struct {
	Active bool
	Attr   []xml.Attr
	Other  []*xmltree.Element
}

type AnswerModeSettings struct {
	AnswerMode AnswerMode
	Attr       []xml.Attr
	Other      []*xmltree.Element
}

type AnswerMode string

const (
	Automatic AnswerMode = "automatic"
	Manual    AnswerMode = "manual"
)

var toggles = map[string]string{
	"isb-settings":  "incoming-session-barring",
	"ipab-settings": "incoming-personal-alert-barring",
	"sss-settings":  "simultaneous-sessions-support",
}

var ErrValue = errors.New("malformed element")

func Decode(b []byte) (Settings, error) {
	if len(b) > maxSize {
		return Settings{}, fmt.Errorf("pocsettings: document of %d bytes exceeds %d", len(b), maxSize)
	}

	root, err := xmltree.Parse(b)
	if err != nil {
		return Settings{}, fmt.Errorf("pocsettings: %w", err)
	}

	if root.Name != name("poc-settings") {
		return Settings{}, fmt.Errorf("pocsettings: root element <%s> of %q", root.Name.Local, root.Name.Space)
	}

	var d decoder

	s := Settings{Attr: root.Attr}

	for _, e := range root.Elements() {
		if known(e) == "entity" {
			en, ok := d.entity(e)
			if ok && !slices.ContainsFunc(s.Entities, func(o Entity) bool { return o.ID == en.ID }) {
				s.Entities = append(s.Entities, en)
				continue
			}

			if ok {
				d.fail("entity", "id %q twice", en.ID)
			}
		}

		s.Other = append(s.Other, e)
	}

	return s, errors.Join(d.errs...)
}

type decoder struct {
	errs []error
}

func (d *decoder) fail(element, format string, a ...any) {
	d.errs = append(d.errs, fmt.Errorf("pocsettings: <%s>: %w: %s", element, ErrValue, fmt.Sprintf(format, a...)))
}

func known(e *xmltree.Element) string {
	if e.Name.Space != Namespace {
		return ""
	}

	return e.Name.Local
}

func (d *decoder) entity(e *xmltree.Element) (Entity, bool) {
	id, ok := e.AttrValue("", "id")
	if !ok {
		d.fail("entity", "without id")
		return Entity{}, false
	}

	en := Entity{ID: id}

	for _, a := range e.Attr {
		if a.Name != (xml.Name{Local: "id"}) {
			en.Attr = append(en.Attr, a)
		}
	}

	seen := map[string]bool{}

	for _, c := range e.Elements() {
		var (
			t  **Toggle
			ok bool
		)

		n := known(c)

		switch n {
		case "isb-settings":
			t = &en.IncomingSessionBarring
		case "ipab-settings":
			t = &en.IncomingPersonalAlertBarring
		case "sss-settings":
			t = &en.SimultaneousSessionsSupport
		case "am-settings":
		default:
			en.Other = append(en.Other, c)
			continue
		}

		switch {
		case seen[n]:
			d.fail(n, "given more than once")
		case t != nil:
			*t, ok = d.toggle(c)
		default:
			en.AnswerMode, ok = d.answerMode(c)
		}

		seen[n] = true

		if ok {
			continue
		}

		en.Other = append(en.Other, c)
	}

	return en, true
}

// RFC 4354 section 6.1
func (d *decoder) toggle(e *xmltree.Element) (*Toggle, bool) {
	cs := e.Elements()
	if len(cs) == 0 || known(cs[0]) != toggles[e.Name.Local] {
		d.fail(e.Name.Local, "without <%s>", toggles[e.Name.Local])
		return nil, false
	}

	v, _ := cs[0].AttrValue("", "active")

	t := &Toggle{Attr: e.Attr, Other: cs[1:]}

	var ok bool
	if t.Active, ok = xsd.ParseBoolean(v); !ok {
		d.fail(cs[0].Name.Local, "active %q", v)
		return nil, false
	}

	if cs[0].HasElements() {
		d.fail(cs[0].Name.Local, "child elements")
		return nil, false
	}

	return t, true
}

func (d *decoder) answerMode(e *xmltree.Element) (*AnswerModeSettings, bool) {
	cs := e.Elements()
	if len(cs) == 0 || known(cs[0]) != "answer-mode" {
		d.fail("am-settings", "without <answer-mode>")
		return nil, false
	}

	m := AnswerMode(xsd.Collapse(cs[0].Text()))
	if m != Automatic && m != Manual || cs[0].HasElements() {
		d.fail("answer-mode", "%q", cs[0].Text())
		return nil, false
	}

	return &AnswerModeSettings{AnswerMode: m, Attr: e.Attr, Other: cs[1:]}, true
}

// RFC 4354 section 6.1
func Encode(s Settings) ([]byte, error) {
	root := &xmltree.Element{Name: name("poc-settings"), Attr: s.Attr}

	var ids []string

	for _, en := range s.Entities {
		if slices.Contains(ids, en.ID) {
			return nil, fmt.Errorf("pocsettings: entity %q twice", en.ID)
		}

		ids = append(ids, en.ID)

		e, err := en.element()
		if err != nil {
			return nil, err
		}

		root.Children = append(root.Children, e)
	}

	root.Children = appendElements(root.Children, s.Other)

	b, err := xmltree.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("pocsettings: %w", err)
	}

	return b, nil
}

func (en *Entity) element() (*xmltree.Element, error) {
	e := &xmltree.Element{Name: name("entity"), Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: en.ID}}}
	e.Attr = append(e.Attr, en.Attr...)

	e.Children = appendToggle(e.Children, "isb-settings", en.IncomingSessionBarring)

	if a := en.AnswerMode; a != nil {
		if a.AnswerMode != Automatic && a.AnswerMode != Manual {
			return nil, fmt.Errorf("pocsettings: entity %q: answer mode %q", en.ID, a.AnswerMode)
		}

		x := &xmltree.Element{Name: name("am-settings"), Attr: a.Attr}
		x.Children = append(x.Children, xmltree.New(Namespace, "answer-mode", xmltree.Text(a.AnswerMode)))
		x.Children = appendElements(x.Children, a.Other)
		e.Children = append(e.Children, x)
	}

	e.Children = appendToggle(e.Children, "ipab-settings", en.IncomingPersonalAlertBarring)
	e.Children = appendToggle(e.Children, "sss-settings", en.SimultaneousSessionsSupport)
	e.Children = appendElements(e.Children, en.Other)

	return e, nil
}

func appendToggle(ns []xmltree.Node, local string, t *Toggle) []xmltree.Node {
	if t == nil {
		return ns
	}

	v := &xmltree.Element{Name: name(toggles[local]), Attr: []xml.Attr{{Name: xml.Name{Local: "active"}, Value: xsd.FormatBoolean(t.Active)}}}
	e := &xmltree.Element{Name: name(local), Attr: t.Attr, Children: []xmltree.Node{v}}
	e.Children = appendElements(e.Children, t.Other)

	return append(ns, e)
}

func appendElements(ns []xmltree.Node, es []*xmltree.Element) []xmltree.Node {
	for _, e := range es {
		ns = append(ns, e)
	}

	return ns
}

func name(local string) xml.Name { return xml.Name{Space: Namespace, Local: local} }
