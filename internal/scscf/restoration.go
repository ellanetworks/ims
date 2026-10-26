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

// The HSS based P-CSCF restoration (TS 23.380 §5.4, §5.8.4): a P-CSCF that lost the binding of a UE, as in a restart,
// answers a request toward it with a 404 (Not Found) (TS 24.229 §5.2.6.4.1). The S-CSCF removes the binding and, when
// the UE has no other, has the HSS release the UE's IMS PDN connection or PDU session with reactivation requested,
// through the MME, the SMF or the AMF: the UE connects again and registers anew (TS 23.380 §5.4.2.1 steps 3 to 8).
// The P-CSCF being next to the S-CSCF, the restoration is not started by a P-CSCF that does not answer (TS 24.229
// §5.4.3.3 b, c). IMS restoration does not apply to emergency registrations (TS 29.228 Annex G), which the IMS does
// not take.

// pcscfLost removes the binding of the registration that the P-CSCF has no binding for, in the background.
func (r *Registrar) pcscfLost(impi string, regID, bindingID int64) {
	r.background(impi, func(ctx context.Context) {
		r.send(r.restore(ctx, impi, regID, bindingID))
	})
}

// restore removes the binding, deactivated (RFC 3680 §5.1), and, the last of the UE's, starts the P-CSCF restoration,
// or else tells the HSS of the deregistration. The private identity is locked.
func (r *Registrar) restore(ctx context.Context, impi string, regID, bindingID int64) []*outgoing {
	st, err := r.load(ctx, impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
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

	// TS 23.380 §5.4.1: no restoration while the UE has other registration flows.
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
		r.log.Warn("failed to store the registration", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	switch {
	case !deleted:
	case !restoring:
		r.deregisterAtHSS(ctx, reg.HSS, impi, reg.IMPU, assignAdministrative)
	case t == cx.AssignmentUnregisteredUser:
		r.keepUnregistered(ctx, reg, hss)
	}

	r.log.Info("contact deregistered: the P-CSCF has no binding for it", slog.String("impi", impi),
		slog.String("impu", reg.IMPU), slog.String("contact", live[j].Contact.URI), slog.Bool("restoration", restoring))

	r.releaseCalls(st, []removal{rm})

	r.deregistered(rm)

	return r.notifyChange(ctx, impi, change{removed: []removal{rm}, reason: reasonDeactivated})
}

// restorePCSCF asks the HSS to start the P-CSCF restoration for the UE of the registration, if the HSS supports it
// (TS 29.229 Table 7.1.1): to unregister the public identity, or, registered by other private identities too, to
// deregister it of this one (TS 23.380 §5.4.2.1 step 4, TS 29.229 §6.3.15). It returns the HSS that answered, and the
// assignment type, and whether the HSS took it: one that does not, as none of the user's serving nodes supports the
// restoration, keeps the registration (TS 29.228 §6.1.2.1, TS 29.229 §6.2.2.12).
func (r *Registrar) restorePCSCF(ctx context.Context, st *state, reg db.Registration) (db.HSS, cx.AssignmentType, bool) {
	if cx.Features(reg.HSS.Features)&cx.FeaturePCSCFRestoration == 0 {
		r.log.Info("no P-CSCF restoration: the HSS does not support it", slog.String("impi", reg.IMPI),
			slog.String("impu", reg.IMPU), slog.String("hss", reg.HSS.Host))

		return db.HSS{}, 0, false
	}

	regs, err := r.cfg.DB.ListRegistrationsBySet(ctx, reg.SetID)
	if err != nil {
		r.log.Warn("failed to read the registrations of the set", slog.String("impi", reg.IMPI), slog.Any("error", err))
		return db.HSS{}, 0, false
	}

	t := cx.AssignmentUnregisteredUser
	if slices.ContainsFunc(regs, func(o db.Registration) bool { return o.IMPI != reg.IMPI && len(st.live(o.Bindings)) > 0 }) {
		t = assignAdministrative
	}

	// The request makes use of the feature: its Supported-Features has the M bit (TS 29.229 §7.2.1).
	_, from, err := r.serverAssignmentOf(ctx, reg.HSS, cx.ServerAssignmentRequest{
		PrivateIdentity: reg.IMPI, PublicIdentities: []string{reg.IMPU}, Type: t, UserDataAlreadyAvailable: true,
		PCSCFRestoration: true, FeaturesRequired: true,
	})
	if err != nil {
		r.log.Warn("the HSS did not start the P-CSCF restoration", slog.String("impi", reg.IMPI),
			slog.String("impu", reg.IMPU), slog.Any("error", err))

		return db.HSS{}, 0, false
	}

	r.log.Info("P-CSCF restoration started", slog.String("impi", reg.IMPI), slog.String("impu", reg.IMPU),
		slog.String("type", t.String()))

	return from, t, true
}

// keepUnregistered keeps the set of the registration, which the restoration took to unregistered, as a set kept
// unregistered, for the requests to its identities until the UE registers again: the SAA has no user data (TS 23.380
// §5.4.2.1 step 5, TS 29.228 §6.1.2.1).
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
		r.log.Warn("failed to keep the unregistered profile", slog.String("impu", reg.IMPU), slog.Any("error", err))
	}
}
