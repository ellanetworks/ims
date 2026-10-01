package sip

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Address is a name-addr or addr-spec with header parameters, as found in
// From, To, Contact, Route, Record-Route and the many headers that share
// their form (RFC 3261 §20.10).
type Address struct {
	// Display is the display name as written: a quoted-string, quotes
	// included, or a sequence of tokens. See DisplayName.
	Display string
	URI     URI
	// Params are the header parameters after the URI, such as tag,
	// expires or +sip.instance.
	Params Params
	// Star is set for the Contact value "*". Other fields are then empty.
	Star bool
}

// DisplayName returns the display name without quotes.
func (a Address) DisplayName() string {
	return Unquote(a.Display)
}

// Tag returns the tag parameter.
func (a Address) Tag() string {
	t, _ := a.Params.Get("tag")
	return t
}

// Clone returns a copy that shares no storage with a.
func (a Address) Clone() Address {
	a.URI = a.URI.Clone()
	a.Params = a.Params.Clone()

	return a
}

// String returns the address in name-addr form, so that URI parameters
// cannot be mistaken for header parameters.
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

// ParseAddress parses one name-addr or addr-spec with its parameters.
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

	// An unquoted display name holds no ';': a '<' after one belongs to a
	// header parameter of an addr-spec, as in +sip.instance="<urn:...>".
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
		// In an addr-spec, parameters after the URI belong to the header
		// (RFC 3261 §20.10).
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

// checkTokens checks an unquoted display name: tokens separated by
// whitespace.
func checkTokens(s string) error {
	for _, t := range strings.Fields(s) {
		if !isToken(t) {
			return fmt.Errorf("invalid display name %q", s)
		}
	}

	return nil
}

// ParseAddressList parses a comma-separated list of addresses. Empty
// elements are skipped. "*" is only valid alone (RFC 3261 §20.10).
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
