package sip

import "strings"

// Identity is u as a public user identity or public service identity in its canonical form (TS 23.003 §13.4, TS
// 29.229 §6.3.2): a SIP URI of its user and host, without port, parameters and headers, the user part of a number
// (user=phone) without its parameters; a tel URI of its number without visual separators (RFC 3966 §5.1.1) and, of a
// local number, its phone-context. Escapes are normalized (RFC 3261 §19.1.4) and the host and tel URI lower-cased,
// so that equivalent identities have one canonical form. A URI of another scheme loses its headers only.
func (u URI) Identity() URI {
	switch {
	case u.IsTel():
		c := URI{Scheme: "tel", User: strings.ToLower(withoutVisualSeparators(u.User))}

		if pc, ok := u.Params.Get("phone-context"); ok && !strings.HasPrefix(c.User, "+") {
			c.Params.Set("phone-context", phoneContext(pc))
		}

		return c
	case u.IsSIP():
		user := normalizeEscapes(u.User)

		// TS 23.003 §13.4: the user part of a number follows the tel URI, without its parameters.
		if v, _ := u.Params.Get("user"); strings.EqualFold(v, "phone") {
			user, _, _ = strings.Cut(user, ";")
		}

		return URI{Scheme: "sip", User: user, Host: strings.ToLower(u.Host)}
	}

	u = u.Clone()
	u.Headers = ""

	return u
}

// phoneContext is a phone-context in its canonical form (RFC 3966 §4): a domain name lower-cased, global number digits
// without visual separators.
func phoneContext(pc string) string {
	if strings.HasPrefix(pc, "+") {
		pc = withoutVisualSeparators(pc)
	}

	return strings.ToLower(pc)
}

// withoutVisualSeparators is a tel URI number without its visual separators (RFC 3966 §3: "-", ".", "(", ")").
func withoutVisualSeparators(number string) string {
	return strings.Map(func(c rune) rune {
		if strings.ContainsRune("-.()", c) {
			return -1
		}

		return c
	}, number)
}

// reserved are the characters of RFC 2396 §2.2 whose escapes are not equivalent to them (RFC 3261 §19.1.4).
const reserved = ";/?:@&=+$,"

// normalizeEscapes is s with the escapes of characters other than reserved ones decoded, and the others' hex digits
// upper-cased: equal for URIs equivalent by RFC 3261 §19.1.4 ("Characters other than those in the 'reserved' set
// are equivalent to their '%' HEX HEX encoding").
func normalizeEscapes(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}

	var b strings.Builder

	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+2 >= len(s) {
			b.WriteByte(s[i])
			continue
		}

		hi, lo := unhex(s[i+1]), unhex(s[i+2])
		if hi < 0 || lo < 0 {
			b.WriteByte(s[i])
			continue
		}

		c := byte(hi<<4 | lo)
		if strings.IndexByte(reserved, c) >= 0 || c < 0x21 || c > 0x7e || c == '%' {
			b.WriteByte('%')
			b.WriteByte(strings.ToUpper(s[i+1 : i+2])[0])
			b.WriteByte(strings.ToUpper(s[i+2 : i+3])[0])
		} else {
			b.WriteByte(c)
		}

		i += 2
	}

	return b.String()
}

func unhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}

	return -1
}
