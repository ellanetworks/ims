package scscf

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

// unregistered is the user profile that SAR UNREGISTERED_USER downloaded for a public identity that is not registered,
// a user's or a PSI's. The S-CSCF keeps it for the identity's later requests rather than ask again, for a registration
// period, then clears its name at the HSS (TS 23.228 §5.12.1 steps 6 and 7, TS 24.229 §5.4.3.2 NOTE 24, §5.4.3.3
// NOTE 27).
type unregistered struct {
	// identity is the canonical public identity of the SAR, and wildcard the wildcarded identity the HSS says it falls
	// in, if any (TS 29.228 Table 6.1.2.2).
	identity string
	wildcard string
	hss      db.HSS
	sub      cx.IMSSubscription
	userData []byte
	expires  time.Time

	// ready is closed once the SAA came, err its failure.
	ready chan struct{}
	err   error
}

func (e *unregistered) done() bool {
	select {
	case <-e.ready:
		return true
	default:
		return false
	}
}

// unregisteredProfile is the profile of the public identity u unregistered: the one kept, or else the one the HSS
// sends, which requests for the same identity in the meantime wait for.
func (r *Registrar) unregisteredProfile(ctx context.Context, u sip.URI) (*unregistered, error) {
	id := canonicalIdentity(u)
	now := r.clock.Now()

	r.mu.Lock()

	e := r.unregistered[id]

	fetch := e == nil || e.done() && !now.Before(e.expires)
	if fetch {
		e = &unregistered{identity: id, ready: make(chan struct{})}
		r.unregistered[id] = e
	}

	r.mu.Unlock()

	if !fetch {
		select {
		case <-e.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		return e, e.err
	}

	err := r.fetchUnregistered(ctx, e)
	if err != nil {
		r.mu.Lock()

		if r.unregistered[id] == e {
			delete(r.unregistered, id)
		}

		r.mu.Unlock()
	}

	e.err = err
	close(e.ready)

	return e, err
}

// TS 29.228 §6.1.2.1
func (r *Registrar) fetchUnregistered(ctx context.Context, e *unregistered) error {
	req := cx.ServerAssignmentRequest{PublicIdentities: []string{e.identity}, Type: cx.AssignmentUnregisteredUser}

	saa, from, err := r.serverAssignmentOf(ctx, db.HSS{}, req)

	// The identity falls in a wildcarded identity that is registered: the S-CSCF asks again as if it had been given
	// the wildcarded identity.
	if se, ok := errors.AsType[*cx.ServerAssignmentError](err); ok && se.IsExperimental(tgpp.ResultErrorInAssignmentType) &&
		se.WildcardedPublicIdentity != "" {
		e.wildcard = se.WildcardedPublicIdentity
		req.WildcardedPublicIdentity = e.wildcard

		saa, from, err = r.serverAssignmentOf(ctx, db.HSS{}, req)
	}

	if err != nil {
		return err
	}

	if len(saa.UserData) == 0 {
		return errNoUserData
	}

	sub, err := cx.ParseUserData(saa.UserData)
	if err != nil {
		return err
	}

	e.wildcard = cmp.Or(saa.WildcardedPublicIdentity, e.wildcard)
	e.hss, e.sub, e.userData = from, sub, saa.UserData
	e.expires = r.clock.Now().Add(r.cfg.MaxExpires)

	return nil
}

// releaseUnregistered drops the unregistered profiles kept for a registration period, and clears the S-CSCF's name
// for their identities at the HSS: TIMEOUT_DEREGISTRATION takes an unregistered identity to not registered (TS 23.228
// §5.12.1, TS 29.228 §6.1.2.1). An identity registered since keeps the assignment of its registration.
func (r *Registrar) releaseUnregistered(ctx context.Context) {
	now := r.clock.Now()

	var expired []*unregistered

	r.mu.Lock()

	for id, e := range r.unregistered {
		if e.done() && e.err == nil && !now.Before(e.expires) {
			expired = append(expired, e)

			delete(r.unregistered, id)
		}
	}

	r.mu.Unlock()

	for _, e := range expired {
		u, err := sip.ParseURI(e.identity)
		if err != nil {
			continue
		}

		r.forgetASs([]string{identityKey(u)})

		if regs, _, err := Recipients(ctx, r.cfg.DB, u, r.cfg.HomeDomain); err != nil || registeredAt(regs, now) {
			continue
		}

		r.log.Debug("unregistered profile released", slog.String("impu", e.identity))

		req := cx.ServerAssignmentRequest{
			PublicIdentities: []string{e.identity}, WildcardedPublicIdentity: e.wildcard, Type: assignTimeoutDeregistration,
		}

		if _, _, err := r.serverAssignmentOf(ctx, e.hss, req); err != nil {
			r.log.Warn("failed to release an unregistered profile at the HSS", slog.String("impu", e.identity), slog.Any("error", err))
		}
	}
}

// dropUnregistered forgets the unregistered profiles of public identities that registered, or that the HSS
// deregistered (TS 29.228 §6.1.3), or of a private identity's subscription, without telling the HSS.
func (r *Registrar) dropUnregistered(impi string, identities []string) {
	ids := make(map[string]bool, len(identities))

	for _, s := range identities {
		if u, err := sip.ParseURI(s); err == nil {
			ids[canonicalIdentity(u)] = true
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for id, e := range r.unregistered {
		if ids[id] || impi != "" && e.done() && e.err == nil && e.sub.PrivateIdentity == impi {
			delete(r.unregistered, id)
		}
	}
}

func registeredAt(regs []db.Registration, now time.Time) bool {
	for _, reg := range regs {
		if len(liveAt(reg.Bindings, now)) > 0 {
			return true
		}
	}

	return false
}
