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

	var out []*outgoing

	for _, impi := range impis {
		o, err := r.terminate(ctx, impi, keys, rtr.Reason.Code)
		if err != nil {
			r.send(out)
			return nil, err
		}

		out = append(out, o...)
	}

	r.send(out)

	return impis[1:], nil
}

func (r *Registrar) terminate(ctx context.Context, impi string, keys []string, reason cx.ReasonCode) ([]*outgoing, error) {
	if reason == cx.ReasonRemoveSCSCF {
		r.log.Debug("RTR REMOVE_S-CSCF: no profile kept", slog.String("impi", impi))
		return nil, nil
	}

	if err := r.lock(ctx, impi); err != nil {
		return nil, err
	}

	defer r.unlock(impi)

	st, err := r.load(ctx, impi)
	if err != nil {
		return nil, err
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
			return nil, err
		}

		if len(rm.bindings) > 0 {
			ch.removed = append(ch.removed, rm)
		}

		r.log.Info("registration terminated by the HSS", slog.String("impi", impi), slog.String("impu", reg.IMPU),
			slog.String("reason", reason.String()))
	}

	r.releaseCalls(ch.removed)

	if reason != cx.ReasonNewServerAssigned {
		if len(ch.removed) == 0 {
			return nil, nil
		}

		return r.notifyChange(ctx, impi, ch), nil
	}

	if after, err := r.load(ctx, impi); err == nil && !after.any() {
		subs, err := r.cfg.DB.ListRegSubscriptions(ctx, impi)
		if err != nil {
			return nil, err
		}

		for _, s := range subs {
			r.dropSubscription(ctx, s)
		}
	}

	return nil, nil
}
