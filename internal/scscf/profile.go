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

// profileChange is how a user profile the HSS sent, pushed or downloaded, changed an implicit set (TS 29.228 §6.5.2.1,
// TS 24.229 §5.4.1.8).
type profileChange struct {
	// set is the set as stored, and before its identities before.
	set    db.ImplicitSet
	before []db.PublicIdentity

	// removed are the non-barred identities that left the set, barred those the set bars now, and added the
	// identities it has non-barred now that it had not, or barred (TS 24.229 §5.4.1.8 steps 1 to 3).
	removed []db.PublicIdentity
	barred  []db.PublicIdentity
	added   []db.PublicIdentity

	// newAS tells that a re-registration of the set now triggers an AS, or an AS for a service profile, that it did
	// not (TS 24.229 §5.4.1.7).
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

// dropped are the identities that leave the registered non-barred ones: removed or barred.
func (pc profileChange) dropped() []db.PublicIdentity {
	return append(slices.Clone(pc.removed), pc.barred...)
}

func (pc profileChange) identitiesChanged() bool {
	return len(pc.removed) > 0 || len(pc.barred) > 0 || len(pc.added) > 0
}

// registerAS is a third-party registration a re-registration makes: of the identity a service profile registers at the
// AS (TS 24.229 §5.4.1.7).
type registerAS struct {
	to, server string
}

// registerASs are the third-party registrations that a re-registration may make, of the service profiles with a
// non-barred identity, as thirdParties evaluates their iFCs (TS 24.229 §5.4.1.7).
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

// spreadProfile has the private identities that registered the set, but for one, learn of its change, each holding
// its lock; and forgets what the S-CSCF kept for the ASs of the identities that left it (TS 29.228 §6.5.2.1).
func (r *Registrar) spreadProfile(ctx context.Context, pc profileChange, except string, shorten bool) {
	if dropped := pc.dropped(); len(dropped) > 0 {
		r.forgetASs(identityKeysOf(dropped))
	}

	if !pc.identitiesChanged() && !shorten {
		return
	}

	regs, err := r.cfg.DB.ListRegistrationsBySet(ctx, pc.set.ID)
	if err != nil {
		r.log.Warn("failed to list the registrations of a set", slog.Any("error", err))
		return
	}

	var impis []string

	for _, reg := range regs {
		if reg.IMPI != except && !slices.Contains(impis, reg.IMPI) {
			impis = append(impis, reg.IMPI)
		}
	}

	for _, impi := range impis {
		r.background(impi, func(ctx context.Context) {
			r.send(r.profileEffects(ctx, impi, pc, shorten))
		})
	}
}

// profileEffects applies the change of the profile of a set to the registration of the set by a private identity,
// whose lock the caller holds (TS 24.229 §5.4.1.8): it releases the calls of the identities removed, shortens the
// registration if asked, and tells the reg event subscribers.
func (r *Registrar) profileEffects(ctx context.Context, impi string, pc profileChange, shorten bool) []*outgoing {
	st, err := r.load(ctx, impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	i := slices.IndexFunc(st.regs, func(reg db.Registration) bool { return reg.SetID == pc.set.ID })
	if i < 0 || !st.registered(&st.regs[i]) {
		return nil
	}

	if reg, ok := withIdentities(st.regs[i], pc.set.Identities); ok {
		saved, err := r.cfg.DB.SaveRegistration(ctx, reg)
		if err != nil {
			r.log.Warn("failed to store the registration", slog.String("impi", impi), slog.Any("error", err))
			return nil
		}

		st.regs[i] = saved
	}

	live := st.live(st.regs[i].Bindings)

	r.releaseRemoved(st, pc, live)

	ch := r.profileNotice(ctx, impi, pc)

	if shorten {
		if err := r.shorten(ctx, st); err != nil {
			r.log.Warn("failed to shorten the registration", slog.String("impi", impi), slog.Any("error", err))
			return nil
		}

		ch.shortened = impi
	}

	if !pc.identitiesChanged() && !shorten {
		return nil
	}

	return r.notifySubscriptions(ctx, impi, ch)
}

// withIdentities is the registration with the registered identity, and that of each binding, among the set's: the
// default identity stands for one that left, at the HSS and as the one the reg event reports as registered. It reports
// whether that changed the registration.
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

// releaseRemoved releases the calls of the bindings for the identities removed from the set, in the form of the set
// their request named (TS 24.229 §5.4.1.8 step 3, §5.4.5.1.2). The identities barred keep theirs (NOTE 2).
func (r *Registrar) releaseRemoved(st *state, pc profileChange, live []db.Binding) {
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

	r.releaseCalls(st, []removal{{
		reg: db.Registration{IMPI: st.impi}, bindings: live, event: reginfo.Rejected, calls: calls,
	}})
}

// profileNotice is the change of the registration state that the reg event reports of the change of a set's profile
// (TS 24.229 §5.4.1.8 step 4, §5.4.2.1.2): the identities added as created, those dropped as rejected, with every
// contact of the set, of every private identity that registered it; and the subscriptions to a dropped identity end.
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

	ch.removed = []removal{{reg: db.Registration{IMPI: impi, Identities: dropped}, bindings: bindings, event: reginfo.Rejected}}
	ch.reason = reasonRejected

	// A subscription is to the identity of the set its Request-URI names, in the first of its forms the set held.
	ch.ended = func(resource string) bool {
		u, err := sip.ParseURI(resource)
		if err != nil {
			return false
		}

		keys := identityKeys(u, r.cfg.HomeDomain)
		i := slices.IndexFunc(keys, func(key string) bool { return holds(pc.before, key) })

		return i >= 0 && holds(dropped, keys[i])
	}

	return ch
}

// defaultIdentity is the first identity of the set that is not barred (TS 29.228 §6.5.1.1).
func defaultIdentity(set []db.PublicIdentity) db.PublicIdentity {
	for _, id := range set {
		if !id.Barred {
			return id
		}
	}

	return db.PublicIdentity{}
}
