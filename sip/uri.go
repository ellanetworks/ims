package sip

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// URI is a sip:, sips: or tel: URI (RFC 3261 §19.1, RFC 3966), or any other
// absolute URI kept opaque. Components are kept as written, escapes
// included, so that String returns the text the URI was parsed from.
type URI struct {
	// Scheme is kept as written. Compare it with IsSIP, IsSIPS and IsTel.
	Scheme string
	// User is the user part of a sip: URI, or the number of a tel: URI.
	User     string
	Password string
	// Host is a host name, an IPv4 address or an IPv6 reference in
	// brackets (RFC 5954). It is empty for tel: URIs.
	Host string
	// Port is 0 when the URI has none. A port of 0 as written is kept only
	// while the Request-Line or header field holding the URI is unchanged.
	Port   uint16
	Params Params
	// Headers is the text after '?', as written.
	Headers string
	// Opaque is everything after "scheme:" for other schemes.
	Opaque string
}

// IsSIP reports whether the scheme is sip or sips.
func (u URI) IsSIP() bool {
	return strings.EqualFold(u.Scheme, "sip") || u.IsSIPS()
}

// IsSIPS reports whether the scheme is sips.
func (u URI) IsSIPS() bool {
	return strings.EqualFold(u.Scheme, "sips")
}

// IsTel reports whether the scheme is tel.
func (u URI) IsTel() bool {
	return strings.EqualFold(u.Scheme, "tel")
}

// HostPort returns the host and, when present, the port.
func (u URI) HostPort() string {
	if u.Port == 0 {
		return u.Host
	}

	return u.Host + ":" + strconv.Itoa(int(u.Port))
}

// Addr returns the host as an IP address, when it is one.
func (u URI) Addr() (netip.Addr, bool) {
	return hostAddr(u.Host)
}

// equal reports whether u and v have the same components, as written.
// It is not URI comparison (RFC 3261 §19.1.4).
func (u URI) equal(v URI) bool {
	return u.Scheme == v.Scheme && u.User == v.User && u.Password == v.Password &&
		u.Host == v.Host && u.Port == v.Port && u.Headers == v.Headers && u.Opaque == v.Opaque &&
		slices.Equal(u.Params, v.Params)
}

// Clone returns a copy that shares no storage with u.
func (u URI) Clone() URI {
	u.Params = u.Params.Clone()
	return u
}

// String returns the URI in wire form.
func (u URI) String() string {
	return string(u.appendTo(nil))
}

func (u URI) appendTo(b []byte) []byte {
	b = append(b, u.Scheme...)
	b = append(b, ':')

	switch {
	case u.IsSIP():
		if u.User != "" {
			b = append(b, u.User...)
			if u.Password != "" {
				b = append(b, ':')
				b = append(b, u.Password...)
			}

			b = append(b, '@')
		}

		b = append(b, u.Host...)
		if u.Port != 0 {
			b = append(b, ':')
			b = strconv.AppendInt(b, int64(u.Port), 10)
		}
	case u.IsTel():
		b = append(b, u.User...)
	default:
		return append(b, u.Opaque...)
	}

	b = u.Params.appendTo(b)
	if u.Headers != "" {
		b = append(b, '?')
		b = append(b, u.Headers...)
	}

	return b
}

// ParseURI parses an absolute URI.
func ParseURI(s string) (URI, error) {
	u, err := parseURI(s)
	if err != nil {
		return URI{}, fmt.Errorf("URI %q: %w", s, err)
	}

	return u, nil
}

func parseURI(s string) (URI, error) {
	for i := range len(s) {
		if c := s[i]; c <= ' ' || c == 0x7f || c == '<' || c == '>' || c == '"' {
			return URI{}, fmt.Errorf("invalid character %q", c)
		}
	}

	scheme, rest, ok := strings.Cut(s, ":")
	if !ok || !isScheme(scheme) {
		return URI{}, errors.New("invalid scheme")
	}

	u := URI{Scheme: scheme}

	switch {
	case u.IsSIP():
		return u, u.parseSIP(rest)
	case u.IsTel():
		return u, u.parseTel(rest)
	}

	if rest == "" {
		return URI{}, errors.New("empty URI")
	}

	u.Opaque = rest

	return u, nil
}

// isScheme checks scheme = ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ).
func isScheme(s string) bool {
	if s == "" || !isAlpha(s[0]) {
		return false
	}

	for i := 1; i < len(s); i++ {
		if c := s[i]; !isAlpha(c) && (c < '0' || c > '9') && c != '+' && c != '-' && c != '.' {
			return false
		}
	}

	return true
}

func isAlpha(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func (u *URI) parseSIP(s string) error {
	// userinfo cannot hold an unescaped '@', and neither can the
	// parameters nor the headers that follow the host.
	if i := strings.IndexByte(s, '@'); i >= 0 {
		u.User, u.Password, _ = strings.Cut(s[:i], ":")
		if u.User == "" {
			return errors.New("empty user part")
		}

		s = s[i+1:]
	}

	s, headers, hasHeaders := strings.Cut(s, "?")
	if hasHeaders {
		if headers == "" {
			return errors.New("empty headers")
		}

		u.Headers = headers
	}

	hostport, params, hasParams := strings.Cut(s, ";")

	host, rest, err := parseHost(hostport)
	if err != nil {
		return err
	}

	u.Host = host

	if rest != "" {
		if rest[0] != ':' {
			return fmt.Errorf("unexpected %q after host", rest)
		}

		if u.Port, err = parsePort(rest[1:]); err != nil {
			return err
		}
	}

	if hasParams {
		if u.Params, err = parseURIParams(params); err != nil {
			return err
		}
	}

	return nil
}

// parseTel parses the part of a tel: URI after the scheme (RFC 3966). The
// number is kept as written; its characters are not checked further.
func (u *URI) parseTel(s string) error {
	number, params, hasParams := strings.Cut(s, ";")
	if number == "" {
		return errors.New("empty telephone number")
	}

	if strings.ContainsAny(number, "?@") {
		return errors.New("invalid telephone number")
	}

	u.User = number

	if hasParams {
		var err error
		if u.Params, err = parseURIParams(params); err != nil {
			return err
		}
	}

	return nil
}

// parseHost reads a host at the start of s: a hostname, an IPv4 address
// or an IPv6 reference (RFC 3261 §25.1, RFC 5954). It returns the host as
// written and the rest of s.
func parseHost(s string) (host, rest string, err error) {
	if s != "" && s[0] == '[' {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return "", "", errors.New("unterminated IPv6 reference")
		}

		a, err := netip.ParseAddr(s[1:end])
		if err != nil || !a.Is6() || a.Zone() != "" {
			return "", "", fmt.Errorf("invalid IPv6 reference %q", s[:end+1])
		}

		return s[:end+1], s[end+1:], nil
	}

	n := 0
	for n < len(s) && isHostByte(s[n]) {
		n++
	}

	if n == 0 {
		return "", "", errors.New("empty host")
	}

	return s[:n], s[n:], nil
}

// isHostByte accepts hostname and IPv4 characters, and '_', which real
// deployments use in container host names.
func isHostByte(c byte) bool {
	return isAlpha(c) || ('0' <= c && c <= '9') || c == '-' || c == '.' || c == '_'
}

func parsePort(s string) (uint16, error) {
	n, err := parseUint(s, 65535)
	if err != nil {
		return 0, fmt.Errorf("invalid port %q", s)
	}

	return uint16(n), nil
}

// hostAddr returns the address of a host that is an IP literal.
func hostAddr(host string) (netip.Addr, bool) {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")

	a, err := netip.ParseAddr(host)
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}

	return a, true
}

// FormatHost returns the host form of an address: IPv6 addresses in brackets.
func FormatHost(a netip.Addr) string {
	if a.Is6() && !a.Is4In6() {
		return "[" + a.String() + "]"
	}

	return a.Unmap().String()
}
