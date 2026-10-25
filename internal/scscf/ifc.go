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
			if headerName(s.SIPHeader.Header) == nil {
				return "(?i)" + s.SIPHeader.Header, true
			}

			if s.SIPHeader.Content != nil {
				exprs = []string{*s.SIPHeader.Content}
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
//
// The groups are checked in the order they first appear, and each SPT once, when a group first needs it: a group
// that decides the trigger point leaves the SPTs of the next ones unchecked.
func (t trigger) matches(tp *cx.TriggerPoint) bool {
	if tp == nil {
		return true
	}

	var (
		valueBuf [16]int8
		groupBuf [16]int32
	)

	n := len(tp.ServicePointTriggers)

	values := valueBuf[:min(n, len(valueBuf))]
	if n > len(valueBuf) {
		values = make([]int8, n)
	}

	seen := groupBuf[:0]

	for _, s := range tp.ServicePointTriggers {
		for _, g := range s.Groups {
			if slices.Contains(seen, g) {
				continue
			}

			seen = append(seen, g)

			if met := t.group(tp, values, g); met != tp.ConjunctiveNormalForm {
				return met
			}
		}
	}

	return tp.ConjunctiveNormalForm
}

// SPT values of a trigger point as matches works them out.
const (
	sptUnknown int8 = iota
	sptFalse
	sptTrue
)

// group reports whether group g of the trigger point is met: one of its SPTs in CNF, where a group is a disjunction,
// all of them in DNF, where it is a conjunction (TS 29.228 Annex B.2.2). values keeps each SPT's value once checked.
func (t trigger) group(tp *cx.TriggerPoint, values []int8, g int32) bool {
	for i, s := range tp.ServicePointTriggers {
		if !slices.Contains(s.Groups, g) {
			continue
		}

		if values[i] == sptUnknown {
			values[i] = sptFalse
			if t.condition(s) != s.Negated {
				values[i] = sptTrue
			}
		}

		if (values[i] == sptTrue) == tp.ConjunctiveNormalForm {
			return tp.ConjunctiveNormalForm
		}
	}

	return !tp.ConjunctiveNormalForm
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
	header := headerName(m.Header)
	if header == nil {
		return false
	}

	var content *regexp.Regexp

	if m.Content != nil {
		if content = compiled(*m.Content); content == nil {
			return false
		}
	}

	for _, f := range t.req.Header {
		name := sip.LongName(f.Name)
		if !header.MatchString(name) {
			continue
		}

		switch {
		case content == nil:
			return true
		case slices.ContainsFunc(textHeaders, func(n string) bool { return strings.EqualFold(n, name) }):
			if content.MatchString(collapseLWS(f.Value)) {
				return true
			}
		case slices.ContainsFunc(authHeaders, func(n string) bool { return strings.EqualFold(n, name) }):
			if content.MatchString(headerValue(f.Value)) {
				return true
			}
		default:
			for _, v := range sip.SplitList(f.Value) {
				if content.MatchString(headerValue(v)) {
					return true
				}
			}
		}
	}

	return false
}

// authHeaders and textHeaders are the header fields whose value is no comma-separated list but may hold commas
// (RFC 3261 §25.1): the exceptions of RFC 3261 §7.3.1, whose commas separate parameters, and those of a date, a
// comment or free text, whose white space is part of the value rather than SWS.
var (
	authHeaders = []string{"Authorization", "Proxy-Authorization", "WWW-Authenticate", "Proxy-Authenticate", "Authentication-Info"}
	textHeaders = []string{"Date", "Subject", "Organization", "Server", "User-Agent", "Retry-After"}
)

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
		case c == '"' && !quoted:
			// LDQUOT (RFC 3261 §25.1).
			b = append(trimSP(b), c)
			quoted = true
		case c == '"':
			// RDQUOT (RFC 3261 §25.1).
			b = append(b, c)
			quoted = false

			if i+1 < len(v) && v[i+1] == ' ' {
				i++
			}
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

	typ := compiled(m.Line)
	if typ == nil {
		return false
	}

	var content *regexp.Regexp

	if m.Content != nil {
		if content = compiled(*m.Content); content == nil {
			return false
		}
	}

	for line := range bytes.SplitSeq(sdp.Body, []byte("\n")) {
		field, value, ok := bytes.Cut(bytes.TrimRight(line, "\r"), []byte("="))
		if ok && typ.Match(field) && (content == nil || content.Match(value)) {
			return true
		}
	}

	return false
}

// collapseLWS reduces each run of linear white space to a single space, and trims it (TS 29.228 Annex F). LWS is SP,
// HTAB and line folding (RFC 3261 §25.1): other white space of a UTF-8 value, such as a no-break space, is kept.
func collapseLWS(v string) string {
	if !hasLWSRun(v) {
		return v
	}

	b := make([]byte, 0, len(v))
	space := false

	for i := 0; i < len(v); i++ {
		switch c := v[i]; c {
		case ' ', '\t', '\r', '\n':
			space = len(b) > 0
		default:
			if space {
				b = append(b, ' ')
				space = false
			}

			b = append(b, c)
		}
	}

	return string(b)
}

// hasLWSRun reports whether v has LWS that collapseLWS changes: other than a SP between two other characters.
func hasLWSRun(v string) bool {
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\t', '\r', '\n':
			return true
		case ' ':
			if i == 0 || i == len(v)-1 || v[i+1] == ' ' {
				return true
			}
		}
	}

	return false
}

// search reports whether the regular expression matches part of s, as regexec does with an extended regular
// expression (TS 29.228 Annex F): RE2 accepts the ERE syntax. An invalid expression matches nothing.
func search(expr, s string) bool {
	re := compiled(expr)

	return re != nil && re.MatchString(s)
}

// headerName is the expression of the header name of a SIPHeader SPT, which matches regardless of case (RFC 3261
// §7.3.1): kept by the expression, rather than built for each header field it is checked against.
func headerName(expr string) *regexp.Regexp {
	if re, ok := headerExpressions.Load(expr); ok {
		return re.(*regexp.Regexp)
	}

	re := compiled("(?i)" + expr)

	headerExpressions.Store(expr, re)

	return re
}

var expressions, headerExpressions sync.Map

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

// mayRegister reports whether a re-registration of the user may meet the trigger point, as thirdParties checks it: a
// REGISTER of type RE_REGISTRATION, in the session case ORIGINATING_REGISTERED. The other SPTs depend on the request,
// and may be met or not: the trigger point is worked out in three-valued logic, where a disjunction is the greatest of
// its values and a conjunction the least (TS 29.228 Annex B.2.2, Annex C).
func mayRegister(tp *cx.TriggerPoint) bool {
	const (
		no int8 = iota
		maybe
		yes
	)

	if tp == nil {
		return true
	}

	value := func(s cx.ServicePointTrigger) int8 {
		var met bool

		switch {
		case s.Method != nil:
			met = *s.Method == "REGISTER" &&
				(len(s.RegistrationTypes) == 0 || slices.Contains(s.RegistrationTypes, cx.RegistrationTypeReRegistration))
		case s.SessionCase != nil:
			met = *s.SessionCase == cx.SessionCaseOriginatingRegistered
		default:
			return maybe
		}

		if met != s.Negated {
			return yes
		}

		return no
	}

	var groups []int32

	for _, s := range tp.ServicePointTriggers {
		for _, g := range s.Groups {
			if !slices.Contains(groups, g) {
				groups = append(groups, g)
			}
		}
	}

	// In CNF, a conjunction of groups that are disjunctions; in DNF, the other way round.
	cnf := tp.ConjunctiveNormalForm

	outer := no
	if cnf {
		outer = yes
	}

	for _, g := range groups {
		inner := yes
		if cnf {
			inner = no
		}

		for _, s := range tp.ServicePointTriggers {
			if !slices.Contains(s.Groups, g) {
				continue
			}

			if cnf {
				inner = max(inner, value(s))
			} else {
				inner = min(inner, value(s))
			}
		}

		if cnf {
			outer = min(outer, inner)
		} else {
			outer = max(outer, inner)
		}
	}

	return outer != no
}
