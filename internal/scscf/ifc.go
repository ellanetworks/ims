package scscf

import (
	"bytes"
	"cmp"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/sip"
)

// serviceProfile is the service profile of the subscription that holds the public identity u: under a distinct
// identity, or else a wildcarded one u matches (TS 29.228 §6.1.4.1, Annex B.2.1; TS 23.003 §13.4A, §13.5).
func serviceProfile(sub cx.IMSSubscription, u sip.URI, homeDomain string) (cx.ServiceProfile, bool) {
	keys := identityKeys(u, homeDomain)

	for _, p := range sub.ServiceProfiles {
		for _, id := range p.PublicIdentities {
			if !wildcarded(id) && slices.Contains(keys, identityKeyOf(id.Identity)) {
				return p, true
			}
		}
	}

	for _, p := range sub.ServiceProfiles {
		for _, id := range p.PublicIdentities {
			for _, w := range []string{id.Identity, id.WildcardedPSI, id.WildcardedIMPU} {
				if wildcarded(id) && wildcardMatch(w, u) {
					return p, true
				}
			}
		}
	}

	return cx.ServiceProfile{}, false
}

// serviceProfileOf is the service profile of u in a subscription the HSS sent, under the wildcarded identity the HSS
// says u falls in, if it said, or else as serviceProfile finds it (TS 29.228 Table 6.1.2.2, §6.1.2.1).
func serviceProfileOf(sub cx.IMSSubscription, u sip.URI, wildcard, homeDomain string) (cx.ServiceProfile, bool) {
	if wildcard != "" {
		for _, p := range sub.ServiceProfiles {
			for _, id := range p.PublicIdentities {
				if wildcarded(id) && slices.ContainsFunc([]string{id.Identity, id.WildcardedPSI, id.WildcardedIMPU},
					func(w string) bool { return w != "" && strings.EqualFold(w, wildcard) }) {
					return p, true
				}
			}
		}
	}

	return serviceProfile(sub, u, homeDomain)
}

func wildcarded(id cx.ProfileIdentity) bool {
	switch id.Type {
	case cx.IdentityWildcardedPSI, cx.IdentityWildcardedIMPU, cx.IdentityNonDistinctIMPU:
		return true
	}

	return false
}

// wildcardMatch reports whether u, in its canonical form, is one of the identities of a wildcarded identity: its fixed
// parts around the regular expression the outer pair of "!" delimits (TS 23.003 §13.4, §13.5). The domain of the
// wildcarded identity matches regardless of case, as the canonical form lowers that of u (RFC 3261 §19.1.4).
func wildcardMatch(wildcard string, u sip.URI) bool {
	i, j := strings.IndexByte(wildcard, '!'), strings.LastIndexByte(wildcard, '!')
	if i < 0 || i == j {
		return false
	}

	re := compiled("^" + regexp.QuoteMeta(wildcard[:i]) + "(?:" + wildcard[i+1:j] + ")" + regexp.QuoteMeta(strings.ToLower(wildcard[j+1:])) + "$")

	return re != nil && re.MatchString(canonicalIdentity(u))
}

// filterCriteria are the initial filter criteria of a service profile for a user registered or not, in priority
// order: those of its part, and those of the common part (TS 29.228 Annex B.2.2, TS 23.218 §5.2.3).
func filterCriteria(p cx.ServiceProfile, registered bool) []cx.InitialFilterCriteria {
	part := cx.ProfilePartUnregistered
	if registered {
		part = cx.ProfilePartRegistered
	}

	var out []cx.InitialFilterCriteria

	for _, c := range p.InitialFilterCriteria {
		if c.ProfilePart == nil || *c.ProfilePart == part {
			out = append(out, c)
		}
	}

	slices.SortStableFunc(out, func(a, b cx.InitialFilterCriteria) int { return cmp.Compare(a.Priority, b.Priority) })

	return out
}

// invalidExpression is the first regular expression of a trigger point that does not compile (TS 29.228 Annex F): an
// SPT that cannot be evaluated, which negated would match every request.
func invalidExpression(tp *cx.TriggerPoint) (string, bool) {
	if tp == nil {
		return "", false
	}

	for _, s := range tp.ServicePointTriggers {
		var exprs []string

		switch {
		case s.RequestURI != nil:
			exprs = []string{*s.RequestURI}
		case s.SIPHeader != nil:
			exprs = []string{"(?i)" + s.SIPHeader.Header}
			if s.SIPHeader.Content != nil {
				exprs = append(exprs, *s.SIPHeader.Content)
			}
		case s.SessionDescription != nil:
			exprs = []string{s.SessionDescription.Line}
			if s.SessionDescription.Content != nil {
				exprs = append(exprs, *s.SessionDescription.Content)
			}
		}

		for _, e := range exprs {
			if compiled(e) == nil {
				return e, true
			}
		}
	}

	return "", false
}

// trigger is what the trigger points of the iFCs are checked against: a request, in a session case, and the type of
// a REGISTER (TS 29.228 Annex B.2.3).
type trigger struct {
	req     *sip.Request
	sescase cx.SessionCase
	regType cx.RegistrationType
}

// matches reports whether the trigger point is met: a conjunction of disjunctions of SPTs, each group a
// disjunction, in CNF, or the other way round in DNF; no trigger point always is (TS 29.228 Annex B.2.2, Annex C).
func (t trigger) matches(tp *cx.TriggerPoint) bool {
	if tp == nil {
		return true
	}

	groups := make(map[int32][]bool)

	var order []int32

	for _, s := range tp.ServicePointTriggers {
		v := t.condition(s) != s.Negated

		for _, g := range s.Groups {
			if _, ok := groups[g]; !ok {
				order = append(order, g)
			}

			groups[g] = append(groups[g], v)
		}
	}

	for _, g := range order {
		var met bool

		if tp.ConjunctiveNormalForm {
			met = slices.Contains(groups[g], true)
		} else {
			met = !slices.Contains(groups[g], false)
		}

		if met != tp.ConjunctiveNormalForm {
			return met
		}
	}

	return tp.ConjunctiveNormalForm
}

// condition reports whether the SPT's condition holds, before its negation (TS 29.228 Annex B.2.3, Annex F).
func (t trigger) condition(s cx.ServicePointTrigger) bool {
	switch {
	case s.RequestURI != nil:
		return search(*s.RequestURI, requestURIPart(t.req.URI))
	case s.Method != nil:
		// RFC 3261 §7.1: the method is case-sensitive.
		if *s.Method != t.req.Method {
			return false
		}

		return t.req.Method != "REGISTER" || len(s.RegistrationTypes) == 0 || slices.Contains(s.RegistrationTypes, t.regType)
	case s.SIPHeader != nil:
		return t.header(*s.SIPHeader)
	case s.SessionCase != nil:
		return *s.SessionCase == t.sescase
	case s.SessionDescription != nil:
		return t.sessionDescription(*s.SessionDescription)
	}

	return false
}

// requestURIPart is what a RequestURI SPT matches: the hostport of a SIP URI, the telephone-subscriber of a tel URI
// (TS 29.228 Annex F).
func requestURIPart(u sip.URI) string {
	if u.IsTel() {
		_, sub, _ := strings.Cut(u.String(), ":")
		return sub
	}

	return u.HostPort()
}

// header matches each header field of the request, and each of the values of a list (TS 29.228 Annex F). The header
// name matches regardless of case (RFC 3261 §7.3.1).
func (t trigger) header(m cx.HeaderMatch) bool {
	for _, f := range t.req.Header {
		name := sip.LongName(f.Name)
		if !search("(?i)"+m.Header, name) {
			continue
		}

		if m.Content == nil {
			return true
		}

		values := []string{f.Value}
		if !singleValued(name) {
			values = sip.SplitList(f.Value)
		}

		for _, v := range values {
			if search(*m.Content, headerValue(v)) {
				return true
			}
		}
	}

	return false
}

// singleValued reports a header field whose value is not a comma-separated list but may hold commas: the exceptions
// of RFC 3261 §7.3.1, and the fields of a date, a comment or free text (RFC 3261 §25.1).
func singleValued(name string) bool {
	for _, n := range []string{
		"Authorization", "Proxy-Authorization", "WWW-Authenticate", "Proxy-Authenticate", "Authentication-Info",
		"Date", "Subject", "Organization", "Server", "User-Agent", "Retry-After",
	} {
		if strings.EqualFold(name, n) {
			return true
		}
	}

	return false
}

// headerValue is a header value as Annex F of TS 29.228 matches it: each run of linear white space reduced to a SP,
// and the optional white space around separators (SWS, RFC 3261 §25.1) removed, outside quoted strings.
func headerValue(v string) string {
	v = collapseLWS(v)

	var b []byte

	quoted := false

	for i := 0; i < len(v); i++ {
		c := v[i]

		switch {
		case quoted && c == '\\' && i+1 < len(v):
			b = append(b, c, v[i+1])
			i++
		case c == '"':
			// LDQUOT and RDQUOT (RFC 3261 §25.1).
			if !quoted {
				b = trimSP(b)
			} else if i+1 < len(v) && v[i+1] == ' ' {
				b = append(b, c)
				i++

				quoted = false

				continue
			}

			quoted = !quoted
			b = append(b, c)
		case !quoted && c == ' ' && (len(b) > 0 && strings.IndexByte(";=,:/*()>", b[len(b)-1]) >= 0 ||
			i+1 < len(v) && strings.IndexByte(";=,:/*()<", v[i+1]) >= 0):
		default:
			b = append(b, c)
		}
	}

	return string(b)
}

func trimSP(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == ' ' {
		b = b[:len(b)-1]
	}

	return b
}

// sessionDescription matches each field of the SDP of the request: its type, and its value (TS 29.228 Annex F).
func (t trigger) sessionDescription(m cx.SessionDescriptionMatch) bool {
	sdp, ok, err := t.req.Content().Find("application/sdp")
	if err != nil || !ok {
		return false
	}

	for line := range bytes.SplitSeq(sdp.Body, []byte("\n")) {
		typ, value, ok := strings.Cut(strings.TrimRight(string(line), "\r"), "=")
		if ok && search(m.Line, typ) && (m.Content == nil || search(*m.Content, value)) {
			return true
		}
	}

	return false
}

// collapseLWS reduces each run of linear white space to a single space (TS 29.228 Annex F).
func collapseLWS(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

// search reports whether the regular expression matches part of s, as regexec does with an extended regular
// expression (TS 29.228 Annex F): RE2 accepts the ERE syntax. An invalid expression matches nothing.
func search(expr, s string) bool {
	re := compiled(expr)

	return re != nil && re.MatchString(s)
}

var expressions sync.Map

func compiled(expr string) *regexp.Regexp {
	if re, ok := expressions.Load(expr); ok {
		return re.(*regexp.Regexp)
	}

	re, err := regexp.Compile(expr)
	if err != nil {
		re = nil
	}

	expressions.Store(expr, re)

	return re
}
