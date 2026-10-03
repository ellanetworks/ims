package scscf

import (
	"net/url"
	"slices"
	"strings"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

type Numbering struct {
	CountryCode         string
	NationalPrefix      string
	InternationalPrefix string
}

const (
	minE164 = 7
	maxE164 = 15
)

func telephoneNumber(u sip.URI) (number, context string, params sip.Params, ok bool) {
	switch {
	case u.IsTel():
		for _, p := range u.Params {
			if strings.EqualFold(p.Name, "phone-context") {
				context = p.Value
			} else {
				params = append(params, p)
			}
		}

		return u.User, context, params, true
	case u.IsSIP():
		if user, _ := u.Params.Get("user"); !strings.EqualFold(user, "phone") {
			return "", "", nil, false
		}

		user := u.User
		if unescaped, err := url.PathUnescape(user); err == nil {
			user = unescaped
		}

		number, rest, _ := strings.Cut(user, ";")

		for p := range strings.SplitSeq(rest, ";") {
			name, value, _ := strings.Cut(p, "=")

			switch {
			case name == "":
			case strings.EqualFold(name, "phone-context"):
				context = value
			default:
				params = append(params, sip.Param{Name: name, Value: value})
			}
		}

		if context == "" {
			context = u.Host
		}

		return number, context, params, number != ""
	}

	return "", "", nil, false
}

func globalNumber(u sip.URI) (string, bool) {
	number, _, _, ok := telephoneNumber(u)
	if !ok {
		return "", false
	}

	number = withoutSeparators(number)

	return number, len(number) > 1 && number[0] == '+' && isDigits(number[1:])
}

// RFC 3966 §5.4
func (n Numbering) normalise(u sip.URI, homeDomain string) (sip.URI, bool) {
	if n.CountryCode == "" {
		return u, false
	}

	number, context, params, ok := telephoneNumber(u)
	digits := withoutSeparators(number)

	if !ok || !isDigits(digits) || !n.homeContext(context, homeDomain) {
		return u, false
	}

	switch {
	case n.InternationalPrefix != "" && strings.HasPrefix(digits, n.InternationalPrefix):
		digits = digits[len(n.InternationalPrefix):]
	case n.NationalPrefix != "" && strings.HasPrefix(digits, n.NationalPrefix):
		digits = n.CountryCode + digits[len(n.NationalPrefix):]
	default:
		digits = n.CountryCode + digits
	}

	if len(digits) < minE164 || len(digits) > maxE164 || digits[0] == '0' {
		return u, false
	}

	return sip.URI{Scheme: "tel", User: "+" + digits, Params: params}, true
}

// TS 24.229 §5.4.3.2 step 10
func localNumber(u sip.URI, homeDomain string) bool {
	number, _, _, ok := telephoneNumber(u)
	if !ok || strings.HasPrefix(number, "+") {
		return false
	}

	host := strings.ToLower(strings.TrimSuffix(u.Host, "."))

	return u.IsTel() || host == homeDomain || strings.HasSuffix(host, "."+homeDomain)
}

// TS 24.229 §7.2A.10.3, RFC 3966
func (n Numbering) homeContext(context, homeDomain string) bool {
	context = strings.ToLower(strings.TrimSuffix(context, "."))

	switch {
	case context == homeDomain || strings.HasSuffix(context, "."+homeDomain):
		return true
	case strings.HasPrefix(context, "+"):
		return withoutSeparators(context) == "+"+n.CountryCode
	}

	return !isDomainName(context)
}

// RFC 3966
func isDomainName(s string) bool {
	if s == "" {
		return false
	}

	labels := strings.Split(s, ".")

	for _, l := range labels {
		if l == "" || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}

		if strings.ContainsFunc(l, func(c rune) bool { return (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' }) {
			return false
		}
	}

	top := labels[len(labels)-1]

	return top[0] >= 'a' && top[0] <= 'z'
}

var numberPortability = []string{"rn", "npdi"}

func calledPartyID(u sip.URI) sip.URI {
	u = u.Clone()
	ported := func(name string) bool {
		return slices.ContainsFunc(numberPortability, func(p string) bool { return strings.EqualFold(p, name) })
	}

	switch {
	case u.IsTel():
		u.Params = slices.DeleteFunc(u.Params, func(p sip.Param) bool { return ported(p.Name) })
	case u.IsSIP():
		number, rest, ok := strings.Cut(u.User, ";")
		if !ok {
			return u
		}

		kept := []string{number}

		for p := range strings.SplitSeq(rest, ";") {
			if name, _, _ := strings.Cut(p, "="); !ported(name) {
				kept = append(kept, p)
			}
		}

		u.User = strings.Join(kept, ";")
	}

	return u
}

func identityKeys(u sip.URI, homeDomain string) []string {
	keys := []string{identityKey(u)}

	add := func(k string) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}

	if number, ok := globalNumber(u); ok {
		add(identityKey(sip.URI{Scheme: "tel", User: number}))
		add(identityKey(phoneURI(number, homeDomain)))

		return keys
	}

	if u.IsTel() {
		add(identityKey(sip.URI{Scheme: "tel", User: u.User}))
	}

	return keys
}

func phoneURI(number, homeDomain string) sip.URI {
	u := sip.URI{Scheme: "sip", User: number, Host: homeDomain}
	u.Params.Set("user", "phone")

	return u
}

// TS 24.229 §5.4.3.2 step 9, §5.4.3.3
func assertedAlias(asserted sip.Address, reg db.Registration, homeDomain string) (sip.Address, bool) {
	number, global := globalNumber(asserted.URI)

	switch {
	case asserted.URI.IsTel() && global:
		return sip.Address{Display: asserted.Display, URI: phoneURI(number, homeDomain)}, true
	case asserted.URI.IsTel() || !asserted.URI.IsSIP():
		return sip.Address{}, false
	case global:
		tel := sip.URI{Scheme: "tel", User: number}
		a := sip.Address{Display: asserted.Display, URI: tel}

		if pi, ok := profileIdentity(reg, identityKey(tel)); ok {
			if pi.Barred {
				return sip.Address{}, false
			}

			if pi.DisplayName != "" {
				a.Display = sip.Quote(pi.DisplayName)
			}
		}

		return a, true
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

func profileIdentity(reg db.Registration, key string) (cx.ProfileIdentity, bool) {
	sub, err := cx.ParseUserData(reg.UserData)
	if err != nil {
		return cx.ProfileIdentity{}, false
	}

	for _, profile := range sub.ServiceProfiles {
		for _, pi := range profile.PublicIdentities {
			if identityKeyOf(pi.Identity) == key {
				return pi, true
			}
		}
	}

	return cx.ProfileIdentity{}, false
}

// TS 29.228 §6.6.4
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
