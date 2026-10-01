package sip

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Param is a URI or header parameter. Value is empty for a parameter that
// has none. A quoted-string value keeps its quotes (see Unquote).
type Param struct {
	Name  string
	Value string
}

// Params is a parameter list in wire order, duplicates included. Names are
// compared ignoring case.
type Params []Param

// Get returns the value of the first parameter with this name, and whether
// it is present.
func (ps Params) Get(name string) (string, bool) {
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) {
			return p.Value, true
		}
	}

	return "", false
}

// Has reports whether a parameter with this name is present.
func (ps Params) Has(name string) bool {
	_, ok := ps.Get(name)
	return ok
}

// Set sets the value of the first parameter with this name, or appends it.
//
// Set and Del never write to the list's existing storage: a URI, Via or
// Address copied by value keeps its own parameters when the copy changes.
func (ps *Params) Set(name, value string) {
	for i, p := range *ps {
		if strings.EqualFold(p.Name, name) {
			c := slices.Clone(*ps)
			c[i].Value = value
			*ps = c

			return
		}
	}

	*ps = append(slices.Clip(*ps), Param{Name: name, Value: value})
}

// Del removes every parameter with this name.
func (ps *Params) Del(name string) {
	if !ps.Has(name) {
		return
	}

	var out Params

	for _, p := range *ps {
		if !strings.EqualFold(p.Name, name) {
			out = append(out, p)
		}
	}

	*ps = out
}

// Clone returns a copy of the list that shares no storage with ps.
func (ps Params) Clone() Params {
	if ps == nil {
		return nil
	}

	return append(Params(nil), ps...)
}

// String returns the list in wire form, each parameter preceded by ';'.
func (ps Params) String() string {
	return string(ps.appendTo(nil))
}

func (ps Params) appendTo(b []byte) []byte {
	for _, p := range ps {
		b = append(b, ';')
		b = append(b, p.Name...)

		if p.Value != "" {
			b = append(b, '=')
			b = append(b, p.Value...)
		}
	}

	return b
}

// parseHeaderParams parses generic-params (RFC 3261 §25.1): ";" name
// ["=" value], with optional whitespace around the separators. s is empty
// or starts with ';' after optional whitespace.
func parseHeaderParams(s string) (Params, error) {
	var ps Params

	for {
		s = trimLeftWSP(s)
		if s == "" {
			return ps, nil
		}

		if s[0] != ';' {
			return nil, fmt.Errorf("unexpected %q before parameters", s)
		}

		s = trimLeftWSP(s[1:])
		if s == "" {
			// A trailing ';', as some implementations send.
			return ps, nil
		}

		n := tokenLen(s)
		if n == 0 {
			return nil, errors.New("empty parameter name")
		}

		p := Param{Name: s[:n]}
		s = trimLeftWSP(s[n:])

		if s != "" && s[0] == '=' {
			v, rest, err := headerParamValue(trimLeftWSP(s[1:]))
			if err != nil {
				return nil, fmt.Errorf("parameter %s: %w", p.Name, err)
			}

			p.Value = v
			s = rest
		}

		ps = append(ps, p)
	}
}

// headerParamValue reads a gen-value: a token, a host or a quoted-string.
// Bytes outside those grammars are accepted, apart from separators, so
// that values from real implementations survive.
func headerParamValue(s string) (value, rest string, err error) {
	if s != "" && s[0] == '"' {
		n, err := quotedLen(s)
		if err != nil {
			return "", "", err
		}

		return s[:n], s[n:], nil
	}

	// An empty value, as in "expires=", is read as no value.
	n := 0
	for n < len(s) && isHeaderParamValueByte(s[n]) {
		n++
	}

	return s[:n], s[n:], nil
}

func isHeaderParamValueByte(c byte) bool {
	return c > ' ' && c != 0x7f && strings.IndexByte(`;,"<>@`, c) < 0
}

// parseURIParams parses uri-parameters (RFC 3261 §19.1.1). s is the text
// after the first ';', up to the headers.
func parseURIParams(s string) (Params, error) {
	var ps Params

	// A trailing ';', as in <sip:a@b;>, is tolerated.
	s = strings.TrimSuffix(s, ";")
	if s == "" {
		return nil, nil
	}

	for part := range strings.SplitSeq(s, ";") {
		name, value, _ := strings.Cut(part, "=")
		if name == "" {
			return nil, errors.New("empty URI parameter name")
		}

		ps = append(ps, Param{Name: name, Value: value})
	}

	return ps, nil
}
