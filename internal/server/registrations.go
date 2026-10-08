package server

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/scscf"
	"github.com/ellanetworks/ims/sip"
)

// ListRegistrations reads the registrations from the database, which outlives a restart of the core.
func (v coreView) ListRegistrations(ctx context.Context, search string, page, perPage int) ([]api.RegistrationStatus, int, error) {
	now := time.Now()

	impis, total, err := v.s.database.ListRegisteredIMPIs(ctx, search, now, page, perPage)
	if err != nil {
		return nil, 0, err
	}

	homeDomain := v.s.settings.Get().Operator.HomeDomain()
	out := make([]api.RegistrationStatus, 0, len(impis))

	for _, impi := range impis {
		regs, err := v.s.database.ListRegistrationsByIMPI(ctx, impi)
		if err != nil {
			return nil, 0, err
		}

		flows, err := v.s.database.ListPCSCFRegistrationsByIMPI(ctx, impi)
		if err != nil {
			return nil, 0, err
		}

		out = append(out, registrationStatus(impi, regs, flows, now))
	}

	if err := v.registeredWith(ctx, out, homeDomain, now); err != nil {
		return nil, 0, err
	}

	return out, total, nil
}

// registeredWith fills in, for each unbarred public identity of the page, the other private identities a request to
// it reaches: those whose registrations hold it, with a live contact, as the S-CSCF routes it (TS 24.229 §5.4.3.3
// step 8). The whole page takes one read of the database.
func (v coreView) registeredWith(ctx context.Context, page []api.RegistrationStatus, homeDomain string, now time.Time) error {
	type ref struct{ reg, id int }

	var (
		refs []ref
		uris []sip.URI
	)

	for i, status := range page {
		for j, id := range status.Identities {
			if u, err := sip.ParseURI(id.URI); err == nil && !id.Barred {
				refs = append(refs, ref{i, j})
				uris = append(uris, u)
			}
		}
	}

	if len(uris) == 0 {
		return nil
	}

	reach, err := scscf.RecipientsOf(ctx, v.s.database, uris, homeDomain)
	if err != nil {
		return err
	}

	for k, r := range refs {
		impi := page[r.reg].IMPI

		var with []string

		for _, reg := range reach[k].Registrations {
			if reg.IMPI != impi && !slices.Contains(with, reg.IMPI) &&
				slices.ContainsFunc(reg.Bindings, func(b db.Binding) bool { return b.ExpiresAt.After(now) }) {
				with = append(with, reg.IMPI)
			}
		}

		slices.Sort(with)
		page[r.reg].Identities[r.id].RegisteredWith = with
	}

	return nil
}

// registrationStatus joins what the S-CSCF and the P-CSCF know of a private identity: the S-CSCF's registration
// sets give its public identities and its unexpired contacts, and the P-CSCF gives each contact's flow.
func registrationStatus(impi string, regs []db.Registration, flows []db.PCSCFRegistration, now time.Time) api.RegistrationStatus {
	status := api.RegistrationStatus{IMPI: impi, Identities: []api.RegisteredIdentity{}, Contacts: []api.RegisteredContact{}}
	listed := map[int64]int{}

	for _, reg := range regs {
		for _, id := range reg.Identities {
			if !slices.ContainsFunc(status.Identities, func(i api.RegisteredIdentity) bool { return i.URI == id.URI }) {
				status.Identities = append(status.Identities, api.RegisteredIdentity{URI: id.URI, DisplayName: id.DisplayName, Barred: id.Barred})
			}
		}

		for _, b := range reg.Bindings {
			if !b.ExpiresAt.After(now) {
				continue
			}

			// A contact registered for several registration sets is listed once.
			if i, ok := listed[b.Contact.ID]; ok {
				d := &status.Contacts[i]
				d.RegisteredAt = minTime(d.RegisteredAt, b.RegisteredAt)
				d.ExpiresAt = maxTime(d.ExpiresAt, b.ExpiresAt)

				continue
			}

			listed[b.Contact.ID] = len(status.Contacts)
			status.Contacts = append(status.Contacts, contact(b, flows))
		}
	}

	return status
}

func contact(b db.Binding, flows []db.PCSCFRegistration) api.RegisteredContact {
	d := api.RegisteredContact{
		Contact:        b.Contact.URI,
		Instance:       b.Contact.Instance,
		RegID:          b.Contact.RegID,
		Q:              1,
		RegisteredAt:   b.RegisteredAt,
		ExpiresAt:      b.ExpiresAt,
		SignallingPath: api.SignallingPathUnmonitored,
	}

	if params, err := sip.ParseParams(b.Contact.Params); err == nil {
		d.Q = scscf.QValue(params)

		// RFC 3840 §9: the media feature tags the contact registered for.
		for _, tag := range []string{"audio", "video"} {
			if params.Has(tag) {
				d.Media = append(d.Media, tag)
			}
		}
	}

	if f, ok := flowOf(b.Contact.URI, flows); ok {
		d.Address = f.UEAddress.String()
		d.Transport = strings.ToLower(f.Transport)
		d.Protected = f.Protected

		switch {
		case f.SignallingLost:
			d.SignallingPath = api.SignallingPathLost
		case f.Policy.ID != "":
			d.SignallingPath = api.SignallingPathMonitored
		}
	}

	return d
}

// flowOf returns the P-CSCF's registration of a contact: the one that lists it, or else the one from the UE address
// in the contact's host.
func flowOf(contact string, flows []db.PCSCFRegistration) (db.PCSCFRegistration, bool) {
	for _, f := range flows {
		if slices.Contains(f.Contacts, contact) {
			return f, true
		}
	}

	u, err := sip.ParseURI(contact)
	if err != nil {
		return db.PCSCFRegistration{}, false
	}

	host, err := netip.ParseAddr(strings.Trim(u.Host, "[]"))
	if err != nil {
		return db.PCSCFRegistration{}, false
	}

	for _, f := range flows {
		if f.UEAddress.Addr() == host.Unmap() {
			return f, true
		}
	}

	return db.PCSCFRegistration{}, false
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}

	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}

	return a
}
