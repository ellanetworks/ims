package scscf

import (
	"context"
	"log/slog"
	"slices"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/reginfo"
)

func (r *Registrar) pcscfLost(ctx context.Context, impi string, regID, bindingID int64) {
	r.background(ctx, impi, func(ctx context.Context) {
		r.send(ctx, r.restore(ctx, impi, regID, bindingID))
	})
}

// RFC 3680 section 5.1
func (r *Registrar) restore(ctx context.Context, impi string, regID, bindingID int64) []*outgoing {
	st, err := r.load(ctx, impi)
	if err != nil {
		r.log.WarnContext(ctx, "failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	i := slices.IndexFunc(st.regs, func(reg db.Registration) bool { return reg.ID == regID })
	if i < 0 {
		return nil
	}

	reg := st.regs[i]
	live := st.live(reg.Bindings)

	j := slices.IndexFunc(live, func(b db.Binding) bool { return b.ID == bindingID })
	if j < 0 {
		return nil
	}

	var (
		hss       db.HSS
		t         cx.AssignmentType
		restoring bool
	)

	if len(live) == 1 {
		hss, t, restoring = r.restorePCSCF(ctx, st, reg)
	}

	rm, deleted, err := r.removeBindings(ctx, st, reg, live[j:j+1], reginfo.Deactivated)
	if err != nil {
		r.log.WarnContext(ctx, "failed to store the registration", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	switch {
	case !deleted:
	case !restoring:
		r.deregisterAtHSS(ctx, reg.HSS, impi, reg.IMPU, assignAdministrative)
	case t == cx.AssignmentUnregisteredUser:
		r.keepUnregistered(ctx, reg, hss)
	}

	r.log.InfoContext(ctx, "contact deregistered: the P-CSCF has no binding for it", slog.String("impi", impi),
		slog.String("impu", reg.IMPU), slog.String("contact", live[j].Contact.URI), slog.Bool("restoration", restoring))

	r.releaseCalls(ctx, st, []removal{rm})

	r.deregistered(ctx, rm)

	return r.notifyChange(ctx, impi, change{removed: []removal{rm}, reason: reasonDeactivated})
}

// TS 29.229 clause 7.1.1 table 7.1.1
// TS 23.380 clause 5.4.2.1
// TS 29.229 clause 6.3.15
// TS 29.228 clause 6.1.2.1
// TS 29.229 clause 6.2.2.12
func (r *Registrar) restorePCSCF(ctx context.Context, st *state, reg db.Registration) (db.HSS, cx.AssignmentType, bool) {
	if cx.Features(reg.HSS.Features)&cx.FeaturePCSCFRestoration == 0 {
		r.log.InfoContext(ctx, "no P-CSCF restoration: the HSS does not support it", slog.String("impi", reg.IMPI),
			slog.String("impu", reg.IMPU), slog.String("hss", reg.HSS.Host))

		return db.HSS{}, 0, false
	}

	regs, err := r.cfg.DB.ListRegistrationsBySet(ctx, reg.SetID)
	if err != nil {
		r.log.WarnContext(ctx, "failed to read the registrations of the set", slog.String("impi", reg.IMPI), slog.Any("error", err))
		return db.HSS{}, 0, false
	}

	t := cx.AssignmentUnregisteredUser
	if slices.ContainsFunc(regs, func(o db.Registration) bool { return o.IMPI != reg.IMPI && len(st.live(o.Bindings)) > 0 }) {
		t = assignAdministrative
	}

	_, from, err := r.serverAssignmentOf(ctx, reg.HSS, cx.ServerAssignmentRequest{
		PrivateIdentity: reg.IMPI, PublicIdentities: []string{reg.IMPU}, Type: t, UserDataAlreadyAvailable: true,
		PCSCFRestoration: true, FeaturesRequired: true,
	})
	if err != nil {
		r.log.WarnContext(ctx, "the HSS did not start the P-CSCF restoration", slog.String("impi", reg.IMPI),
			slog.String("impu", reg.IMPU), slog.Any("error", err))

		return db.HSS{}, 0, false
	}

	r.log.InfoContext(ctx, "P-CSCF restoration started", slog.String("impi", reg.IMPI), slog.String("impu", reg.IMPU),
		slog.String("type", t.String()))

	return from, t, true
}

// TS 23.380 clause 5.4.2.1
// TS 29.228 clause 6.1.2.1
func (r *Registrar) keepUnregistered(ctx context.Context, reg db.Registration, hss db.HSS) {
	u, err := sip.ParseURI(reg.IMPU)
	if err != nil {
		return
	}

	if err := r.lockProfiles(ctx); err != nil {
		return
	}

	defer r.unlockProfiles()

	if _, err := r.cfg.DB.SaveSet(ctx, db.ImplicitSet{
		Identities: reg.Identities, UserData: reg.UserData, HSS: hss,
		Unregistered: &db.Unregistered{Identity: canonicalIdentity(u), ExpiresAt: r.clock.Now().Add(r.cfg.MaxExpires)},
	}); err != nil {
		r.log.WarnContext(ctx, "failed to keep the unregistered profile", slog.String("impu", reg.IMPU), slog.Any("error", err))
	}
}
