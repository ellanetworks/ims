package sip

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

type Param struct {
	Name  string
	Value string
}

type Params []Param

func (ps Params) Get(name string) (string, bool) {
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) {
			return p.Value, true
		}
	}

	return "", false
}

func (ps Params) Has(name string) bool {
	_, ok := ps.Get(name)
	return ok
}

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

func (ps Params) Clone() Params {
	if ps == nil {
		return nil
	}

	return append(Params(nil), ps...)
}

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

func ParseParams(s string) (Params, error) {
	t := trimLeftWSP(s)
	if t != "" && t[0] != ';' {
		t = ";" + t
	}

	ps, err := parseHeaderParams(t)
	if err != nil {
		return nil, fmt.Errorf("parameters %q: %w", s, err)
	}

	return ps, nil
}

func ParseTokenParams(s string) (string, Params, error) {
	t := trimWSP(s)

	n := tokenLen(t)
	if n == 0 {
		return "", nil, fmt.Errorf("%q: missing token", s)
	}

	ps, err := parseHeaderParams(t[n:])
	if err != nil {
		return "", nil, fmt.Errorf("%q: %w", s, err)
	}

	return t[:n], ps, nil
}

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

func headerParamValue(s string) (value, rest string, err error) {
	if s != "" && s[0] == '"' {
		n, err := quotedLen(s)
		if err != nil {
			return "", "", err
		}

		return s[:n], s[n:], nil
	}

	n := 0
	for n < len(s) && isHeaderParamValueByte(s[n]) {
		n++
	}

	return s[:n], s[n:], nil
}

func isHeaderParamValueByte(c byte) bool {
	return c > ' ' && c != 0x7f && strings.IndexByte(`;,"<>@`, c) < 0
}

func parseURIParams(s string) (Params, error) {
	var ps Params

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
