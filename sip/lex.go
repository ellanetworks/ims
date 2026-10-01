package sip

import (
	"errors"
	"strings"
)

func isTokenByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}

	return strings.IndexByte("-.!%*_+`'~", c) >= 0
}

func isToken(s string) bool {
	return s != "" && tokenLen(s) == len(s)
}

func tokenLen(s string) int {
	n := 0
	for n < len(s) && isTokenByte(s[n]) {
		n++
	}

	return n
}

func isWSP(c byte) bool {
	return c == ' ' || c == '\t'
}

func trimWSP(s string) string {
	return strings.Trim(s, " \t")
}

func trimLeftWSP(s string) string {
	return strings.TrimLeft(s, " \t")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

var (
	errSyntax = errors.New("invalid number")
	errRange  = errors.New("number out of range")
)

func parseUint(s string, maxValue uint64) (uint64, error) {
	if !isDigits(s) {
		return 0, errSyntax
	}

	var n uint64

	for i := range len(s) {
		n = n*10 + uint64(s[i]-'0')
		if n > maxValue {
			return 0, errRange
		}
	}

	return n, nil
}

func quotedLen(s string) (int, error) {
	if s == "" || s[0] != '"' {
		return 0, errors.New("missing opening quote")
	}

	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i + 1, nil
		}
	}

	return 0, errors.New("unterminated quoted string")
}

func Unquote(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}

	s = s[1 : len(s)-1]
	if strings.IndexByte(s, '\\') < 0 {
		return s
	}

	var b strings.Builder

	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}

		b.WriteByte(s[i])
	}

	return b.String()
}

func Quote(s string) string {
	var b strings.Builder

	b.WriteByte('"')

	for i := range len(s) {
		switch c := s[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\r', '\n':
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
		}
	}

	b.WriteByte('"')

	return b.String()
}

func SplitList(v string) []string {
	var (
		out    []string
		start  int
		quoted bool
		angle  bool
	)

	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case quoted:
		case c == '<':
			angle = true
		case c == '>':
			angle = false
		case c == ',' && !angle:
			out = append(out, trimWSP(v[start:i]))
			start = i + 1
		}
	}

	return append(out, trimWSP(v[start:]))
}

func firstListElement(v string) (first, rest string) {
	quoted, angle := false, false

	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case quoted:
		case c == '<':
			angle = true
		case c == '>':
			angle = false
		case c == ',' && !angle:
			return trimWSP(v[:i]), trimWSP(v[i+1:])
		}
	}

	return trimWSP(v), ""
}
