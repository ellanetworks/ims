package sip

import (
	"errors"
	"fmt"
)

type Auth struct {
	Scheme string
	Params Params
}

func (a Auth) String() string {
	b := append([]byte(nil), a.Scheme...)

	for i, p := range a.Params {
		if i == 0 {
			b = append(b, ' ')
		} else {
			b = append(b, ", "...)
		}

		b = append(b, p.Name...)
		b = append(b, '=')

		if p.Value == "" {
			b = append(b, `""`...)
		} else {
			b = append(b, p.Value...)
		}
	}

	return string(b)
}

func ParseAuth(s string) (Auth, error) {
	a, err := parseAuth(trimWSP(s))
	if err != nil {
		return Auth{}, fmt.Errorf("auth %q: %w", s, err)
	}

	return a, nil
}

func parseAuth(s string) (Auth, error) {
	n := tokenLen(s)
	if n == 0 {
		return Auth{}, errors.New("missing auth-scheme")
	}

	a := Auth{Scheme: s[:n]}

	s = s[n:]
	if s != "" && !isWSP(s[0]) {
		return Auth{}, fmt.Errorf("unexpected %q after auth-scheme", s)
	}

	for {
		s = trimLeftWSP(s)
		if s == "" {
			return a, nil
		}

		if s[0] == ',' {
			s = s[1:]
			continue
		}

		n := tokenLen(s)
		if n == 0 {
			return Auth{}, fmt.Errorf("unexpected %q before parameter", s)
		}

		p := Param{Name: s[:n]}

		s = trimLeftWSP(s[n:])
		if s == "" || s[0] != '=' {
			return Auth{}, fmt.Errorf("parameter %s without a value", p.Name)
		}

		v, rest, err := headerParamValue(trimLeftWSP(s[1:]))
		if err != nil {
			return Auth{}, fmt.Errorf("parameter %s: %w", p.Name, err)
		}

		if v == "" {
			return Auth{}, fmt.Errorf("parameter %s without a value", p.Name)
		}

		p.Value = v
		a.Params = append(a.Params, p)

		s = trimLeftWSP(rest)
		if s != "" && s[0] != ',' {
			return Auth{}, fmt.Errorf("unexpected %q after parameter %s", s, p.Name)
		}
	}
}
