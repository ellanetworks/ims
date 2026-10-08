package server

import (
	"net/netip"
	"path/filepath"
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

// A q-value the registrar would now refuse, stored before it did, lists as none rather than as a number JSON
// cannot carry.
func TestContactQ(t *testing.T) {
	for params, want := range map[string]float64{``: 1, `;q=0.5`: 0.5, `;q=0`: 0, `;q=NaN`: 1, `;q=5`: 1, `;q=1e-1`: 1} {
		b := binding("sip:ue@192.0.2.9:5060", params, listNow, listNow.Add(time.Hour))

		if got := contact(b, nil).Q; got != want {
			t.Errorf("q of %q = %v, want %v", params, got, want)
		}
	}
}

// registered_with lists the other private identities a request to a public identity reaches, as the S-CSCF routes
// it: those with a live contact, and none at all when one of them bars the identity.
func TestRegisteredWith(t *testing.T) {
	d, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	save := func(impi, tel string, barred bool, expires time.Time) {
		t.Helper()

		_, err := d.SaveRegistration(t.Context(), db.Registration{
			IMPI: impi, IMPU: tel,
			Identities: []db.PublicIdentity{{URI: tel, Key: tel, Barred: barred}},
			Bindings: []db.Binding{{
				Contact: db.Contact{IMPI: impi, URI: "sip:" + impi + "@192.0.2.1"}, CallID: impi, CSeq: 1,
				ExpiresAt: expires, Event: db.BindingRegistered, IMPU: tel, RegisteredAt: listNow,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	live, gone := listNow.Add(time.Hour), listNow.Add(-time.Minute)

	save("phone", "tel:+15551230001", false, live)
	save("watch", "tel:+15551230001", false, live)
	save("stale", "tel:+15551230001", false, gone)
	save("desk", "tel:+15551230002", false, live)
	save("barring", "tel:+15551230002", true, live)

	page := []api.RegistrationStatus{
		{IMPI: "phone", Identities: []api.RegisteredIdentity{{URI: "tel:+15551230001"}, {URI: "sip:phone@" + listDomain, Barred: true}}},
		{IMPI: "desk", Identities: []api.RegisteredIdentity{{URI: "tel:+15551230002"}}},
	}

	if err := (coreView{&Server{database: d}}).registeredWith(t.Context(), page, listDomain, listNow); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		got  []string
		want []string
	}{
		{page[0].Identities[0].RegisteredWith, []string{"watch"}},
		{page[0].Identities[1].RegisteredWith, nil},
		{page[1].Identities[0].RegisteredWith, nil},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("registered with %v, want %v", tc.got, tc.want)
		}
	}
}
