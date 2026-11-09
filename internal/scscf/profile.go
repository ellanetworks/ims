package scscf

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/reginfo"
)

// TS 29.228 clause 6.5.2.1
// TS 24.229 clause 5.4.1.8
type profileChange struct {
	set    db.ImplicitSet
	before []db.PublicIdentity

	removed []db.PublicIdentity
	barred  []db.PublicIdentity
	added   []db.PublicIdentity

	newAS bool
}

func diffProfile(before []db.PublicIdentity, beforeData []byte, after db.ImplicitSet, sub cx.IMSSubscription) profileChange {
	pc := profileChange{set: after, before: before}

	unbarred := func(set []db.PublicIdentity, key string) bool {
		return slices.ContainsFunc(set, func(id db.PublicIdentity) bool { return id.Key == key && !id.Barred })
	}

	for _, id := range before {
		switch {
		case id.Barred:
		case !holds(after.Identities, id.Key):
			pc.removed = append(pc.removed, id)
		case !unbarred(after.Identities, id.Key):
			pc.barred = append(pc.barred, id)
		}
	}

	for _, id := range after.Identities {
		if !id.Barred && !unbarred(before, id.Key) {
			pc.added = append(pc.added, id)
		}
	}

	stored := map[registerAS]bool{}

	if old, err := cx.ParseUserData(beforeData); err == nil {
		for _, as := range registerASs(old) {
			stored[as] = true
		}
	}

	pc.newAS = slices.ContainsFunc(registerASs(sub), func(as registerAS) bool { return !stored[as] })

	return pc
}

// TS 29.228 clause 6.5.2.1
func (pc profileChange) current(ids []db.PublicIdentity) profileChange {
	unbarred := func(key string) bool {
		return slices.ContainsFunc(ids, func(id db.PublicIdentity) bool { return id.Key == key && !id.Barred })
	}

	out := pc
	out.set.Identities, out.removed, out.barred, out.added = ids, nil, nil, nil

	for _, id := range pc.dropped() {
		switch {
		case unbarred(id.Key):
		case holds(pc.removed, id.Key) && !holds(ids, id.Key):
			out.removed = append(out.removed, id)
		default:
			out.barred = append(out.barred, id)
		}
	}

	for _, id := range pc.added {
		if unbarred(id.Key) {
			out.added = append(out.added, id)
		}
	}

	return out
}

func (pc profileChange) dropped() []db.PublicIdentity {
	return append(slices.Clone(pc.removed), pc.barred...)
}

func (pc profileChange) identitiesChanged() bool {
	return len(pc.removed) > 0 || len(pc.barred) > 0 || len(pc.added) > 0
}

// TS 24.229 clause 5.4.1.7
type registerAS struct {
	to, server string
}

// TS 24.229 clause 5.4.1.7
func registerASs(sub cx.IMSSubscription) []registerAS {
	var out []registerAS

	for _, p := range sub.ServiceProfiles {
		i := slices.IndexFunc(p.PublicIdentities, func(id cx.ProfileIdentity) bool { return !id.Barred && !wildcarded(id) })
		if i < 0 {
			continue
		}

		for _, c := range filterCriteria(p, true) {
			if _, bad := invalidExpression(c.Trigger); bad || !mayRegister(c.Trigger) {
				continue
			}

			as := registerAS{to: identityKeyOf(p.PublicIdentities[i].Identity), server: strings.ToLower(c.ApplicationServer.ServerName)}

			if !slices.Contains(out, as) {
				out = append(out, as)
			}
		}
	}

	return out
}

// TS 29.228 clause 6.5.2.1
func (r *Registrar) spreadProfile(ctx context.Context, pc profileChange, except string, shorten bool) {
	if dropped := pc.dropped(); len(dropped) > 0 {
		r.forgetASs(identityKeysOf(dropped))
	}

	if !pc.identitiesChanged() && !shorten {
		return
	}

	regs, err := r.cfg.DB.ListRegistrationsBySet(ctx, pc.set.ID)
	if err != nil {
		r.log.WarnContext(ctx, "failed to list the registrations of a set", slog.Any("error", err))
		return
	}

	var impis []string

	for _, reg := range regs {
		if reg.IMPI != except && !slices.Contains(impis, reg.IMPI) {
			impis = append(impis, reg.IMPI)
		}
	}

	for _, impi := range impis {
		r.background(ctx, impi, func(ctx context.Context) {
			r.send(ctx, r.profileEffects(ctx, impi, pc, shorten))
		})
	}
}

// TS 24.229 clause 5.4.1.8
func (r *Registrar) profileEffects(ctx context.Context, impi string, pc profileChange, shorten bool) []*outgoing {
	st, err := r.load(ctx, impi)
	if err != nil {
		r.log.WarnContext(ctx, "failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	i := slices.IndexFunc(st.regs, func(reg db.Registration) bool { return reg.SetID == pc.set.ID })
	if i < 0 || !st.registered(&st.regs[i]) {
		return nil
	}

	pc = pc.current(st.regs[i].Identities)

	if reg, ok := withIdentities(st.regs[i], pc.set.Identities); ok {
		saved, err := r.cfg.DB.SaveRegistration(ctx, reg)
		if err != nil {
			r.log.WarnContext(ctx, "failed to store the registration", slog.String("impi", impi), slog.Any("error", err))
			return nil
		}

		st.regs[i] = saved
	}

	live := st.live(st.regs[i].Bindings)

	r.releaseRemoved(ctx, st, pc, live)

	ch := r.profileNotice(ctx, impi, pc)

	if shorten {
		if err := r.shorten(ctx, st); err != nil {
			r.log.WarnContext(ctx, "failed to shorten the registration", slog.String("impi", impi), slog.Any("error", err))
			return nil
		}

		ch.shortened = impi
	}

	if !pc.identitiesChanged() && !shorten {
		return nil
	}

	return r.notifySubscriptions(ctx, impi, ch)
}

func withIdentities(reg db.Registration, ids []db.PublicIdentity) (db.Registration, bool) {
	def := defaultIdentity(ids)
	if def.Key == "" {
		return reg, false
	}

	changed := false

	if !holds(ids, identityKeyOf(reg.IMPU)) {
		reg.IMPU, changed = def.URI, true
	}

	reg.Bindings = slices.Clone(reg.Bindings)

	for j, b := range reg.Bindings {
		if b.IMPU != "" && !holds(ids, b.IMPU) {
			reg.Bindings[j].IMPU, changed = def.Key, true
		}
	}

	return reg, changed
}

// TS 24.229 clause 5.4.1.8
// TS 24.229 clause 5.4.5.1.2
func (r *Registrar) releaseRemoved(ctx context.Context, st *state, pc profileChange, live []db.Binding) {
	if len(pc.removed) == 0 {
		return
	}

	pick := func(served []string) bool {
		i := slices.IndexFunc(served, func(key string) bool { return holds(pc.before, key) })
		return i >= 0 && holds(pc.removed, served[i])
	}

	calls := make(map[bindingKey]map[*proxy.Dialog]proxy.Side, len(live))
	for _, b := range live {
		calls[bindingKey(b.ID)] = r.calls.serving(bindingKey(b.ID), pick)
	}

	r.releaseCalls(ctx, st, []removal{{
		reg: db.Registration{IMPI: st.impi}, bindings: live, event: reginfo.Rejected, calls: calls,
	}})
}

// TS 24.229 clause 5.4.1.8
// TS 24.229 clause 5.4.2.1.2
func (r *Registrar) profileNotice(ctx context.Context, impi string, pc profileChange) change {
	ch := change{created: map[string]bool{}}

	for _, id := range pc.added {
		ch.created[id.Key] = true
	}

	dropped := pc.dropped()
	if len(dropped) == 0 {
		return ch
	}

	var bindings []db.Binding

	if regs, err := r.cfg.DB.ListRegistrationsBySet(ctx, pc.set.ID); err == nil {
		now := r.clock.Now()

		for _, reg := range regs {
			bindings = append(bindings, liveAt(reg.Bindings, now)...)
		}
	}

	ch.rejected = rejection{impi: impi, identities: dropped, bindings: bindings}
	ch.reason = reasonRejected

	ch.ended = func(resource string) bool {
		u, err := sip.ParseURI(resource)
		if err != nil {
			return false
		}

		keys := identityKeys(u, r.cfg.Home)
		i := slices.IndexFunc(keys, func(key string) bool { return holds(pc.before, key) })

		return i >= 0 && holds(dropped, keys[i])
	}

	return ch
}

// TS 29.228 clause 6.5.1.1
func defaultIdentity(set []db.PublicIdentity) db.PublicIdentity {
	for _, id := range set {
		if !id.Barred {
			return id
		}
	}

	return db.PublicIdentity{}
}

// TS 29.228 clause 6.5.2.1
// TS 24.229 clause 5.4.1.8
type move struct {
	pc    profileChange
	taken []string

	deregistered bool
}

func (r *Registrar) moves(ctx context.Context, set db.ImplicitSet, stale []db.Registration) ([]move, error) {
	sets, err := r.cfg.DB.FindSets(ctx, identityKeysOf(set.Identities))
	if err != nil {
		return nil, err
	}

	var out []move

	for _, s := range sets {
		if s.ID == set.ID {
			continue
		}

		regs, err := r.cfg.DB.ListRegistrationsBySet(ctx, s.ID)
		if err != nil {
			return nil, err
		}

		if !slices.ContainsFunc(regs, func(reg db.Registration) bool {
			return !slices.ContainsFunc(stale, func(old db.Registration) bool { return old.ID == reg.ID })
		}) {
			continue
		}

		var taken []string

		for _, id := range s.Identities {
			if holds(set.Identities, id.Key) {
				taken = append(taken, id.Key)
			}
		}

		sub, err := cx.ParseUserData(s.UserData)
		if err != nil {
			return nil, err
		}

		sub = withoutIdentities(sub, taken)

		data, err := cx.MarshalUserData(sub)
		if err != nil {
			return nil, err
		}

		next := s
		next.Identities, next.UserData = implicitSet(sub), data

		out = append(out, move{
			pc:           diffProfile(s.Identities, s.UserData, next, sub),
			taken:        taken,
			deregistered: !hasUnbarred(next.Identities) || defaultIdentity(s.Identities).Key != defaultIdentity(next.Identities).Key,
		})
	}

	return out, nil
}

func withoutIdentities(sub cx.IMSSubscription, keys []string) cx.IMSSubscription {
	profiles := make([]cx.ServiceProfile, 0, len(sub.ServiceProfiles))

	for _, p := range sub.ServiceProfiles {
		p.PublicIdentities = slices.DeleteFunc(slices.Clone(p.PublicIdentities), func(id cx.ProfileIdentity) bool {
			return (id.Type == cx.IdentityDistinctPublicUserIdentity || id.Type == cx.IdentityNonDistinctIMPU) &&
				slices.Contains(keys, identityKeyOf(id.Identity))
		})

		if len(p.PublicIdentities) > 0 {
			profiles = append(profiles, p)
		}
	}

	sub.ServiceProfiles = profiles

	return sub
}

func (r *Registrar) applyMoves(ctx context.Context, impi string, moves []move) {
	for _, m := range moves {
		r.log.WarnContext(ctx, "the HSS's profile moved identities from another registered set", slog.String("impi", impi),
			slog.Any("identities", m.taken), slog.Bool("deregistered", m.deregistered))

		if !m.deregistered {
			r.spreadProfile(ctx, m.pc, "", false)
			continue
		}

		regs, err := r.cfg.DB.ListRegistrationsBySet(ctx, m.pc.set.ID)
		if err != nil {
			r.log.WarnContext(ctx, "failed to list the registrations of a set", slog.Any("error", err))
			continue
		}

		var impis []string

		for _, reg := range regs {
			if !slices.Contains(impis, reg.IMPI) {
				impis = append(impis, reg.IMPI)
			}
		}

		for _, other := range impis {
			r.background(ctx, other, func(ctx context.Context) {
				r.send(ctx, r.terminateMoved(ctx, other, m.pc))
			})
		}
	}
}

// TS 24.229 clause 5.4.1.5
func (r *Registrar) terminateMoved(ctx context.Context, impi string, pc profileChange) []*outgoing {
	st, err := r.load(ctx, impi)
	if err != nil {
		r.log.WarnContext(ctx, "failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	i := slices.IndexFunc(st.regs, func(reg db.Registration) bool { return reg.SetID == pc.set.ID })
	if i < 0 {
		return nil
	}

	reg := st.regs[i]
	reg.Identities = pc.before

	rm, _, err := r.removeBindings(ctx, st, reg, st.live(reg.Bindings), reginfo.Rejected)
	if err != nil {
		r.log.WarnContext(ctx, "failed to delete the registration", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	r.log.InfoContext(ctx, "registration terminated: the HSS moved its default identity", slog.String("impi", impi),
		slog.String("impu", reg.IMPU))

	r.releaseCalls(ctx, st, []removal{rm})
	r.deregistered(ctx, rm)

	return r.notifyChange(ctx, impi, change{removed: []removal{rm}, reason: reasonRejected})
}
