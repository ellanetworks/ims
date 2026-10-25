package scscf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

// CxLockTimeout bounds how long a request of the HSS waits for the registrations it changes, held by a registration
// that may itself wait for the HSS: the HSS gets an answer before its own timer for it fires, commonly 10 s or more.
const CxLockTimeout = 5 * time.Second

var (
	// ErrUserUnknown is a request of the HSS for a private identity the S-CSCF serves no registration or profile of
	// (TS 29.228 Table 6.2.2.1.1).
	ErrUserUnknown = errors.New("scscf: private identity unknown")

	// ErrUnsupportedUserData is a profile push with user information the S-CSCF does not recognise or support
	// (TS 29.228 Table 6.2.2.1.1).
	ErrUnsupportedUserData = errors.New("scscf: unsupported user data")

	// ErrUnableToComply is a profile push the S-CSCF does not apply: one the HSS shall not send, or of a set it does
	// not hold (TS 29.228 Table 6.2.2.1.1, §6.5.2.1).
	ErrUnableToComply = errors.New("scscf: unable to comply")
)

// PushProfile applies a profile that the HSS pushes (TS 29.228 §6.2.2.1, §6.5.2.1, §6.6.1; TS 24.229 §5.4.1.8): the
// user profile replaces that of the implicit set it is of, registered by any private identity, or kept unregistered.
// The S-CSCF stores the new profile at once, or keeps what it had on an error; then each private identity that
// registered the set learns of the change. The charging information and the allowed WAF and WWSF identities are not
// used by the S-CSCF.
func (r *Registrar) PushProfile(ctx context.Context, ppr cx.PushProfileRequest) error {
	ctx, done, ok := r.begin(ctx)
	if !ok {
		return errors.New("registrar closed")
	}

	defer done()

	ctx, cancel := context.WithTimeout(ctx, CxLockTimeout)
	defer cancel()

	if len(ppr.UserData) == 0 {
		return r.known(ctx, ppr.PrivateIdentity)
	}

	sub, err := cx.ParseUserData(ppr.UserData)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnsupportedUserData, err)
	}

	if err := r.lockProfiles(ctx); err != nil {
		return err
	}

	pc, shorten, err := r.storePushed(ctx, ppr.PrivateIdentity, sub, ppr.UserData)

	r.unlockProfiles()

	if err != nil || pc == nil {
		return err
	}

	r.log.Info("user profile updated by the HSS", slog.String("impi", ppr.PrivateIdentity),
		slog.Int("added", len(pc.added)), slog.Int("removed", len(pc.removed)), slog.Int("barred", len(pc.barred)),
		slog.Bool("reauthenticate", shorten))

	r.spreadProfile(r.ctx, *pc, "", shorten)

	return nil
}

// storePushed stores the profile pushed in place of that of its set, holding the lock of the profiles, and returns
// the change, nil if none, and whether the registrations of the set are to be shortened.
func (r *Registrar) storePushed(ctx context.Context, impi string, sub cx.IMSSubscription, data []byte,
) (*profileChange, bool, error) {
	set, regs, err := r.pushedSet(ctx, sub)
	if err != nil {
		return nil, false, err
	}

	// The HSS names a private identity known from an earlier server assignment (TS 29.228 Table 6.2.2.1).
	if set.ID == 0 || !slices.ContainsFunc(regs, func(reg db.Registration) bool { return reg.IMPI == impi }) &&
		privateIdentityOf(set.UserData) != impi {
		if err := r.known(ctx, impi); err != nil {
			return nil, false, err
		}

		if set.ID == 0 {
			return nil, false, fmt.Errorf("%w: profile of no set the S-CSCF holds", ErrUnableToComply)
		}
	}

	ids := implicitSet(sub)
	registered := registeredAt(regs, r.clock.Now())

	// TS 29.228 §6.5.2.1: the HSS deregisters the set with an RTR to leave it no identity, or to change its default
	// identity.
	if registered {
		if !hasUnbarred(ids) {
			return nil, false, fmt.Errorf("%w: profile push that bars every identity of a registered set", ErrUnableToComply)
		}

		if before, after := defaultIdentity(set.Identities), defaultIdentity(ids); before.Key != after.Key {
			return nil, false, fmt.Errorf("%w: profile push that changes the default identity of a registered set from %s to %s",
				ErrUnableToComply, before.URI, after.URI)
		}
	}

	if bytes.Equal(set.UserData, data) {
		return nil, false, nil
	}

	next := set
	next.Identities, next.UserData = ids, data

	// A set kept for an identity that left it, or for a wildcarded identity it no longer has, goes: the S-CSCF asks the
	// HSS again at the identity's next request.
	if u := set.Unregistered; u != nil {
		if id, err := sip.ParseURI(u.Identity); err != nil || !servesUnregistered(sub, id, u.Wildcard, r.cfg.HomeDomain) {
			if err := r.cfg.DB.DeleteSet(ctx, set.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
				return nil, false, err
			}

			return nil, false, nil
		}
	}

	saved, err := r.cfg.DB.SaveSet(ctx, next)
	if err != nil {
		return nil, false, err
	}

	pc := diffProfile(set.Identities, set.UserData, saved, sub)

	return &pc, registered && pc.newAS, nil
}

// pushedSet is the set the profile pushed is of, and the registrations of it: the set that holds one of its public
// user identities, or else the set kept unregistered for one of its identities, a PSI's or a wildcarded one's.
func (r *Registrar) pushedSet(ctx context.Context, sub cx.IMSSubscription) (db.ImplicitSet, []db.Registration, error) {
	sets, err := r.cfg.DB.FindSets(ctx, identityKeysOf(implicitSet(sub)))
	if err != nil {
		return db.ImplicitSet{}, nil, err
	}

	if len(sets) == 0 {
		kept, err := r.cfg.DB.ListUnregisteredSets(ctx)
		if err != nil {
			return db.ImplicitSet{}, nil, err
		}

		names := profileNames(sub)

		sets = slices.DeleteFunc(kept, func(s db.ImplicitSet) bool {
			return !names[identityKeyOf(s.Unregistered.Identity)] && (s.Unregistered.Wildcard == "" || !names[s.Unregistered.Wildcard])
		})
	}

	if len(sets) == 0 {
		return db.ImplicitSet{}, nil, nil
	}

	regs, err := r.cfg.DB.ListRegistrationsBySet(ctx, sets[0].ID)
	if err != nil {
		return db.ImplicitSet{}, nil, err
	}

	return sets[0], regs, nil
}

// known is nil if the S-CSCF holds a registration of the private identity, or a set kept unregistered of its
// subscription, and ErrUserUnknown otherwise.
func (r *Registrar) known(ctx context.Context, impi string) error {
	regs, err := r.cfg.DB.ListRegistrationsByIMPI(ctx, impi)
	if err != nil {
		return err
	}

	if len(regs) > 0 {
		return nil
	}

	kept, err := r.cfg.DB.ListUnregisteredSets(ctx)
	if err != nil {
		return err
	}

	if slices.ContainsFunc(kept, func(s db.ImplicitSet) bool { return privateIdentityOf(s.UserData) == impi }) {
		return nil
	}

	return ErrUserUnknown
}

// privateIdentityOf is the private identity of the subscription of a profile.
func privateIdentityOf(data []byte) string {
	sub, err := cx.ParseUserData(data)
	if err != nil {
		return ""
	}

	return sub.PrivateIdentity
}

// profileNames are the public identities of a subscription, by key and as they are, wildcarded ones included.
func profileNames(sub cx.IMSSubscription) map[string]bool {
	names := map[string]bool{}

	for _, p := range sub.ServiceProfiles {
		for _, id := range p.PublicIdentities {
			for _, name := range []string{id.Identity, id.WildcardedPSI, id.WildcardedIMPU} {
				if name != "" {
					names[name], names[identityKeyOf(name)] = true, true
				}
			}
		}
	}

	return names
}

// servesUnregistered reports whether the subscription still has a service profile for the identity a set was kept for.
func servesUnregistered(sub cx.IMSSubscription, u sip.URI, wildcard, homeDomain string) bool {
	_, ok := serviceProfileOf(sub, u, wildcard, homeDomain)
	return ok
}
