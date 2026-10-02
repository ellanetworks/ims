package scscf

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regevent"
)

// Terminate handles a Registration-Termination-Request (TS 29.228 §6.1.3,
// TS 24.229 §5.4.1.5). It returns the private identities processed, once
// local state is removed; the NOTIFYs follow.
func (r *Registrar) Terminate(ctx context.Context, rtr cx.RegistrationTerminationRequest) ([]string, error) {
	if !r.start() {
		return nil, errors.New("registrar closed")
	}

	defer r.wg.Done()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()

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

	return impis, nil
}

func (r *Registrar) terminate(ctx context.Context, impi string, keys []string, reason cx.ReasonCode) ([]*outgoing, error) {
	if reason == cx.ReasonRemoveSCSCF {
		r.log.Info("RTR REMOVE_S-CSCF: no profile kept", slog.String("impi", impi))
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

	for _, reg := range st.regs {
		if len(keys) > 0 && !slices.ContainsFunc(keys, func(k string) bool { return holds(reg.Identities, k) }) {
			continue
		}

		if live := st.live(reg.Bindings); len(live) > 0 {
			ch.removed = append(ch.removed, removal{reg: reg, bindings: live, event: event})
		}

		if err := r.cfg.DB.DeleteRegistration(ctx, reg.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
			return nil, err
		}

		r.log.Info("registration terminated by the HSS", slog.String("impi", impi), slog.String("impu", reg.IMPU),
			slog.String("reason", reason.String()))
	}

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
