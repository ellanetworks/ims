package server

import (
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/db"
)

const (
	listIMPI   = "001010000000001@ims.mnc001.mcc001.3gppnetwork.org"
	listDomain = "ims.mnc001.mcc001.3gppnetwork.org"
)

var listNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func binding(contact, params string, registeredAt, expiresAt time.Time) db.Binding {
	return db.Binding{
		Contact:      db.Contact{URI: contact, Params: params},
		RegisteredAt: registeredAt,
		ExpiresAt:    expiresAt,
	}
}

func TestRegistrationStatus(t *testing.T) {
	const (
		phone  = "sip:001010000000001@[2001:db8::1]:5064"
		tablet = "sip:001010000000001@192.0.2.7:5060"
		gone   = "sip:001010000000001@192.0.2.8:5060"
	)

	regs := []db.Registration{{
		IMPI: listIMPI,
		Identities: []db.PublicIdentity{
			{URI: "sip:" + listIMPI, Barred: true},
			{URI: "tel:+15551230001", DisplayName: "Alice"},
		},
		Bindings: []db.Binding{
			binding(phone, `;+sip.instance="<urn:gsma:imei:35000000-000001-0>";audio;video;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`,
				listNow.Add(-time.Hour), listNow.Add(time.Hour)),
			binding(gone, ``, listNow.Add(-2*time.Hour), listNow),
		},
	}, {
		// A second registration set: its identities add to the first, and its bindings of the same contact are
		// listed once.
		IMPI: listIMPI,
		Identities: []db.PublicIdentity{
			{URI: "tel:+15551230001", DisplayName: "Alice"},
			{URI: "sip:alice@" + listDomain},
		},
		Bindings: []db.Binding{
			binding(phone, `;audio`, listNow.Add(-2*time.Hour), listNow.Add(30*time.Minute)),
			binding(tablet, `;+sip.instance="<urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6>";q=0.5;audio`,
				listNow, listNow.Add(time.Hour)),
		},
	}}

	flows := []db.PCSCFRegistration{{
		IMPI:      listIMPI,
		Transport: "UDP",
		Protected: true,
		UEAddress: netip.MustParseAddrPort("[2001:db8::1]:5064"),
		Contacts:  []string{phone},
		Policy:    db.PolicySession{ID: "pcscf;1;2"},
	}, {
		// Matched by the address in the contact, not by its list of contacts.
		IMPI:           listIMPI,
		Transport:      "TCP",
		UEAddress:      netip.MustParseAddrPort("192.0.2.7:40000"),
		Policy:         db.PolicySession{ID: "pcscf;1;3"},
		SignallingLost: true,
	}}

	got := registrationStatus(listIMPI, regs, flows, listNow)

	want := api.RegistrationStatus{
		IMPI: listIMPI,
		Identities: []api.RegisteredIdentity{
			{URI: "sip:" + listIMPI, Barred: true},
			{URI: "tel:+15551230001", DisplayName: "Alice"},
			{URI: "sip:alice@" + listDomain},
		},
		Contacts: []api.RegisteredContact{{
			Contact:        phone,
			Instance:       "urn:gsma:imei:35000000-000001-0",
			Q:              1,
			Media:          []string{"audio", "video"},
			RegisteredAt:   listNow.Add(-2 * time.Hour),
			ExpiresAt:      listNow.Add(time.Hour),
			Address:        "[2001:db8::1]:5064",
			Transport:      "udp",
			Protected:      true,
			SignallingPath: api.SignallingPathMonitored,
		}, {
			Contact:        tablet,
			Instance:       "urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6",
			Q:              0.5,
			Media:          []string{"audio"},
			RegisteredAt:   listNow,
			ExpiresAt:      listNow.Add(time.Hour),
			Address:        "192.0.2.7:40000",
			Transport:      "tcp",
			SignallingPath: api.SignallingPathLost,
		}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestRegistrationStatusWithoutFlow(t *testing.T) {
	regs := []db.Registration{{
		IMPI:     listIMPI,
		Bindings: []db.Binding{binding("sip:ue@192.0.2.9:5060", `;not a param`, listNow, listNow.Add(time.Hour))},
	}}

	got := registrationStatus(listIMPI, regs, nil, listNow)

	want := api.RegistrationStatus{
		IMPI:       listIMPI,
		Identities: []api.RegisteredIdentity{},
		Contacts: []api.RegisteredContact{{
			Contact:        "sip:ue@192.0.2.9:5060",
			Q:              1,
			RegisteredAt:   listNow,
			ExpiresAt:      listNow.Add(time.Hour),
			SignallingPath: api.SignallingPathUnmonitored,
		}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}
