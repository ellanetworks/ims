package server

import (
	"context"
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
	type ref struct{ reg, set, id int }

	var (
		refs []ref
		uris []sip.URI
	)

	for i, status := range page {
		for j, set := range status.ImplicitRegistrationSets {
			for k, id := range set.Identities {
				if u, err := sip.ParseURI(id.URI); err == nil && !id.Barred {
					refs = append(refs, ref{i, j, k})
					uris = append(uris, u)
				}
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
		page[r.reg].ImplicitRegistrationSets[r.set].Identities[r.id].RegisteredWith = with
	}

	return nil
}

// registrationStatus joins what the S-CSCF and the P-CSCF know of a private identity: each registration set the
// S-CSCF holds with an unexpired contact gives an implicit registration set, with its HSS, its public identities and
// those contacts, and the P-CSCF gives each contact's flow. A contact bound to several sets is listed in each, with
// that binding's times (TS 23.228 §5.2.1a.0).
func registrationStatus(impi string, regs []db.Registration, flows []db.PCSCFRegistration, now time.Time) api.RegistrationStatus {
	status := api.RegistrationStatus{IMPI: impi, ImplicitRegistrationSets: []api.ImplicitRegistrationSet{}}

	for _, reg := range regs {
		set := api.ImplicitRegistrationSet{
			HSS:        api.HSS{Host: reg.HSS.Host, Realm: reg.HSS.Realm},
			Identities: []api.RegisteredIdentity{},
			Contacts:   []api.RegisteredContact{},
		}

		for _, b := range reg.Bindings {
			if b.ExpiresAt.After(now) {
				set.Contacts = append(set.Contacts, contact(b, flows))
			}
		}

		if len(set.Contacts) == 0 {
			continue
		}

		for _, id := range reg.Identities {
			set.Identities = append(set.Identities, api.RegisteredIdentity{URI: id.URI, DisplayName: id.DisplayName, Barred: id.Barred})
		}

		status.ImplicitRegistrationSets = append(status.ImplicitRegistrationSets, set)
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

	if f, ok := flowOf(b.Contact, flows); ok {
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

// flowOf returns the P-CSCF's registration of a contact: the one whose IMS flow token is in the first URI of the
// contact's Path (TS 24.229 §5.2.2.1 step 1), or none when another P-CSCF registered it.
func flowOf(c db.Contact, flows []db.PCSCFRegistration) (db.PCSCFRegistration, bool) {
	hops, err := sip.ParseAddressList(c.Path)
	if err != nil || len(hops) == 0 || hops[0].URI.User == "" {
		return db.PCSCFRegistration{}, false
	}

	i := slices.IndexFunc(flows, func(f db.PCSCFRegistration) bool { return f.FlowToken == hops[0].URI.User })
	if i < 0 {
		return db.PCSCFRegistration{}, false
	}

	return flows[i], true
}
