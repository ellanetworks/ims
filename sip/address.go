package sip

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

type Address struct {
	Display string
	URI     URI

	Params Params

	Star bool
}

func (a Address) DisplayName() string {
	return Unquote(a.Display)
}

func (a Address) Tag() string {
	t, _ := a.Params.Get("tag")
	return t
}

func (a Address) Clone() Address {
	a.URI = a.URI.Clone()
	a.Params = a.Params.Clone()

	return a
}

func (a Address) String() string {
	if a.Star {
		return "*"
	}

	var b []byte

	if a.Display != "" {
		b = append(b, a.Display...)
		b = append(b, ' ')
	}

	b = append(b, '<')
	b = a.URI.appendTo(b)
	b = append(b, '>')
	b = a.Params.appendTo(b)

	return string(b)
}

func ParseAddress(s string) (Address, error) {
	a, err := parseAddress(trimWSP(s))
	if err != nil {
		return Address{}, fmt.Errorf("address %q: %w", s, err)
	}

	return a, nil
}

func parseAddress(s string) (Address, error) {
	if s == "*" {
		return Address{Star: true}, nil
	}

	var (
		a       Address
		uri     string
		rest    string
		angle   = strings.IndexByte(s, '<')
		display string
	)

	if semi := strings.IndexByte(s, ';'); semi >= 0 && angle > semi {
		angle = -1
	}

	switch {
	case s != "" && s[0] == '"':
		n, err := quotedLen(s)
		if err != nil {
			return Address{}, err
		}

		a.Display = s[:n]

		s = trimLeftWSP(s[n:])
		if s == "" || s[0] != '<' {
			return Address{}, errors.New("display name not followed by <")
		}

		angle = 0

		fallthrough
	case angle >= 0:
		display, s = trimWSP(s[:angle]), s[angle+1:]
		if display != "" {
			if err := checkTokens(display); err != nil {
				return Address{}, err
			}

			a.Display = display
		}

		end := strings.IndexByte(s, '>')
		if end < 0 {
			return Address{}, errors.New("missing >")
		}

		uri, rest = s[:end], s[end+1:]
	default:
		uri, rest, _ = strings.Cut(s, ";")
		if rest != "" || strings.HasSuffix(s, ";") {
			rest = ";" + rest
		}

		uri = trimWSP(uri)
	}

	u, err := parseURI(uri)
	if err != nil {
		return Address{}, err
	}

	a.URI = u

	if a.Params, err = parseHeaderParams(rest); err != nil {
		return Address{}, err
	}

	return a, nil
}

func checkTokens(s string) error {
	for _, t := range strings.Fields(s) {
		if !isToken(t) {
			return fmt.Errorf("invalid display name %q", s)
		}
	}

	return nil
}

func ParseAddressList(v string) ([]Address, error) {
	var out []Address

	for _, e := range SplitList(v) {
		if e == "" {
			continue
		}

		a, err := ParseAddress(e)
		if err != nil {
			return nil, err
		}

		out = append(out, a)
	}

	if len(out) > 1 && slices.ContainsFunc(out, func(a Address) bool { return a.Star }) {
		return nil, fmt.Errorf("address list %q: * with other addresses", v)
	}

	return out, nil
}
