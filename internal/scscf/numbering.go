package scscf

import (
	"strings"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

// Numbering turns home-local numbers into global ones: a number dialled in
// the home domain's context loses its national prefix and gains the country
// code (TS 24.229 §5.4.3.2 step 10, IR.92 §2.2). A zero Numbering leaves
// local numbers as dialled.
type Numbering struct {
	CountryCode    string
	NationalPrefix string
}

// telephoneNumber splits a tel URI, or a SIP URI with user=phone, into its
// number and phone-context. A SIP URI without phone-context is in the context
// of its host.
func telephoneNumber(u sip.URI) (number, context string, ok bool) {
	switch {
	case u.IsTel():
		context, _ = u.Params.Get("phone-context")
		return u.User, context, true
	case u.IsSIP():
		if user, _ := u.Params.Get("user"); !strings.EqualFold(user, "phone") {
			return "", "", false
		}

		number, params, _ := strings.Cut(u.User, ";")

		for p := range strings.SplitSeq(params, ";") {
			if name, value, _ := strings.Cut(p, "="); strings.EqualFold(name, "phone-context") {
				context = value
			}
		}

		if context == "" {
			context = u.Host
		}

		return number, context, number != ""
	}

	return "", "", false
}

// globalNumber is the E.164 number, "+" included, of a tel URI or a SIP URI
// with user=phone that holds a global number.
func globalNumber(u sip.URI) (string, bool) {
	number, _, ok := telephoneNumber(u)
	if !ok {
		return "", false
	}

	number = withoutSeparators(number)

	return number, len(number) > 1 && number[0] == '+' && isDigits(number[1:])
}

// normalise rewrites a home-local number as a global tel URI. It reports
// false, leaving u as it is, for anything else.
func (n Numbering) normalise(u sip.URI, homeDomain string) (sip.URI, bool) {
	if n.CountryCode == "" {
		return u, false
	}

	number, context, ok := telephoneNumber(u)
	digits := withoutSeparators(number)

	switch {
	case !ok || !isDigits(digits):
		return u, false
	case !strings.EqualFold(context, homeDomain) && context != "+"+n.CountryCode:
		return u, false
	}

	if n.NationalPrefix != "" {
		digits = strings.TrimPrefix(digits, n.NationalPrefix)
	}

	return sip.URI{Scheme: "tel", User: "+" + n.CountryCode + digits}, true
}

// identityKeys are the keys under which a public identity may be stored: its
// own, and for a global number both its tel and its home SIP form.
func identityKeys(u sip.URI, homeDomain string) []string {
	keys := []string{identityKey(u)}

	number, ok := globalNumber(u)
	if !ok {
		return keys
	}

	for _, k := range []string{identityKey(sip.URI{Scheme: "tel", User: number}), identityKey(phoneURI(number, homeDomain))} {
		if k != keys[0] {
			keys = append(keys, k)
		}
	}

	return keys
}

func phoneURI(number, homeDomain string) sip.URI {
	u := sip.URI{Scheme: "sip", User: number, Host: homeDomain}
	u.Params.Set("user", "phone")

	return u
}

// assertedAlias is the second P-Asserted-Identity of TS 24.229 §5.4.3.2 step 9
// and §5.4.3.3: the tel URI a SIP URI is an alias of, or the home SIP form of
// a tel URI.
func assertedAlias(asserted sip.Address, reg db.Registration, homeDomain string) (sip.Address, bool) {
	if asserted.URI.IsTel() {
		number, ok := globalNumber(asserted.URI)
		if !ok {
			return sip.Address{}, false
		}

		return sip.Address{Display: asserted.Display, URI: phoneURI(number, homeDomain)}, true
	}

	if !asserted.URI.IsSIP() {
		return sip.Address{}, false
	}

	tel, ok := telAlias(reg, identityKey(asserted.URI))
	if !ok {
		return sip.Address{}, false
	}

	u, err := sip.ParseURI(tel.Identity)
	if err != nil {
		return sip.Address{}, false
	}

	a := sip.Address{URI: u}
	if tel.DisplayName != "" {
		a.Display = sip.Quote(tel.DisplayName)
	}

	return a, true
}

// telAlias finds the unbarred tel URI that is an alias of an identity: in the
// same service profile and, when the HSS groups aliases, the same alias group
// (TS 29.228 §6.6.4).
func telAlias(reg db.Registration, key string) (cx.ProfileIdentity, bool) {
	sub, err := cx.ParseUserData(reg.UserData)
	if err != nil {
		return cx.ProfileIdentity{}, false
	}

	for _, profile := range sub.ServiceProfiles {
		i := -1

		for k, pi := range profile.PublicIdentities {
			if identityKeyOf(pi.Identity) == key {
				i = k
				break
			}
		}

		if i < 0 {
			continue
		}

		self := profile.PublicIdentities[i]

		for _, pi := range profile.PublicIdentities {
			if pi.Barred || !strings.HasPrefix(strings.ToLower(pi.Identity), "tel:") || pi.AliasIdentityGroupID != self.AliasIdentityGroupID {
				continue
			}

			return pi, true
		}
	}

	return cx.ProfileIdentity{}, false
}

func withoutSeparators(number string) string {
	return strings.Map(func(c rune) rune {
		if strings.ContainsRune("-.()", c) {
			return -1
		}

		return c
	}, number)
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}

	return s != ""
}
