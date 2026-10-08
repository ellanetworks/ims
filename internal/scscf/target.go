package scscf

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

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

func preferences(h sip.Header, name string) []preference {
	var out []preference

	for _, v := range h.Elements(name) {
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

// Recipients returns the registrations a request to the public identity u reaches: every registration holding it,
// under any of its aliases, whichever private identity registered it (TS 24.229 §5.4.3.3 step 8, TS 23.228
// §4.3.3.4). barred reports that one of them bars it, and the request then reaches none.
func Recipients(ctx context.Context, store *db.DB, u sip.URI, homeDomain string) (regs []db.Registration, barred bool, err error) {
	keys := identityKeys(u, homeDomain)

	for _, key := range keys {
		found, err := store.ListRegistrationsByIdentity(ctx, key)
		if err != nil {
			return nil, false, err
		}

		for _, reg := range found {
			if !slices.ContainsFunc(regs, func(r db.Registration) bool { return r.ID == reg.ID }) {
				regs = append(regs, reg)
			}
		}
	}

	for _, reg := range regs {
		if slices.ContainsFunc(reg.Identities, func(id db.PublicIdentity) bool { return id.Barred && slices.Contains(keys, id.Key) }) {
			return nil, true, nil
		}
	}

	return regs, false, nil
}

// rejectedBy reports whether a Reject-Contact predicate discards a contact: one whose feature tags
// all appear in the contact's feature set, and all match (RFC 3841 §7.2.4).
func rejectedBy(features map[string][]string, rejects []preference) bool {
	if len(features) == 0 {
		return false
	}

	for _, r := range rejects {
		matched := true

		for _, t := range r.terms {
			have, ok := features[t.tag]
			if !ok || !t.matches(have) {
				matched = false
				break
			}
		}

		if matched {
			return true
		}
	}

	return false
}

// target is a registered contact a request can go to.
type target struct {
	binding db.Binding
	reg     db.Registration
	q, qa   float64

	// instance is the UA instance of the contact (RFC 5626 §4.1), and flow its reg-id when the
	// registration used outbound (RFC 5626 §6).
	instance, flow string

	// others are the instance's other flows, tried in turn on a 430 (RFC 5626 §7).
	others []target
}

type disposition int

const (
	forkParallel disposition = iota
	forkSequential
	noFork
)

// RFC 3841 §9.1
func requestDisposition(h sip.Header) disposition {
	d := forkParallel

	for _, v := range h.Elements("Request-Disposition") {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "no-fork":
			return noFork
		case "sequential":
			d = forkSequential
		}
	}

	return d
}

// targetSet orders the live contacts of the called user into groups tried one after another, the
// contacts of a group in parallel (TS 24.229 §5.4.3.3 steps 8 to 10, RFC 3841 §7.2, RFC 3261 §16.6).
// Contacts with the highest q-value come first; within a q-value, those that match the caller's
// preferences best (RFC 3841 §7.2.4), so that the request forks across the contacts with matching
// callee capabilities (TS 23.228 §4.2.7.2) and reaches the others only if those fail. A group
// lists its most recently registered contacts first, and a UA instance gets one contact at a time
// (RFC 5626 §7).
func targetSet(regs []db.Registration, now time.Time, h sip.Header) [][]target {
	prefs, rejects := preferences(h, "Accept-Contact"), preferences(h, "Reject-Contact")

	var all []target

	for _, reg := range regs {
		for _, b := range liveAt(reg.Bindings, now) {
			params, _ := sip.ParseParams(b.Contact.Params)
			features := contactFeatures(params)

			if rejectedBy(features, rejects) {
				continue
			}

			qa, ok := callerPreference(features, prefs)
			if !ok {
				continue
			}

			t := target{binding: b, reg: reg, q: QValue(params), qa: qa}

			if v, ok := params.Get("+sip.instance"); ok {
				t.instance = sip.Unquote(v)

				if id, ok := params.Get("reg-id"); ok && outbound(b.Contact.Path) {
					t.flow = id
				}
			}

			all = append(all, t)
		}
	}

	slices.SortStableFunc(all, func(a, b target) int {
		return cmp.Or(cmp.Compare(b.q, a.q), cmp.Compare(b.qa, a.qa), b.binding.RegisteredAt.Compare(a.binding.RegisteredAt))
	})

	var set []target

	for _, t := range all {
		i := slices.IndexFunc(set, func(o target) bool { return t.instance != "" && o.instance == t.instance })

		switch {
		case i < 0:
			set = append(set, t)
		case t.flow != "" && t.flow != set[i].flow && !slices.ContainsFunc(set[i].others, func(o target) bool { return o.flow == t.flow }):
			set[i].others = append(set[i].others, t)
		}
	}

	if len(set) == 0 {
		return nil
	}

	switch requestDisposition(h) {
	case noFork:
		return [][]target{set[:1]}
	case forkSequential:
		groups := make([][]target, len(set))
		for i, t := range set {
			groups[i] = []target{t}
		}

		return groups
	}

	var groups [][]target

	for i, t := range set {
		if i == 0 || t.q != set[i-1].q || t.qa != set[i-1].qa {
			groups = append(groups, nil)
		}

		groups[len(groups)-1] = append(groups[len(groups)-1], t)
	}

	return groups
}

// outbound reports whether a registration over path used outbound: its first hop added "ob" to
// its Path (RFC 5626 §6).
func outbound(path string) bool {
	hops, err := sip.ParseAddressList(path)

	return err == nil && len(hops) > 0 && hops[0].URI.Params.Has("ob")
}

// QValue is the callee preference of a registered contact: its q-value, or 1.0 when it has none
// (RFC 3841 §7.2.3), and 0 when it is not a number.
func QValue(params sip.Params) float64 {
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
