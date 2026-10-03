package sdp

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

const ContentType = "application/sdp"

const maxSize = 1 << 16

var ErrMissing = errors.New("missing line")

type Line struct {
	Type  byte
	Value string

	lf   bool
	bare bool
}

func (l Line) String() string {
	return string(l.Type) + "=" + string(appendText(nil, l.Value))
}

func (l Line) appendTo(b []byte, last bool) []byte {
	b = append(b, l.Type, '=')
	b = appendText(b, l.Value)

	switch {
	case l.bare && last:
		return b
	case l.lf:
		return append(b, '\n')
	default:
		return append(b, "\r\n"...)
	}
}

func appendText(b []byte, s string) []byte {
	if !strings.ContainsAny(s, "\r\n\x00") {
		return append(b, s...)
	}

	for i := range len(s) {
		if c := s[i]; c == '\r' || c == '\n' || c == 0 {
			b = append(b, ' ')
		} else {
			b = append(b, c)
		}
	}

	return b
}

type Lines []Line

func (ls Lines) Value(t byte) (string, bool) {
	for _, l := range ls {
		if l.Type == t {
			return l.Value, true
		}
	}

	return "", false
}

func (ls Lines) Values(t byte) []string {
	var out []string

	for _, l := range ls {
		if l.Type == t {
			out = append(out, l.Value)
		}
	}

	return out
}

func (ls *Lines) Set(t byte, value string) {
	i := slices.IndexFunc(*ls, func(l Line) bool { return l.Type == t })
	if i < 0 {
		ls.Add(t, value)
		return
	}

	(*ls)[i].Value = value
	tail := (*ls)[i+1:]
	*ls = append((*ls)[:i+1], slices.DeleteFunc(tail, func(l Line) bool { return l.Type == t })...)
}

func (ls *Lines) Add(t byte, value string) {
	r := rank(t)
	i := 0

	for j, l := range *ls {
		if rank(l.Type) <= r {
			i = j + 1
		}
	}

	nl := Line{Type: t, Value: value}
	if i > 0 {
		nl.lf = (*ls)[i-1].lf
	} else if len(*ls) > 0 {
		nl.lf = (*ls)[0].lf
	}

	*ls = slices.Insert(*ls, i, nl)
}

func (ls *Lines) Del(t byte) int {
	return ls.DelFunc(func(l Line) bool { return l.Type == t })
}

func (ls *Lines) DelFunc(del func(Line) bool) int {
	n := len(*ls)
	*ls = slices.DeleteFunc(*ls, del)

	return n - len(*ls)
}

const order = "mvosiuepcbtrzka"

func rank(t byte) int {
	if i := strings.IndexByte(order, t); i >= 0 {
		return i
	}

	return len(order)
}

func splitAttr(v string) (name, value string) {
	name, value, _ = strings.Cut(v, ":")
	return trimWSP(name), trimWSP(value)
}

func trimWSP(s string) string {
	return strings.Trim(s, " \t")
}

func (ls Lines) Attr(name string) (string, bool) {
	for _, l := range ls {
		if l.Type != 'a' {
			continue
		}

		if n, v := splitAttr(l.Value); strings.EqualFold(n, name) {
			return v, true
		}
	}

	return "", false
}

func (ls Lines) Attrs(name string) []string {
	var out []string

	for _, l := range ls {
		if l.Type != 'a' {
			continue
		}

		if n, v := splitAttr(l.Value); strings.EqualFold(n, name) {
			out = append(out, v)
		}
	}

	return out
}

func (ls Lines) HasAttr(name string) bool {
	_, ok := ls.Attr(name)
	return ok
}

func attrValue(name, value string) string {
	if value == "" {
		return name
	}

	return name + ":" + value
}

func (ls *Lines) AddAttr(name, value string) {
	ls.Add('a', attrValue(name, value))
}

func (ls *Lines) SetAttr(name, value string) {
	i := slices.IndexFunc(*ls, func(l Line) bool { return isAttr(l, name) })
	if i < 0 {
		ls.AddAttr(name, value)
		return
	}

	(*ls)[i].Value = attrValue(name, value)
	tail := (*ls)[i+1:]
	*ls = append((*ls)[:i+1], slices.DeleteFunc(tail, func(l Line) bool { return isAttr(l, name) })...)
}

func (ls *Lines) DelAttr(name string) int {
	return ls.DelFunc(func(l Line) bool { return isAttr(l, name) })
}

func isAttr(l Line, name string) bool {
	if l.Type != 'a' {
		return false
	}

	n, _ := splitAttr(l.Value)

	return strings.EqualFold(n, name)
}

func (ls Lines) appendTo(b []byte, last bool) []byte {
	for i, l := range ls {
		b = l.appendTo(b, last && i == len(ls)-1)
	}

	return b
}

type Session struct {
	Lines

	Media []*Media

	trailer string
}

func Parse(b []byte) (*Session, error) {
	if len(b) == 0 {
		return nil, errors.New("sdp: empty body")
	}

	if len(b) > maxSize {
		return nil, fmt.Errorf("sdp: body of %d bytes exceeds %d", len(b), maxSize)
	}

	s := &Session{}
	cur := &s.Lines
	rest := string(b)

	for n := 1; rest != ""; n++ {
		if n > 1 && strings.Trim(rest, "\r\n") == "" {
			s.trailer = rest
			break
		}

		var (
			text string
			l    Line
		)

		if i := strings.IndexByte(rest, '\n'); i < 0 {
			text, rest = rest, ""
			l.bare = true
		} else {
			text, rest = rest[:i], rest[i+1:]
			if t, ok := strings.CutSuffix(text, "\r"); ok {
				text = t
			} else {
				l.lf = true
			}
		}

		if len(text) < 2 || !isType(text[0]) || text[1] != '=' {
			return nil, fmt.Errorf("sdp line %d: %q is not <type>=<value>", n, text)
		}

		if strings.ContainsAny(text[2:], "\r\x00") {
			return nil, fmt.Errorf("sdp line %d: control character in value", n)
		}

		l.Type, l.Value = text[0], text[2:]

		switch {
		case n == 1 && l.Type != 'v':
			return nil, fmt.Errorf("sdp line 1: got %c=, want v=", l.Type)
		case l.Type == 'm':
			if _, err := ParseMediaDesc(l.Value); err != nil {
				return nil, fmt.Errorf("sdp line %d: %w", n, err)
			}

			s.Media = append(s.Media, &Media{Lines: Lines{l}})
			cur = &s.Media[len(s.Media)-1].Lines
		default:
			*cur = append(*cur, l)
		}
	}

	return s, nil
}

func isType(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func (s *Session) AppendTo(b []byte) []byte {
	last := s.trailer == ""
	b = s.appendTo(b, last && len(s.Media) == 0)

	for i, m := range s.Media {
		b = m.appendTo(b, last && i == len(s.Media)-1)
	}

	return append(b, s.trailer...)
}

func (s *Session) Bytes() []byte { return s.AppendTo(nil) }

func (s *Session) String() string { return string(s.Bytes()) }

func (s *Session) Clone() *Session {
	c := &Session{Lines: slices.Clone(s.Lines), trailer: s.trailer}

	for _, m := range s.Media {
		c.Media = append(c.Media, m.Clone())
	}

	return c
}

func (s *Session) Origin() (Origin, error) {
	v, ok := s.Value('o')
	if !ok {
		return Origin{}, fmt.Errorf("%w: o=", ErrMissing)
	}

	return ParseOrigin(v)
}

func (s *Session) SetOrigin(o Origin) {
	s.Set('o', o.String())
}

func (s *Session) MediaConnection(i int) (Connection, error) {
	c, err := s.Media[i].Connection()
	if errors.Is(err, ErrMissing) {
		return s.Connection()
	}

	return c, err
}

func (s *Session) MediaDirection(i int) Direction {
	if d, ok := s.Media[i].Direction(); ok {
		return d
	}

	if d, ok := s.Direction(); ok {
		return d
	}

	return SendRecv
}

func (s *Session) AddrTypes() ([]string, error) {
	var out []string

	for i, m := range s.Media {
		if m.Port() == 0 {
			continue
		}

		c, err := s.MediaConnection(i)
		if err != nil {
			return nil, fmt.Errorf("media %d: %w", i+1, err)
		}

		a, ok := c.Addr()
		if !ok {
			return nil, fmt.Errorf("media %d: c=%s is not an %s address literal", i+1, c, c.AddrType)
		}

		family := IP6
		if a.Is4() {
			family = IP4
		}

		if !slices.Contains(out, family) {
			out = append(out, family)
		}
	}

	return out, nil
}
