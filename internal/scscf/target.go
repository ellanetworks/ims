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

// Recipients returns the registrations a request to the public identity u reaches: every registration holding it,
// under any of its aliases, whichever private identity registered it (TS 24.229 §5.4.3.3 step 8, TS 23.228
// §4.3.3.4). barred reports that one of them bars it, and the request then reaches none.
func Recipients(ctx context.Context, store *db.DB, u sip.URI, homeDomain string) (regs []db.Registration, barred bool, err error) {
	reach, err := RecipientsOf(ctx, store, []sip.URI{u}, homeDomain)
	if err != nil {
		return nil, false, err
	}

	return reach[0].Registrations, reach[0].Barred, nil
}

// Reach is what Recipients returns for one public identity.
type Reach struct {
	Registrations []db.Registration
	Barred        bool
}

// RecipientsOf is Recipients for each of several public identities, with one read of the database.
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

// reach picks, out of regs, those that hold one of the keys of a public identity.
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

// target is a registered contact a request can go to.
type target struct {
	binding db.Binding
	reg     db.Registration
	q, qa   float64

	// instance is the UA instance of the contact (RFC 5626 §4.1), and flow its reg-id when the
	// registration used outbound (RFC 5626 §6), or else 0.
	instance string
	flow     int64

	// others are the instance's other flows, tried in turn on a 430 (RFC 5626 §7).
	others []target
}

type disposition int

const (
	// byPriority is the S-CSCF's own choice without directives (TS 24.229 §5.4.3.3 step 10a): the contacts of a
	// q-value in parallel, and q-values one after another.
	byPriority disposition = iota
	forkParallel
	forkSequential
	noFork
)

// RFC 3841 §9.1
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

// targetSet orders the live contacts of the called user into groups tried one after another, the contacts of a
// group in parallel (TS 24.229 §5.4.3.3 steps 8 to 10, RFC 3841 §7.2, RFC 3261 §16.6). Without directives, a group
// holds the contacts of one q-value, the highest first; within it, the contacts that match the caller's
// preferences best come first (RFC 3841 §7.2.4: Qa orders an equivalence class, it does not split it), then the
// most recently registered. A UA instance gets one contact at a time (RFC 5626 §7).
//
// step reports whether the groups are the S-CSCF's own, which a group may leave unanswered for a while before the
// next; with the "sequential" directive, the next contact gets the request only on a final response
// (RFC 3841 §9.1).
func targetSet(regs []db.Registration, now time.Time, h sip.Header) (groups [][]target, step bool) {
	// Malformed predicates are left out; the others apply.
	prefs, _ := h.AcceptContact()
	rejects, _ := h.RejectContact()

	var all []target

	for _, reg := range regs {
		for _, b := range liveAt(reg.Bindings, now) {
			params, _ := sip.ParseParams(b.Contact.Params)

			// Malformed feature parameters are left out (RFC 3841 §7.2.3).
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

		// Only an outbound flow has other flows to replace it on a 430 (RFC 5626 §5.3.1, §7).
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

// QValue is the callee preference of a registered contact: its q-value, or 1.0 when it has none
// (RFC 3841 §7.2.3). The registrar refuses a q-value outside the RFC 3261 §25.1 grammar; one stored before it
// did counts as none.
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
