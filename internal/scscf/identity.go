package scscf

import (
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

func identityKey(u sip.URI) string {
	scheme := strings.ToLower(u.Scheme)

	switch {
	case u.IsSIP():
		s := scheme + ":"
		if u.User != "" {
			s += u.User
			if u.Password != "" {
				s += ":" + u.Password
			}

			s += "@"
		}

		s += strings.ToLower(u.Host)
		if u.Port != 0 {
			s += ":" + strconv.Itoa(int(u.Port))
		}

		var params []string

		for _, name := range []string{"maddr", "method", "ttl", "user"} {
			if v, ok := u.Params.Get(name); ok {
				params = append(params, ";"+name+"="+strings.ToLower(v))
			}
		}

		return s + strings.Join(params, "")
	case u.IsTel():
		s := scheme + ":" + strings.Map(func(c rune) rune {
			if strings.ContainsRune("-.()", c) {
				return -1
			}

			return c
		}, u.User)

		if v, ok := u.Params.Get("phone-context"); ok {
			s += ";phone-context=" + strings.ToLower(v)
		}

		return s
	default:
		return scheme + ":" + u.Opaque
	}
}

func identityKeyOf(s string) string {
	u, err := sip.ParseURI(s)
	if err != nil {
		return s
	}

	return identityKey(u)
}

// canonicalIdentity is the form of a public identity on Cx: "sip:user@domain", without port or parameters, or a tel
// URI of a global number without parameters or visual separators (TS 29.229 §6.3.2, TS 23.003 §13.4). A local number
// keeps the phone-context that scopes it (RFC 3966 §5.1.5).
func canonicalIdentity(u sip.URI) string {
	switch {
	case u.IsTel():
		c := sip.URI{Scheme: "tel", User: withoutSeparators(u.User)}

		if pc, ok := u.Params.Get("phone-context"); ok && !strings.HasPrefix(c.User, "+") {
			c.Params.Set("phone-context", pc)
		}

		return c.String()
	case u.IsSIP():
		user := u.User

		// TS 23.003 §13.4: the user part of a number follows the tel URI, without its parameters.
		if v, _ := u.Params.Get("user"); strings.EqualFold(v, "phone") {
			user, _, _ = strings.Cut(user, ";")
		}

		return sip.URI{Scheme: "sip", User: user, Host: strings.ToLower(u.Host)}.String()
	}

	return receivedIdentity(u)
}

func receivedIdentity(u sip.URI) string {
	u = u.Clone()
	u.Headers = ""

	return u.String()
}

func privateIdentity(impu sip.URI) string {
	if impu.User == "" {
		return impu.Host
	}

	return impu.User + "@" + impu.Host
}

func implicitSet(sub cx.IMSSubscription) []db.PublicIdentity {
	var (
		set  []db.PublicIdentity
		seen = make(map[string]bool)
	)

	for _, profile := range sub.ServiceProfiles {
		for _, pi := range profile.PublicIdentities {
			if pi.Type != cx.IdentityDistinctPublicUserIdentity && pi.Type != cx.IdentityNonDistinctIMPU {
				continue
			}

			key := identityKeyOf(pi.Identity)
			if seen[key] {
				continue
			}

			seen[key] = true
			set = append(set, db.PublicIdentity{URI: pi.Identity, Key: key, DisplayName: pi.DisplayName, Barred: pi.Barred})
		}
	}

	return set
}

func holds(set []db.PublicIdentity, key string) bool {
	return slices.ContainsFunc(set, func(id db.PublicIdentity) bool { return id.Key == key })
}

func hasUnbarred(set []db.PublicIdentity) bool {
	return slices.ContainsFunc(set, func(id db.PublicIdentity) bool { return !id.Barred })
}

func associatedURIs(set []db.PublicIdentity) string {
	var uris []string

	for _, id := range set {
		if id.Barred {
			continue
		}

		uri := "<" + id.URI + ">"
		if id.DisplayName != "" {
			uri = sip.Quote(id.DisplayName) + " " + uri
		}

		uris = append(uris, uri)
	}

	return strings.Join(uris, ", ")
}
