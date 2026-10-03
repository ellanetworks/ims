package scscf

import (
	"net/url"
	"slices"
	"strings"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

// Numbering turns local numbers dialled in the home network into global
// ones (TS 24.229 §5.4.3.2 step 10, IR.92 §2.2): an international prefix
// becomes "+", a national prefix gives way to the country code, and any other
// number gains the country code. A Numbering without a country code leaves
// local numbers as dialled.
type Numbering struct {
	CountryCode         string
	NationalPrefix      string
	InternationalPrefix string
}

// E.164 numbers are at most 15 digits, country code included (ITU-T E.164
// §6.1). Shorter local numbers than minE164 are short codes, not subscriber
// numbers, and are left as dialled.
const (
	minE164 = 7
	maxE164 = 15
)

// telephoneNumber splits a tel URI, or a SIP URI with user=phone, into its
// number, phone-context and other parameters. A SIP URI without
// phone-context is in the context of its host.
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

// globalNumber is the E.164 number, "+" included, of a tel URI or a SIP URI
// with user=phone that holds a global number.
func globalNumber(u sip.URI) (string, bool) {
	number, _, _, ok := telephoneNumber(u)
	if !ok {
		return "", false
	}

	number = withoutSeparators(number)

	return number, len(number) > 1 && number[0] == '+' && isDigits(number[1:])
}

// normalise rewrites a local number of the home network as a global tel URI,
// keeping its other parameters (RFC 3966 §5.4). It reports false, leaving u
// as it is, for anything else.
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

// homeContext reports whether a phone-context places a local number in the
// home network:
//   - the home domain, or one of its subdomains, which is where geo-local
//     contexts live (TS 24.229 §7.2A.10.3: geo-local.<home>,
//     <mcc>.<mnc>.eps.<home>, <mcc>.<mnc>.5gs.<home>); a private network has
//     a single geography;
//   - the home country code: the number is national. A longer global number
//     would name an area whose code the number then lacks;
//   - a value that is no RFC 3966 descriptor at all, such as the digits of
//     the caller's own number some Samsung phones send (the Open5GS capture
//     sip/internal/corpus/testdata/open5gs/ipsec_to_ipsec_call): the phone
//     can only mean its own network.
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

// isDomainName is the domainname of RFC 3966 §3: labels of letters, digits
// and hyphens, the last starting with a letter.
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

// numberPortability are the tel URI parameters of RFC 4694 that a
// P-Called-Party-ID leaves out (TS 24.229 §5.4.3.3 step 10c): the S-CSCF
// receives no Loose-Route Indication from the HSS.
var numberPortability = []string{"rn", "npdi"}

// calledPartyID is the Request-URI as the P-Called-Party-ID carries it.
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

// identityKeys are the keys under which a public identity may be stored: its
// own; for a global number both its tel and its home SIP form; and for a local
// tel number also the number without its phone-context, the identity the
// I-CSCF asks the HSS about (internal/icscf publicIdentity), so that a number
// the LIR finds is found here too.
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

// assertedAlias is the second P-Asserted-Identity of TS 24.229 §5.4.3.2 step 9
// and §5.4.3.3: the tel URI a SIP URI is an alias of, or the home SIP form of
// a tel URI.
func assertedAlias(asserted sip.Address, reg db.Registration, homeDomain string) (sip.Address, bool) {
	number, global := globalNumber(asserted.URI)

	switch {
	case asserted.URI.IsTel() && global:
		return sip.Address{Display: asserted.Display, URI: phoneURI(number, homeDomain)}, true
	case asserted.URI.IsTel() || !asserted.URI.IsSIP():
		return sip.Address{}, false
	case global:
		// A SIP URI with user=phone is the alias of the tel URI of its number,
		// by the construction of step 9 b), whether or not the HSS lists both.
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

// profileIdentity finds an identity in the user data the HSS sent.
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
