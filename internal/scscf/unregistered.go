package scscf

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

// The S-CSCF keeps the user profile that SAR UNREGISTERED_USER downloads for a public identity that is not
// registered, a user's or a PSI's, as an implicit set kept unregistered: for the later requests of the identities of
// the set, or of the wildcarded identity it falls in, rather than ask again, for a registration period, then clears its
// name at the HSS (TS 23.228 §5.12.1 steps 6 and 7, TS 24.229 §5.4.3.2 NOTE 24, §5.4.3.3 NOTE 27, TS 29.228 §6.5.1.5).

// fetch is a download of the profile of a public identity that is not registered, in progress, which requests for the
// same identity wait for.
type fetch struct {
	ready chan struct{}
	set   db.ImplicitSet
	err   error
}

// unregisteredProfile is the set kept of the public identity u unregistered, and its subscription: the one kept, or
// else the one the HSS sends.
func (r *Registrar) unregisteredProfile(ctx context.Context, u sip.URI) (db.ImplicitSet, cx.IMSSubscription, error) {
	set, ok, err := r.keptSet(ctx, u)
	if err != nil {
		return db.ImplicitSet{}, cx.IMSSubscription{}, err
	}

	if !ok {
		if set, err = r.fetchOnce(ctx, u); err != nil {
			return db.ImplicitSet{}, cx.IMSSubscription{}, err
		}
	}

	sub, err := cx.ParseUserData(set.UserData)
	if err != nil {
		return db.ImplicitSet{}, cx.IMSSubscription{}, err
	}

	return set, sub, nil
}

// keptSet is the set kept unregistered, unexpired, that holds u, or that was kept for it or for a wildcarded identity
// it falls in.
func (r *Registrar) keptSet(ctx context.Context, u sip.URI) (db.ImplicitSet, bool, error) {
	now := r.clock.Now()
	keys := identityKeys(u, r.cfg.HomeDomain)

	sets, err := r.cfg.DB.FindUnregisteredSets(ctx, append(slices.Clone(keys), canonicalIdentity(u)))
	if err != nil {
		return db.ImplicitSet{}, false, err
	}

	for _, s := range sets {
		kept := s.Unregistered

		switch {
		case !now.Before(kept.ExpiresAt):
		case slices.Contains(keys, identityKeyOf(kept.Identity)), slices.ContainsFunc(keys, func(k string) bool { return holds(s.Identities, k) }),
			kept.Wildcard != "" && wildcardMatch(kept.Wildcard, u):
			return s, true, nil
		}
	}

	return db.ImplicitSet{}, false, nil
}

// fetchOnce downloads the profile of u, or waits for the download in progress.
func (r *Registrar) fetchOnce(ctx context.Context, u sip.URI) (db.ImplicitSet, error) {
	id := canonicalIdentity(u)

	r.mu.Lock()

	f, waiting := r.fetching[id]
	if !waiting {
		f = &fetch{ready: make(chan struct{})}
		r.fetching[id] = f
	}

	r.mu.Unlock()

	if waiting {
		select {
		case <-f.ready:
			return f.set, f.err
		case <-ctx.Done():
			return db.ImplicitSet{}, ctx.Err()
		}
	}

	f.set, f.err = r.fetchUnregistered(ctx, u, id)

	r.mu.Lock()
	delete(r.fetching, id)
	r.mu.Unlock()

	close(f.ready)

	return f.set, f.err
}

// TS 29.228 §6.1.2.1
func (r *Registrar) fetchUnregistered(ctx context.Context, u sip.URI, id string) (db.ImplicitSet, error) {
	req := cx.ServerAssignmentRequest{PublicIdentities: []string{id}, Type: cx.AssignmentUnregisteredUser}

	saa, from, err := r.serverAssignmentOf(ctx, db.HSS{}, req)

	// The identity falls in a wildcarded identity that is registered: the S-CSCF asks again as if it had been given
	// the wildcarded identity.
	if se, ok := errors.AsType[*cx.ServerAssignmentError](err); ok && se.IsExperimental(tgpp.ResultErrorInAssignmentType) &&
		se.WildcardedPublicIdentity != "" {
		req.WildcardedPublicIdentity = se.WildcardedPublicIdentity

		saa, from, err = r.serverAssignmentOf(ctx, db.HSS{}, req)
	}

	if err != nil {
		return db.ImplicitSet{}, err
	}

	if len(saa.UserData) == 0 {
		return db.ImplicitSet{}, errNoUserData
	}

	sub, err := cx.ParseUserData(saa.UserData)
	if err != nil {
		return db.ImplicitSet{}, err
	}

	next := db.ImplicitSet{
		Identities: implicitSet(sub), UserData: saa.UserData, HSS: from,
		Unregistered: &db.Unregistered{
			Identity: id, Wildcard: cmp.Or(saa.WildcardedPublicIdentity, req.WildcardedPublicIdentity),
			ExpiresAt: r.clock.Now().Add(r.cfg.MaxExpires),
		},
	}

	if err := r.lockProfiles(ctx); err != nil {
		return db.ImplicitSet{}, err
	}

	defer r.unlockProfiles()

	// The set of a profile kept, expired, is the same set downloaded again.
	sets, err := r.cfg.DB.ListUnregisteredSets(ctx)
	if err != nil {
		return db.ImplicitSet{}, err
	}

	key := identityKey(u)

	if i := slices.IndexFunc(sets, func(s db.ImplicitSet) bool {
		return identityKeyOf(s.Unregistered.Identity) == key || slices.ContainsFunc(next.Identities, func(id db.PublicIdentity) bool {
			return holds(s.Identities, id.Key)
		})
	}); i >= 0 {
		next.ID = sets[i].ID
	}

	saved, err := r.cfg.DB.SaveSet(ctx, next)

	// A registered set holds an identity of the profile, as one whose contacts expired, not yet removed: the request is
	// served with the profile, which is not kept.
	if errors.Is(err, db.ErrIdentityConflict) {
		r.log.Debug("unregistered profile with an identity of a registered set: not kept", slog.String("impu", id),
			slog.Any("error", err))

		return next, nil
	}

	return saved, err
}

// releaseUnregistered drops the sets kept unregistered for a registration period, and clears the S-CSCF's name for
// their identities at the HSS: TIMEOUT_DEREGISTRATION takes an unregistered identity to not registered (TS 23.228
// §5.12.1, TS 29.228 §6.1.2.1). An identity registered since keeps the assignment of its registration.
func (r *Registrar) releaseUnregistered(ctx context.Context) {
	now := r.clock.Now()

	sets, err := r.cfg.DB.ListUnregisteredSets(ctx)
	if err != nil {
		r.log.Warn("failed to list the unregistered profiles", slog.Any("error", err))
		return
	}

	for _, s := range sets {
		if now.Before(s.Unregistered.ExpiresAt) {
			break
		}

		if err := r.cfg.DB.DeleteSet(ctx, s.ID); err != nil {
			r.log.Warn("failed to delete an unregistered profile", slog.String("impu", s.Unregistered.Identity),
				slog.Any("error", err))

			continue
		}

		r.forgetASs(append(identityKeysOf(s.Identities), identityKeyOf(s.Unregistered.Identity)))

		u, err := sip.ParseURI(s.Unregistered.Identity)
		if err != nil {
			continue
		}

		if regs, _, err := Recipients(ctx, r.cfg.DB, u, r.cfg.HomeDomain); err != nil || registeredAt(regs, now) {
			continue
		}

		r.log.Debug("unregistered profile released", slog.String("impu", s.Unregistered.Identity))

		req := cx.ServerAssignmentRequest{
			PublicIdentities: []string{s.Unregistered.Identity}, WildcardedPublicIdentity: s.Unregistered.Wildcard,
			Type: assignTimeoutDeregistration,
		}

		if _, _, err := r.serverAssignmentOf(ctx, s.HSS, req); err != nil {
			r.log.Warn("failed to release an unregistered profile at the HSS", slog.String("impu", s.Unregistered.Identity),
				slog.Any("error", err))
		}
	}
}

// dropUnregistered forgets the sets kept unregistered of a private identity's subscription, or that hold one of the
// public identities, that the HSS deregistered (TS 29.228 §6.1.3), without telling the HSS.
func (r *Registrar) dropUnregistered(ctx context.Context, impi string, identities []string) {
	keys := make([]string, 0, len(identities))

	for _, s := range identities {
		keys = append(keys, identityKeyOf(s))
	}

	sets, err := r.cfg.DB.ListUnregisteredSets(ctx)
	if err != nil {
		r.log.Warn("failed to list the unregistered profiles", slog.Any("error", err))
		return
	}

	for _, s := range sets {
		held := slices.Contains(keys, identityKeyOf(s.Unregistered.Identity)) ||
			slices.ContainsFunc(s.Identities, func(id db.PublicIdentity) bool { return slices.Contains(keys, id.Key) })

		if !held && (impi == "" || privateIdentityOf(s.UserData) != impi) {
			continue
		}

		if err := r.cfg.DB.DeleteSet(ctx, s.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
			r.log.Warn("failed to delete an unregistered profile", slog.String("impu", s.Unregistered.Identity),
				slog.Any("error", err))
		}
	}
}

func identityKeysOf(ids []db.PublicIdentity) []string {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, id.Key)
	}

	return keys
}

func registeredAt(regs []db.Registration, now time.Time) bool {
	for _, reg := range regs {
		if len(liveAt(reg.Bindings, now)) > 0 {
			return true
		}
	}

	return false
}
