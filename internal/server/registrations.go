package server

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
)

// ListRegistrations reads the registrations from the database, which outlives a restart of the core.
func (v coreView) ListRegistrations(ctx context.Context, search string, page, perPage int) ([]api.RegistrationStatus, int, error) {
	now := time.Now()

	impis, total, err := v.s.database.ListRegisteredIMPIs(ctx, search, now, page, perPage)
	if err != nil {
		return nil, 0, err
	}

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

	return out, total, nil
}

// registrationStatus joins what the S-CSCF and the P-CSCF know of a private identity: the S-CSCF's registration
// sets give its public identities and its unexpired contacts, one device each, and the P-CSCF gives each device's
// flow.
func registrationStatus(impi string, regs []db.Registration, flows []db.PCSCFRegistration, now time.Time) api.RegistrationStatus {
	status := api.RegistrationStatus{IMPI: impi, Identities: []api.RegisteredIdentity{}, Devices: []api.RegisteredDevice{}}

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

			// A contact registered for several registration sets is one device.
			i := slices.IndexFunc(status.Devices, func(d api.RegisteredDevice) bool { return d.Contact == b.Contact.URI })
			if i >= 0 {
				d := &status.Devices[i]
				d.RegisteredAt = minTime(d.RegisteredAt, b.RegisteredAt)
				d.ExpiresAt = maxTime(d.ExpiresAt, b.ExpiresAt)

				continue
			}

			status.Devices = append(status.Devices, device(b, flows))
		}
	}

	return status
}

func device(b db.Binding, flows []db.PCSCFRegistration) api.RegisteredDevice {
	d := api.RegisteredDevice{
		Contact:        b.Contact.URI,
		RegisteredAt:   b.RegisteredAt,
		ExpiresAt:      b.ExpiresAt,
		SignallingPath: api.SignallingPathUnmonitored,
	}

	if params, err := sip.ParseParams(b.Contact.Params); err == nil {
		if v, ok := params.Get("+sip.instance"); ok {
			d.Instance = strings.TrimSuffix(strings.TrimPrefix(sip.Unquote(v), "<"), ">")
		}

		// RFC 3840 §9: the media feature tags the device registered for.
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
