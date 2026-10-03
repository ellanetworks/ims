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

const icsiRef = "+g.3gpp.icsi-ref"

// preference is an Accept-Contact feature preference on the ICSI (RFC 3841
// §7.2), the only feature the S-CSCF matches on.
type preference struct {
	icsis    []string
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

		value, ok := params.Get(icsiRef)
		if !ok {
			continue
		}

		out = append(out, preference{icsis: featureValues(value), require: params.Has("require"), explicit: params.Has("explicit")})
	}

	return out
}

func featureValues(v string) []string {
	var out []string

	for s := range strings.SplitSeq(sip.Unquote(v), ",") {
		s = strings.TrimSpace(s)
		if u, err := url.PathUnescape(s); err == nil {
			s = u
		}

		if s != "" {
			out = append(out, strings.ToLower(s))
		}
	}

	return out
}

// selectBinding picks the single target of a terminating request (Decision
// 3): the bindings that pass the required Accept-Contact preferences, ranked
// by how many preferences they match, then by q, then the newest.
func selectBinding(bindings []db.Binding, prefs []preference) (db.Binding, bool) {
	type candidate struct {
		b     db.Binding
		score int
		q     float64
	}

	var cs []candidate

	for _, b := range bindings {
		params, _ := sip.ParseParams(b.Contact.Params)

		var have []string
		if v, ok := params.Get(icsiRef); ok {
			have = featureValues(v)
		}

		c := candidate{b: b, q: qValue(params)}
		ok := true

		for _, p := range prefs {
			switch {
			case slices.ContainsFunc(p.icsis, func(s string) bool { return slices.Contains(have, s) }):
				c.score++
			case p.require && (have != nil || p.explicit):
				ok = false
			}
		}

		if ok {
			cs = append(cs, c)
		}
	}

	if len(cs) == 0 {
		return db.Binding{}, false
	}

	best := slices.MaxFunc(cs, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(a.score, b.score), cmp.Compare(a.q, b.q), a.b.RegisteredAt.Compare(b.b.RegisteredAt))
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
