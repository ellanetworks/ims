package scscf

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

// TS 24.229 clause 5.4.3.3
// TS 23.228 clause 4.3.3.4
func Recipients(ctx context.Context, store *db.DB, u sip.URI, homeDomain string) (regs []db.Registration, barred bool, err error) {
	reach, err := RecipientsOf(ctx, store, []sip.URI{u}, homeDomain)
	if err != nil {
		return nil, false, err
	}

	return reach[0].Registrations, reach[0].Barred, nil
}

type Reach struct {
	Registrations []db.Registration
	Barred        bool
}

func RecipientsOf(ctx context.Context, store *db.DB, uris []sip.URI, homeDomain string) ([]Reach, error) {
	keys := make([][]string, len(uris))

	var all []string

	for i, u := range uris {
		keys[i] = identityKeys(u, homeDomain)

		for _, k := range keys[i] {
			if !slices.Contains(all, k) {
				all = append(all, k)
			}
		}
	}

	regs, err := store.ListRegistrationsByIdentities(ctx, all)
	if err != nil {
		return nil, err
	}

	out := make([]Reach, len(uris))

	for i := range uris {
		out[i] = reach(regs, keys[i])
	}

	return out, nil
}

func reach(regs []db.Registration, keys []string) Reach {
	var r Reach

	for _, reg := range regs {
		for _, id := range reg.Identities {
			if !slices.Contains(keys, id.Key) {
				continue
			}

			if id.Barred {
				return Reach{Barred: true}
			}

			if !slices.ContainsFunc(r.Registrations, func(o db.Registration) bool { return o.ID == reg.ID }) {
				r.Registrations = append(r.Registrations, reg)
			}
		}
	}

	return r
}

type target struct {
	binding db.Binding
	reg     db.Registration
	q, qa   float64

	instance string
	flow     int64

	others []target
}

type disposition int

const (
	// TS 24.229 clause 5.4.3.3
	byPriority disposition = iota
	forkParallel
	forkSequential
	noFork
)

// RFC 3841 section 9.1
func requestDisposition(h sip.Header) disposition {
	d := byPriority

	for _, v := range h.Elements("Request-Disposition") {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "no-fork":
			return noFork
		case "sequential":
			d = forkSequential
		case "parallel":
			d = forkParallel
		}
	}

	return d
}

// TS 24.229 clause 5.4.3.3
// RFC 3841 section 7.2
// RFC 3841 section 7.2.4
// RFC 3841 section 9.1
// RFC 3261 section 16.6
// RFC 5626 section 7
func targetSet(regs []db.Registration, now time.Time, h sip.Header) (groups [][]target, step bool) {
	prefs, _ := h.AcceptContact()
	rejects, _ := h.RejectContact()

	var all []target

	for _, reg := range regs {
		for _, b := range liveAt(reg.Bindings, now) {
			params, _ := sip.ParseParams(b.Contact.Params)

			features, _ := sip.ParseFeatureSet(params)

			if features.RejectedBy(rejects) {
				continue
			}

			qa, ok := features.Preference(prefs)
			if !ok {
				continue
			}

			t := target{binding: b, reg: reg, q: QValue(params), qa: qa}

			t.instance, t.flow = b.Contact.Instance, b.Contact.RegID

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
		case set[i].flow != 0 && t.flow != 0 && t.flow != set[i].flow &&
			!slices.ContainsFunc(set[i].others, func(o target) bool { return o.flow == t.flow }):
			set[i].others = append(set[i].others, t)
		}
	}

	if len(set) == 0 {
		return nil, false
	}

	switch requestDisposition(h) {
	case noFork:
		return [][]target{set[:1]}, false
	case forkParallel:
		return [][]target{set}, false
	case forkSequential:
		for _, t := range set {
			groups = append(groups, []target{t})
		}

		return groups, false
	}

	for i, t := range set {
		if i == 0 || t.q != set[i-1].q {
			groups = append(groups, nil)
		}

		groups[len(groups)-1] = append(groups[len(groups)-1], t)
	}

	return groups, true
}

// RFC 3841 section 7.2.3
// RFC 3261 section 25.1
func QValue(params sip.Params) float64 {
	v, ok := params.Get("q")
	if !ok {
		return 1
	}

	q, err := sip.ParseQValue(v)
	if err != nil {
		return 1
	}

	return q
}
