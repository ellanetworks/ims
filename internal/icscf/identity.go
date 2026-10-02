package icscf

import (
	"errors"
	"strings"

	"github.com/ellanetworks/ims/sip"
)

var errNoIdentity = errors.New("no public identity")

// publicIdentity is the identity the HSS is queried with: a SIP or SIPS URI
// reduced to "sip", user and host, as Kamailio builds it, or a tel URI
// without parameters or visual separators (RFC 3966 §4).
func publicIdentity(u sip.URI) (string, error) {
	switch {
	case u.IsSIP() && u.Host != "":
		return sip.URI{Scheme: "sip", User: u.User, Host: strings.ToLower(u.Host)}.String(), nil
	case u.IsTel() && u.User != "":
		return sip.URI{Scheme: "tel", User: withoutSeparators(u.User)}.String(), nil
	default:
		return "", errNoIdentity
	}
}

// sessionIdentity is the identity of the user a request is for or from. A
// SIP URI for a global number with "user=phone" names the tel URI of that
// number (TS 24.229 §5.3.2.1 1b), and so does a GRUU on such a URI (1c).
func sessionIdentity(u sip.URI) (string, error) {
	if number, ok := globalNumber(u); ok {
		return publicIdentity(sip.URI{Scheme: "tel", User: number})
	}

	return publicIdentity(u)
}

// globalNumber returns the global number of a SIP URI with "user=phone",
// without the tel parameters of its user part.
func globalNumber(u sip.URI) (string, bool) {
	user, _ := u.Params.Get("user")

	if !u.IsSIP() || !strings.EqualFold(user, "phone") || !strings.HasPrefix(u.User, "+") {
		return "", false
	}

	number, _, _ := strings.Cut(u.User, ";")

	return number, len(number) > 1
}

// withoutSeparators removes the visual separators of a telephone number,
// which do not take part in comparisons (RFC 3966 §4).
func withoutSeparators(number string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune("-.()", r) {
			return -1
		}

		return r
	}, number)
}

// privateIdentity derives the IMPI from the IMPU being registered: the URI
// without its scheme, port and parameters (TS 24.229 §5.3.1.2).
func privateIdentity(u sip.URI) string {
	switch {
	case u.IsTel(), u.User == "":
		return u.User + u.Host
	default:
		return u.User + "@" + u.Host
	}
}

// telURI converts a SIP URI for a global number with "user=phone" to a tel
// URI that keeps the number's parameters (TS 24.229 §5.3.2.1 1b).
func telURI(u sip.URI) (sip.URI, bool, error) {
	if _, ok := globalNumber(u); !ok || u.Params.Has("gr") {
		return u, false, nil
	}

	tel, err := sip.ParseURI("tel:" + u.User)
	if err != nil {
		return sip.URI{}, false, err
	}

	return tel, true, nil
}

// credentials returns the Authorization header field for the home domain, or
// the first Digest one.
func credentials(h sip.Header, realm string) (sip.Auth, bool) {
	var (
		first sip.Auth
		found bool
	)

	for _, v := range h.Values("Authorization") {
		a, err := sip.ParseAuth(v)
		if err != nil || !strings.EqualFold(a.Scheme, "Digest") {
			continue
		}

		if r, _ := a.Params.Get("realm"); strings.EqualFold(sip.Unquote(r), realm) {
			return a, true
		}

		if !found {
			first, found = a, true
		}
	}

	return first, found
}

func authParam(a sip.Auth, name string) string {
	v, _ := a.Params.Get(name)
	return sip.Unquote(v)
}

// visitedNetwork returns the first P-Visited-Network-ID value, unquoted and
// without parameters.
func visitedNetwork(h sip.Header) string {
	values := h.Elements("P-Visited-Network-ID")
	if len(values) == 0 {
		return ""
	}

	v := strings.TrimSpace(values[0])

	if strings.HasPrefix(v, `"`) {
		if end := closingQuote(v); end > 0 {
			return sip.Unquote(v[:end+1])
		}

		return ""
	}

	v, _, _ = strings.Cut(v, ";")

	return strings.TrimSpace(v)
}

func closingQuote(s string) int {
	for k := 1; k < len(s); k++ {
		switch s[k] {
		case '\\':
			k++
		case '"':
			return k
		}
	}

	return -1
}
