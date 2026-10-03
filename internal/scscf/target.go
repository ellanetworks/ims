package scscf

import (
	"cmp"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

// baseTags are the feature tags of RFC 3840 §10 that a Contact carries
// without a leading "+" (RFC 3841 §7.2.3).
var baseTags = []string{
	"audio", "automata", "class", "duplex", "data", "control", "mobility", "description", "events", "priority",
	"methods", "schemes", "application", "video", "actor", "language", "isfocus", "type", "extensions", "text",
}

func isFeatureTag(name string) bool {
	return strings.HasPrefix(name, "+") || slices.Contains(baseTags, name)
}

// term is one feature tag of an Accept-Contact predicate, with the values it
// accepts, or refuses when negated (RFC 3840 §9).
type term struct {
	tag     string
	values  []string
	negated bool
}

// preference is an Accept-Contact predicate (RFC 3841 §7.2.4).
type preference struct {
	terms    []term
	require  bool
	explicit bool
}

func preferences(h sip.Header) []preference {
	var out []preference

	for _, v := range h.Elements("Accept-Contact") {
		_, params, err := sip.ParseTokenParams(v)
		if err != nil {
			continue
		}

		var p preference

		for _, param := range params {
			name := strings.ToLower(param.Name)

			switch {
			case name == "require":
				p.require = true
			case name == "explicit":
				p.explicit = true
			case isFeatureTag(name):
				if t, ok := parseTerm(name, param.Value); ok {
					p.terms = append(p.terms, t)
				}
			}
		}

		if len(p.terms) > 0 {
			out = append(out, p)
		}
	}

	return out
}

// parseTerm reads a feature parameter value: a boolean without value, or a
// quoted list of tokens and strings, any of them negated with "!". Numeric
// ranges ("#") are not supported and the term is left out.
func parseTerm(tag, value string) (term, bool) {
	t := term{tag: tag}

	for _, v := range featureValues(value) {
		negated := strings.HasPrefix(v, "!")
		v = strings.TrimPrefix(v, "!")

		if strings.HasPrefix(v, "#") || len(t.values) > 0 && negated != t.negated {
			return term{}, false
		}

		t.negated = negated
		t.values = append(t.values, v)
	}

	return t, true
}

// featureValues are the values of a feature parameter (RFC 3840 §9): TRUE
// for a parameter without value, else the comma-separated list in its quoted
// string. Tokens compare without case and strings in angle brackets with it.
func featureValues(v string) []string {
	if v == "" {
		return []string{"true"}
	}

	var out []string

	for s := range strings.SplitSeq(sip.Unquote(v), ",") {
		s = strings.TrimSpace(s)
		if u, err := url.PathUnescape(s); err == nil {
			s = u
		}

		if !strings.HasPrefix(strings.TrimPrefix(s, "!"), "<") {
			s = strings.ToLower(s)
		}

		if s != "" {
			out = append(out, s)
		}
	}

	return out
}

// contactFeatures are the feature parameters of a registered contact.
func contactFeatures(params sip.Params) map[string][]string {
	features := make(map[string][]string)

	for _, p := range params {
		if name := strings.ToLower(p.Name); isFeatureTag(name) {
			features[name] = featureValues(p.Value)
		}
	}

	return features
}

// matches reports whether a term accepts a contact's values for its tag.
func (t term) matches(have []string) bool {
	return slices.ContainsFunc(t.values, func(v string) bool { return slices.Contains(have, v) }) != t.negated
}

// callerPreference applies the Accept-Contact predicates to a contact (RFC
// 3841 §7.2.4): it reports false for a contact a required predicate drops,
// else the contact's Qa, the average of its scores. A contact without feature
// parameters is immune to caller preferences, with a Qa of 1.
func callerPreference(features map[string][]string, prefs []preference) (float64, bool) {
	if len(features) == 0 {
		return 1, true
	}

	var (
		total float64
		m     int
	)

	for _, p := range prefs {
		matched := true
		score := 0.0

		for _, t := range p.terms {
			have, ok := features[t.tag]
			if !ok {
				continue
			}

			score += 1 / float64(len(p.terms))
			matched = matched && t.matches(have)
		}

		if !matched {
			if p.require {
				return 0, false
			}

			continue
		}

		if score < 1 && p.explicit {
			if p.require {
				return 0, false
			}

			score = 0
		}

		total += score
		m++
	}

	if m == 0 {
		return 0, true
	}

	return total / float64(m), true
}

// selectBinding picks the single target of a terminating request (Decision
// 3): among the bindings the caller preferences keep, the highest q-value
// set by the callee, then the highest caller preference Qa (RFC 3841 §7.2.4:
// caller preferences only order contacts of equal q-value), then the newest.
func selectBinding(bindings []db.Binding, prefs []preference) (db.Binding, bool) {
	type candidate struct {
		b  db.Binding
		q  float64
		qa float64
	}

	var cs []candidate

	for _, b := range bindings {
		params, _ := sip.ParseParams(b.Contact.Params)

		if qa, ok := callerPreference(contactFeatures(params), prefs); ok {
			cs = append(cs, candidate{b: b, q: qValue(params), qa: qa})
		}
	}

	if len(cs) == 0 {
		return db.Binding{}, false
	}

	best := slices.MaxFunc(cs, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(a.q, b.q), cmp.Compare(a.qa, b.qa), a.b.RegisteredAt.Compare(b.b.RegisteredAt))
	})

	return best.b, true
}

func qValue(params sip.Params) float64 {
	v, ok := params.Get("q")
	if !ok {
		return 1
	}

	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}

	return f
}
