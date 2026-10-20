package scscf

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/regevent"
)

func (r *Registrar) Terminate(ctx context.Context, rtr cx.RegistrationTerminationRequest) ([]string, error) {
	ctx, done, ok := r.begin(ctx)
	if !ok {
		return nil, errors.New("registrar closed")
	}

	defer done()

	impis := []string{rtr.PrivateIdentity}

	for _, impi := range rtr.AssociatedIdentities {
		if !slices.Contains(impis, impi) {
			impis = append(impis, impi)
		}
	}

	keys := make([]string, 0, len(rtr.PublicIdentities))
	for _, impu := range rtr.PublicIdentities {
		keys = append(keys, identityKeyOf(impu))
	}

	// The unregistered profiles of the identities, or of the subscription, go as well (TS 29.228 §6.1.3).
	for _, impi := range impis {
		r.dropUnregistered(impi, rtr.PublicIdentities)
	}

	var (
		out []*outgoing
		tps []*thirdParty
	)

	for _, impi := range impis {
		o, t, err := r.terminate(ctx, impi, keys, rtr.Reason.Code)

		out, tps = append(out, o...), append(tps, t...)

		if err != nil {
			r.send(out)
			r.sendThirdParty(tps)

			return nil, err
		}
	}

	r.send(out)
	r.sendThirdParty(tps)

	return impis[1:], nil
}

func (r *Registrar) terminate(ctx context.Context, impi string, keys []string, reason cx.ReasonCode) ([]*outgoing, []*thirdParty, error) {
	if reason == cx.ReasonRemoveSCSCF {
		r.log.Debug("RTR REMOVE_S-CSCF: no profile kept", slog.String("impi", impi))
		return nil, nil, nil
	}

	if err := r.lock(ctx, impi); err != nil {
		return nil, nil, err
	}

	defer r.unlock(impi)

	st, err := r.load(ctx, impi)
	if err != nil {
		return nil, nil, err
	}

	event, subReason := regevent.Rejected, reasonRejected
	if reason == cx.ReasonServerChange {
		event, subReason = regevent.Deactivated, reasonDeactivated
	}

	ch := change{reason: subReason}

	if reason == cx.ReasonServerChange {
		keys = nil
	}

	for _, reg := range st.regs {
		if len(keys) > 0 && !slices.ContainsFunc(keys, func(k string) bool { return holds(reg.Identities, k) }) {
			continue
		}

		rm, _, err := r.removeBindings(ctx, st, reg, st.live(reg.Bindings), event)
		if err != nil {
			return nil, nil, err
		}

		if len(rm.bindings) > 0 {
			ch.removed = append(ch.removed, rm)
		}

		r.log.Info("registration terminated by the HSS", slog.String("impi", impi), slog.String("impu", reg.IMPU),
			slog.String("reason", reason.String()))
	}

	r.releaseCalls(st, ch.removed)

	if reason != cx.ReasonNewServerAssigned {
		if len(ch.removed) == 0 {
			return nil, nil, nil
		}

		var tps []*thirdParty
		for _, rm := range ch.removed {
			tps = append(tps, r.deregistered(rm)...)
		}

		return r.notifyChange(ctx, impi, ch), tps, nil
	}

	// TS 29.228 §6.1.3: NEW_SERVER_ASSIGNED only has the S-CSCF remove what it has of the identities. The new S-CSCF
	// registered them at the ASs: a third-party deregistration would undo it.
	for _, rm := range ch.removed {
		if rm.ended {
			keys := make([]string, 0, len(rm.reg.Identities))
			for _, id := range rm.reg.Identities {
				keys = append(keys, id.Key)
			}

			r.forgetASs(keys)
		}
	}

	if after, err := r.load(ctx, impi); err == nil && !after.any() {
		subs, err := r.cfg.DB.ListRegSubscriptions(ctx, impi)
		if err != nil {
			return nil, nil, err
		}

		for _, s := range subs {
			r.dropSubscription(ctx, s)
		}
	}

	return nil, nil, nil
}
