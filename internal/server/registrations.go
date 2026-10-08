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

		status := registrationStatus(impi, regs, flows, now)

		for i := range status.Identities {
			id := &status.Identities[i]
			if id.Barred {
				continue
			}

			if id.RegisteredWith, err = v.registeredWith(ctx, impi, id.URI, homeDomain, now); err != nil {
				return nil, 0, err
			}
		}

		out = append(out, status)
	}

	return out, total, nil
}

// registeredWith returns the other private identities a request to the public identity reaches: those whose
// registrations hold it, with a live contact, as the S-CSCF routes it (TS 24.229 §5.4.3.3 step 8).
func (v coreView) registeredWith(ctx context.Context, impi, uri, homeDomain string, now time.Time) ([]string, error) {
	u, err := sip.ParseURI(uri)
	if err != nil {
		return nil, nil
	}

	regs, barred, err := scscf.Recipients(ctx, v.s.database, u, homeDomain)
	if err != nil || barred {
		return nil, err
	}

	var out []string

	for _, reg := range regs {
		if reg.IMPI != impi && !slices.Contains(out, reg.IMPI) && slices.ContainsFunc(reg.Bindings, func(b db.Binding) bool { return b.ExpiresAt.After(now) }) {
			out = append(out, reg.IMPI)
		}
	}

	slices.Sort(out)

	return out, nil
}

// registrationStatus joins what the S-CSCF and the P-CSCF know of a private identity: the S-CSCF's registration
// sets give its public identities and its unexpired contacts, and the P-CSCF gives each contact's flow.
func registrationStatus(impi string, regs []db.Registration, flows []db.PCSCFRegistration, now time.Time) api.RegistrationStatus {
	status := api.RegistrationStatus{IMPI: impi, Identities: []api.RegisteredIdentity{}, Contacts: []api.RegisteredContact{}}

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
			i := slices.IndexFunc(status.Contacts, func(c api.RegisteredContact) bool { return c.Contact == b.Contact.URI })
			if i >= 0 {
				d := &status.Contacts[i]
				d.RegisteredAt = minTime(d.RegisteredAt, b.RegisteredAt)
				d.ExpiresAt = maxTime(d.ExpiresAt, b.ExpiresAt)

				continue
			}

			status.Contacts = append(status.Contacts, contact(b, flows))
		}
	}

	return status
}

func contact(b db.Binding, flows []db.PCSCFRegistration) api.RegisteredContact {
	d := api.RegisteredContact{
		Contact:        b.Contact.URI,
		Q:              1,
		RegisteredAt:   b.RegisteredAt,
		ExpiresAt:      b.ExpiresAt,
		SignallingPath: api.SignallingPathUnmonitored,
	}

	if params, err := sip.ParseParams(b.Contact.Params); err == nil {
		d.Q = scscf.QValue(params)

		if v, ok := params.Get("+sip.instance"); ok {
			d.Instance = strings.TrimSuffix(strings.TrimPrefix(sip.Unquote(v), "<"), ">")
		}

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
