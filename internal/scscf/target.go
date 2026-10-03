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

var baseTags = []string{
	"audio", "automata", "class", "duplex", "data", "control", "mobility", "description", "events", "priority",
	"methods", "schemes", "application", "video", "actor", "language", "isfocus", "type", "extensions", "text",
}

func isFeatureTag(name string) bool {
	return strings.HasPrefix(name, "+") || slices.Contains(baseTags, name)
}

type term struct {
	tag     string
	values  []string
	negated bool
}

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

// RFC 3840
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

func contactFeatures(params sip.Params) map[string][]string {
	features := make(map[string][]string)

	for _, p := range params {
		if name := strings.ToLower(p.Name); isFeatureTag(name) {
			features[name] = featureValues(p.Value)
		}
	}

	return features
}

func (t term) matches(have []string) bool {
	return slices.ContainsFunc(t.values, func(v string) bool { return slices.Contains(have, v) }) != t.negated
}

// RFC 3841 §7.2.4
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

// RFC 3841 §7.2.4
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
