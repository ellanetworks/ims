package sip

import "strings"

// TS 23.003 clause 13.4
// TS 29.229 clause 6.3.2
// RFC 3966 section 5.1.1
// RFC 3261 section 19.1.4
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

		if v, _ := u.Params.Get("user"); strings.EqualFold(v, "phone") {
			user, _, _ = strings.Cut(user, ";")
		}

		return URI{Scheme: "sip", User: user, Host: strings.ToLower(u.Host)}
	}

	u = u.Clone()
	u.Headers = ""

	return u
}

// RFC 3966 section 4
func phoneContext(pc string) string {
	if strings.HasPrefix(pc, "+") {
		pc = withoutVisualSeparators(pc)
	}

	return strings.ToLower(pc)
}

// RFC 3966 section 3
func withoutVisualSeparators(number string) string {
	return strings.Map(func(c rune) rune {
		if strings.ContainsRune("-.()", c) {
			return -1
		}

		return c
	}, number)
}

// RFC 2396 section 2.2
// RFC 3261 section 19.1.4
const reserved = ";/?:@&=+$,"

// RFC 3261 section 19.1.4
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
