package scscf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/reginfo"
)

var (
	// ErrUserUnknown is a profile push for a private identity the S-CSCF serves no registration or profile of
	// (TS 29.228 Table 6.2.2.1.1).
	ErrUserUnknown = errors.New("scscf: private identity unknown")

	// ErrUnsupportedUserData is a profile push with user information the S-CSCF does not recognise or support
	// (TS 29.228 Table 6.2.2.1.1).
	ErrUnsupportedUserData = errors.New("scscf: unsupported user data")
)

// PushProfile applies a profile that the HSS pushes (TS 29.228 §6.2.2.1, §6.5.2.1, TS 24.229 §5.4.1.8). The user
// profile replaces that of the implicit registration set it lists, in every registration of the set: the HSS names one
// of the private identities that share it. The charging information and the allowed WAF and WWSF identities are not
// used by the S-CSCF. On an error, the S-CSCF keeps what it had.
func (r *Registrar) PushProfile(ctx context.Context, ppr cx.PushProfileRequest) error {
	ctx, done, ok := r.begin(ctx)
	if !ok {
		return errors.New("registrar closed")
	}

	defer done()

	if len(ppr.UserData) == 0 {
		known, err := r.serves(ctx, ppr.PrivateIdentity)
		if err != nil {
			return err
		}

		if !known {
			return ErrUserUnknown
		}

		return nil
	}

	sub, err := cx.ParseUserData(ppr.UserData)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnsupportedUserData, err)
	}

	ids := implicitSet(sub)
	if len(ids) == 0 {
		return fmt.Errorf("%w: no public user identity", ErrUnsupportedUserData)
	}

	out, held, err := r.pushProfile(ctx, ppr.PrivateIdentity, sub, ids, ppr.UserData)
	if err != nil {
		r.send(out)
		return err
	}

	kept := r.pushUnregistered(ppr.PrivateIdentity, sub)

	// The HSS tries another of the private identities of the set (TS 29.228 §6.2.2.1).
	if !held && !kept {
		return ErrUserUnknown
	}

	impis, err := r.sharing(ctx, ppr.PrivateIdentity, ids)
	if err != nil {
		r.send(out)
		return err
	}

	for _, impi := range impis {
		o, _, err := r.pushProfile(ctx, impi, sub, ids, ppr.UserData)

		out = append(out, o...)

		if err != nil {
			r.send(out)
			return err
		}
	}

	r.send(out)

	return nil
}

// serves reports whether the S-CSCF holds a registration, or a profile kept unregistered, of the private identity.
func (r *Registrar) serves(ctx context.Context, impi string) (bool, error) {
	if err := r.lock(ctx, impi); err != nil {
		return false, err
	}

	st, err := r.load(ctx, impi)

	r.unlock(impi)

	if err != nil {
		return false, err
	}

	if st.any() {
		return true, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, e := range r.unregistered {
		if e.done() && e.err == nil && e.sub.PrivateIdentity == impi {
			return true, nil
		}
	}

	return false, nil
}

// sharing are the other private identities with a registration of the set.
func (r *Registrar) sharing(ctx context.Context, impi string, ids []db.PublicIdentity) ([]string, error) {
	var impis []string

	for _, id := range ids {
		regs, err := r.cfg.DB.ListRegistrationsByIdentity(ctx, id.Key)
		if err != nil {
			return nil, err
		}

		for _, reg := range regs {
			if reg.IMPI != impi && !slices.Contains(impis, reg.IMPI) {
				impis = append(impis, reg.IMPI)
			}
		}
	}

	return impis, nil
}

// pushProfile applies the profile to the registered sets of the private identity that it overlaps, and reports
// whether there was any.
func (r *Registrar) pushProfile(ctx context.Context, impi string, sub cx.IMSSubscription, ids []db.PublicIdentity,
	userData []byte,
) ([]*outgoing, bool, error) {
	if err := r.lock(ctx, impi); err != nil {
		return nil, false, err
	}

	defer r.unlock(impi)

	st, err := r.load(ctx, impi)
	if err != nil {
		return nil, false, err
	}

	var (
		held    bool
		reauth  bool
		changed bool
		ch      = change{reason: reasonRejected, created: map[string]bool{}}
	)

	for i := range st.regs {
		reg := st.regs[i]
		if !st.registered(&reg) || !overlaps(reg.Identities, ids) {
			continue
		}

		held = true

		if bytes.Equal(reg.UserData, userData) {
			continue
		}

		// TS 29.228 §6.5.2.1: the HSS deregisters the set with an RTR to leave it no identity, or to change its
		// default identity.
		if !hasUnbarred(ids) {
			return nil, true, errors.New("profile push that bars every identity of a registered set")
		}

		if before, after := defaultIdentity(reg.Identities), defaultIdentity(ids); before.Key != after.Key {
			return nil, true, fmt.Errorf("profile push that changes the default identity of a registered set from %s to %s",
				before.URI, after.URI)
		}

		dropped, added := identityChanges(reg.Identities, ids)
		live := st.live(reg.Bindings)

		// TS 24.229 §5.4.1.8 step 3: the calls of the identities that leave the set end before they do.
		if len(dropped) > 0 {
			rm := removal{reg: db.Registration{IMPI: impi, Identities: dropped}, bindings: live, event: reginfo.Rejected}
			rm.calls = r.callsServing(live, reg.Identities, dropped)

			r.releaseCalls(st, []removal{rm})

			ch.removed = append(ch.removed, rm)
		}

		reauth = reauth || r.newRegisterAS(reg.UserData, sub)

		reg.Identities, reg.UserData = ids, userData

		// The identity the UE registered may be gone: the set's default identity stands for it, at the HSS and
		// as the one whose binding the reg event reports as "registered".
		if !holds(ids, identityKeyOf(reg.IMPU)) {
			reg.IMPU = defaultIdentity(ids).URI
		}

		reg.Bindings = slices.Clone(reg.Bindings)

		for j, b := range reg.Bindings {
			if b.IMPU != "" && !holds(ids, b.IMPU) {
				reg.Bindings[j].IMPU = defaultIdentity(ids).Key
			}
		}

		saved, err := r.cfg.DB.SaveRegistration(ctx, reg)
		if err != nil {
			return nil, true, err
		}

		st.regs[i] = saved

		for _, id := range added {
			ch.created[id.Key] = true
		}

		changed = changed || len(dropped) > 0 || len(added) > 0

		r.log.Info("user profile updated by the HSS", slog.String("impi", impi), slog.String("impu", reg.IMPU),
			slog.Int("added", len(added)), slog.Int("removed", len(dropped)))
	}

	// TS 24.229 §5.4.1.8 step 4: the UE and the IM CN entities learn of the change from the reg event, and an AS
	// that the REGISTER now triggers from the re-registration the shortened registration brings.
	if reauth {
		if err := r.shorten(ctx, st); err != nil {
			return nil, true, err
		}

		ch.shortened = impi
	}

	if !changed && !reauth {
		return nil, held, nil
	}

	return r.notifySubscriptions(ctx, impi, ch), held, nil
}

// pushUnregistered replaces the profile that the S-CSCF keeps of an identity unregistered with the one pushed, and
// forgets it when the identity has left the set, to ask the HSS again. It reports whether there was any.
func (r *Registrar) pushUnregistered(impi string, sub cx.IMSSubscription) bool {
	keys := profileKeys(sub)

	r.mu.Lock()
	defer r.mu.Unlock()

	found := false

	for id, e := range r.unregistered {
		if !e.done() || e.err != nil {
			continue
		}

		if !sharesKey(profileKeys(e.sub), keys) {
			continue
		}

		found = true

		u, err := sip.ParseURI(id)
		if err != nil {
			delete(r.unregistered, id)
			continue
		}

		if _, ok := serviceProfileOf(sub, u, e.wildcard, r.cfg.HomeDomain); !ok {
			delete(r.unregistered, id)
			continue
		}

		// The entry is read without the lock once ready: a new one takes its place.
		ready := make(chan struct{})
		close(ready)

		r.unregistered[id] = &unregistered{
			identity: e.identity, wildcard: e.wildcard, hss: e.hss, sub: sub, expires: e.expires, ready: ready,
		}
	}

	if found {
		r.log.Info("unregistered user profile updated by the HSS", slog.String("impi", impi))
	}

	return found
}

// profileKeys are the keys of the public identities of a subscription, wildcarded ones included.
func profileKeys(sub cx.IMSSubscription) map[string]bool {
	keys := map[string]bool{}

	for _, p := range sub.ServiceProfiles {
		for _, id := range p.PublicIdentities {
			keys[identityKeyOf(id.Identity)] = true
		}
	}

	return keys
}

func sharesKey(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}

	return false
}

func overlaps(a, b []db.PublicIdentity) bool {
	return slices.ContainsFunc(a, func(id db.PublicIdentity) bool { return holds(b, id.Key) })
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

// identityChanges are the non-barred identities of the set before that are gone or barred after, and the non-barred
// identities after that were not, or were barred, before (TS 24.229 §5.4.1.8 steps 1 and 2).
func identityChanges(before, after []db.PublicIdentity) (dropped, added []db.PublicIdentity) {
	unbarred := func(set []db.PublicIdentity, key string) bool {
		return slices.ContainsFunc(set, func(id db.PublicIdentity) bool { return id.Key == key && !id.Barred })
	}

	for _, id := range before {
		if !id.Barred && !unbarred(after, id.Key) {
			dropped = append(dropped, id)
		}
	}

	for _, id := range after {
		if !id.Barred && !unbarred(before, id.Key) {
			added = append(added, id)
		}
	}

	return dropped, added
}

// callsServing are the calls of the bindings whose served user is one of the identities dropped from the set: the
// identity of the set its request named, in the first of its forms the set holds.
func (r *Registrar) callsServing(bindings []db.Binding, set, dropped []db.PublicIdentity,
) map[bindingKey]map[*proxy.Dialog]proxy.Side {
	pick := func(served []string) bool {
		i := slices.IndexFunc(served, func(key string) bool { return holds(set, key) })
		return i >= 0 && holds(dropped, served[i])
	}

	out := make(map[bindingKey]map[*proxy.Dialog]proxy.Side, len(bindings))
	for _, b := range bindings {
		out[bindingKey(b.ID)] = r.calls.serving(bindingKey(b.ID), pick)
	}

	return out
}

// newRegisterAS reports whether the profile pushed has an AS that a REGISTER may trigger, which the stored profile has
// not: one the UE's next re-registration notifies (TS 24.229 §5.4.1.8 step 4 b).
func (r *Registrar) newRegisterAS(stored []byte, pushed cx.IMSSubscription) bool {
	before := map[string]bool{}

	if sub, err := cx.ParseUserData(stored); err == nil {
		for _, as := range registerASs(sub) {
			before[as] = true
		}
	}

	return slices.ContainsFunc(registerASs(pushed), func(as string) bool { return !before[as] })
}

// registerASs are the ASs of the iFCs that a REGISTER may trigger, of the service profiles with a non-barred identity,
// as thirdParties evaluates them (TS 24.229 §5.4.1.7).
func registerASs(sub cx.IMSSubscription) []string {
	var out []string

	for _, p := range sub.ServiceProfiles {
		if !slices.ContainsFunc(p.PublicIdentities, func(id cx.ProfileIdentity) bool { return !id.Barred && !wildcarded(id) }) {
			continue
		}

		for _, c := range filterCriteria(p, true) {
			if _, bad := invalidExpression(c.Trigger); bad || !mayRegister(c.Trigger) {
				continue
			}

			if as := strings.ToLower(c.ApplicationServer.ServerName); !slices.Contains(out, as) {
				out = append(out, as)
			}
		}
	}

	return out
}
