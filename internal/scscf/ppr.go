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
	"github.com/ellanetworks/ims/internal/homedomain"
	"github.com/ellanetworks/ims/sip"
)

const CxLockTimeout = 5 * time.Second

var (
	// TS 29.228 clause 6.2.2.1 table 6.2.2.1.1
	ErrUserUnknown = errors.New("scscf: private identity unknown")

	// TS 29.228 clause 6.2.2.1 table 6.2.2.1.1
	ErrUnsupportedUserData = errors.New("scscf: unsupported user data")

	// TS 29.228 clause 6.2.2.1 table 6.2.2.1.1
	// TS 29.228 clause 6.5.2.1
	ErrUnableToComply = errors.New("scscf: unable to comply")
)

// TS 29.228 clause 6.2.2.1
// TS 29.228 clause 6.5.2.1
// TS 29.228 clause 6.6.1
// TS 24.229 clause 5.4.1.8
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

	r.log.InfoContext(ctx, "user profile updated by the HSS", slog.String("impi", ppr.PrivateIdentity),
		slog.Int("added", len(pc.added)), slog.Int("removed", len(pc.removed)), slog.Int("barred", len(pc.barred)),
		slog.Bool("reauthenticate", shorten))

	r.spreadProfile(ctx, *pc, "", shorten)

	return nil
}

func (r *Registrar) storePushed(ctx context.Context, impi string, sub cx.IMSSubscription, data []byte,
) (*profileChange, bool, error) {
	set, regs, err := r.pushedSet(ctx, sub)
	if err != nil {
		return nil, false, err
	}

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

	if u := set.Unregistered; u != nil {
		if id, err := sip.ParseURI(u.Identity); err != nil || !servesUnregistered(sub, id, u.Wildcard, r.cfg.Home) {
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

func (r *Registrar) pushedSet(ctx context.Context, sub cx.IMSSubscription) (db.ImplicitSet, []db.Registration, error) {
	sets, err := r.cfg.DB.FindSets(ctx, identityKeysOf(implicitSet(sub)))
	if err != nil {
		return db.ImplicitSet{}, nil, err
	}

	if len(sets) > 1 {
		return db.ImplicitSet{}, nil, fmt.Errorf("%w: profile push with the identities of %d sets", ErrUnableToComply, len(sets))
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

func privateIdentityOf(data []byte) string {
	sub, err := cx.ParseUserData(data)
	if err != nil {
		return ""
	}

	return sub.PrivateIdentity
}

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

func servesUnregistered(sub cx.IMSSubscription, u sip.URI, wildcard string, homeDomains homedomain.Domains) bool {
	_, ok := serviceProfileOf(sub, u, wildcard, homeDomains)
	return ok
}
