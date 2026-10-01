package sip

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

type URI struct {
	Scheme string

	User     string
	Password string

	Host string

	Port   uint16
	Params Params

	Headers string

	Opaque string
}

func (u URI) IsSIP() bool {
	return strings.EqualFold(u.Scheme, "sip") || u.IsSIPS()
}

func (u URI) IsSIPS() bool {
	return strings.EqualFold(u.Scheme, "sips")
}

func (u URI) IsTel() bool {
	return strings.EqualFold(u.Scheme, "tel")
}

func (u URI) HostPort() string {
	if u.Port == 0 {
		return u.Host
	}

	return u.Host + ":" + strconv.Itoa(int(u.Port))
}

func (u URI) Addr() (netip.Addr, bool) {
	return HostAddr(u.Host)
}

func (u URI) equal(v URI) bool {
	return u.Scheme == v.Scheme && u.User == v.User && u.Password == v.Password &&
		u.Host == v.Host && u.Port == v.Port && u.Headers == v.Headers && u.Opaque == v.Opaque &&
		slices.Equal(u.Params, v.Params)
}

func (u URI) Equivalent(v URI) bool {
	if !strings.EqualFold(u.Scheme, v.Scheme) {
		return false
	}

	if !u.IsSIP() {
		v.Scheme = u.Scheme
		return u.equal(v)
	}

	if u.User != v.User || u.Password != v.Password || !strings.EqualFold(u.Host, v.Host) ||
		u.Port != v.Port || !strings.EqualFold(u.Headers, v.Headers) {
		return false
	}

	return paramsEquivalent(u.Params, v.Params) && paramsEquivalent(v.Params, u.Params)
}

func paramsEquivalent(a, b Params) bool {
	for _, p := range a {
		value, ok := b.Get(p.Name)

		switch {
		case ok && !strings.EqualFold(p.Value, value):
			return false
		case !ok && slices.Contains([]string{"user", "ttl", "method", "maddr"}, strings.ToLower(p.Name)):
			return false
		}
	}

	return true
}

func (u URI) Clone() URI {
	u.Params = u.Params.Clone()
	return u
}

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
	if i := strings.IndexByte(s, '@'); i >= 0 {
		u.User, u.Password, _ = strings.Cut(s[:i], ":")
		if u.User == "" {
			return errors.New("empty user part")
		}

		if err := checkEscaped(u.User, userMarks); err != nil {
			return fmt.Errorf("user part: %w", err)
		}

		if err := checkEscaped(u.Password, passwordMarks); err != nil {
			return fmt.Errorf("password: %w", err)
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

func (u *URI) parseTel(s string) error {
	number, params, hasParams := strings.Cut(s, ";")
	if number == "" {
		return errors.New("empty telephone number")
	}

	if !isTelNumber(number) {
		return fmt.Errorf("invalid telephone number %q", number)
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

	labels := strings.Split(strings.TrimSuffix(s[:n], "."), ".")
	for _, l := range labels {
		if l == "" || l[0] == '-' || l[len(l)-1] == '-' {
			return "", "", fmt.Errorf("invalid host %q", s[:n])
		}
	}

	return s[:n], s[n:], nil
}

const (
	userMarks     = "-_.!~*'()&=+$,;?/"
	passwordMarks = "-_.!~*'()&=+$,"
)

func checkEscaped(s, marks string) error {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case isAlpha(c), '0' <= c && c <= '9', strings.IndexByte(marks, c) >= 0:
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			i += 2
		default:
			return fmt.Errorf("invalid character %q", c)
		}
	}

	return nil
}

func isHex(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

func isTelNumber(s string) bool {
	s = strings.TrimPrefix(s, "+")
	digits := 0

	for i := range len(s) {
		switch c := s[i]; {
		case isHex(c), c == '*', c == '#':
			digits++
		case strings.IndexByte("-.()", c) >= 0:
		default:
			return false
		}
	}

	return digits > 0
}

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

func HostAddr(host string) (netip.Addr, bool) {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")

	a, err := netip.ParseAddr(host)
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}

	return a, true
}

func FormatHost(a netip.Addr) string {
	if a.Is6() && !a.Is4In6() {
		return "[" + a.String() + "]"
	}

	return a.Unmap().String()
}
