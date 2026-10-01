package scscf

import (
	"strconv"
	"strings"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

// normalizeURI returns the form public identities are stored and compared in:
// no parameters or headers, a lowercase scheme and host, and a tel number
// without visual separators.
func normalizeURI(u sip.URI) string {
	scheme := strings.ToLower(u.Scheme)

	switch {
	case u.IsSIP():
		s := scheme + ":"
		if u.User != "" {
			s += u.User + "@"
		}

		s += strings.ToLower(u.Host)
		if u.Port != 0 {
			s += ":" + strconv.Itoa(int(u.Port))
		}

		return s
	case u.IsTel():
		return scheme + ":" + strings.Map(func(c rune) rune {
			if strings.ContainsRune("-.()", c) {
				return -1
			}

			return c
		}, u.User)
	default:
		return scheme + ":" + u.Opaque
	}
}

func normalizeIdentity(s string) string {
	u, err := sip.ParseURI(s)
	if err != nil {
		return s
	}

	return normalizeURI(u)
}

// privateIdentity derives the IMPI from the IMPU of a REGISTER without
// credentials (TS 33.203 §6.1.1): the IMPU without its scheme.
func privateIdentity(impu sip.URI) string {
	if impu.User == "" {
		return strings.ToLower(impu.Host)
	}

	return impu.User + "@" + strings.ToLower(impu.Host)
}

// implicitSet returns the implicit registration set of impu: the public user
// identities of the service profile holding it, in profile order, so the first
// one is the default. found is false when no profile holds impu.
func implicitSet(sub cx.IMSSubscription, impu string) ([]db.PublicIdentity, bool) {
	for _, profile := range sub.ServiceProfiles {
		var (
			set   []db.PublicIdentity
			found bool
			seen  = make(map[string]bool)
		)

		for _, pi := range profile.PublicIdentities {
			if pi.Type != cx.IdentityDistinctPublicUserIdentity && pi.Type != cx.IdentityNonDistinctIMPU {
				continue
			}

			uri := normalizeIdentity(pi.Identity)
			if seen[uri] {
				continue
			}

			seen[uri] = true
			found = found || uri == impu
			set = append(set, db.PublicIdentity{URI: uri, Barred: pi.Barred})
		}

		if found {
			return set, true
		}
	}

	return nil, false
}

// lookupIdentity finds impu in a registration set.
func lookupIdentity(set []db.PublicIdentity, impu string) (db.PublicIdentity, bool) {
	for _, id := range set {
		if id.URI == impu {
			return id, true
		}
	}

	return db.PublicIdentity{}, false
}

// associatedURIs is the P-Associated-URI value: the non-barred identities, the
// default one first.
func associatedURIs(set []db.PublicIdentity) string {
	var b strings.Builder

	for _, id := range set {
		if id.Barred {
			continue
		}

		if b.Len() > 0 {
			b.WriteString(", ")
		}

		b.WriteString("<" + id.URI + ">")
	}

	return b.String()
}
