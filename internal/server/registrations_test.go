package server

import (
	"net/netip"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
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

func binding(c db.Contact, registeredAt, expiresAt time.Time) db.Binding {
	return db.Binding{Contact: c, RegisteredAt: registeredAt, ExpiresAt: expiresAt}
}

func TestRegistrationStatus(t *testing.T) {
	const (
		phone  = "sip:001010000000001@[2001:db8::1]:5064"
		tablet = "sip:001010000000001@192.0.2.7:5060"
		gone   = "sip:001010000000001@192.0.2.8:5060"

		tabletInstance = "urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6"
	)

	phoneContact := db.Contact{
		URI: phone, Instance: "urn:gsma:imei:35000000-000001-0", Path: "<sip:token1@pcscf." + listDomain + ";lr;ob>",
		Params: `;+sip.instance="<urn:gsma:imei:35000000-000001-0>";audio;video;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`,
	}

	regs := []db.Registration{{
		IMPI: listIMPI,
		Identities: []db.PublicIdentity{
			{URI: "sip:" + listIMPI, Barred: true},
			{URI: "tel:+15551230001", DisplayName: "Alice"},
		},
		HSS: db.HSS{Host: "hss1." + listDomain, Realm: listDomain},
		Bindings: []db.Binding{
			binding(phoneContact, listNow.Add(-time.Hour), listNow.Add(time.Hour)),
			binding(db.Contact{URI: gone}, listNow.Add(-2*time.Hour), listNow),
		},
	}, {
		// A second implicit registration set, bound to another HSS node: the phone's binding to it is its own.
		IMPI: listIMPI,
		Identities: []db.PublicIdentity{
			{URI: "sip:alice@" + listDomain},
		},
		HSS: db.HSS{Host: "hss2." + listDomain, Realm: listDomain},
		Bindings: []db.Binding{
			binding(phoneContact, listNow.Add(-2*time.Hour), listNow.Add(30*time.Minute)),
			binding(db.Contact{
				URI: tablet, Instance: tabletInstance, RegID: 1, Path: "<sip:token2@pcscf." + listDomain + ";lr;ob>",
				Params: `;+sip.instance="<` + tabletInstance + `>";reg-id=1;q=0.5;audio`,
			}, listNow, listNow.Add(time.Hour)),
		},
	}}

	flows := []db.PCSCFRegistration{{
		IMPI:      listIMPI,
		FlowToken: "token1",
		Transport: "UDP",
		Protected: true,
		UEAddress: netip.MustParseAddrPort("[2001:db8::1]:5064"),
		Contacts:  []string{phone},
		Policy:    db.PolicySession{ID: "pcscf;1;2"},
	}, {
		// Matched by the flow token in the contact's Path, not by its list of contacts.
		IMPI:           listIMPI,
		FlowToken:      "token2",
		Instance:       tabletInstance,
		RegID:          1,
		Transport:      "TCP",
		UEAddress:      netip.MustParseAddrPort("192.0.2.7:40000"),
		Policy:         db.PolicySession{ID: "pcscf;1;3"},
		SignallingLost: true,
	}}

	got := registrationStatus(listIMPI, regs, flows, listNow)

	phoneStatus := func(registeredAt, expiresAt time.Time) api.RegisteredContact {
		return api.RegisteredContact{
			Contact:        phone,
			Instance:       "urn:gsma:imei:35000000-000001-0",
			Q:              1,
			Media:          []string{"audio", "video"},
			RegisteredAt:   registeredAt,
			ExpiresAt:      expiresAt,
			Address:        "[2001:db8::1]:5064",
			Transport:      "udp",
			Protected:      true,
			SignallingPath: api.SignallingPathMonitored,
		}
	}

	want := api.RegistrationStatus{
		IMPI: listIMPI,
		ImplicitRegistrationSets: []api.ImplicitRegistrationSet{{
			HSS: api.HSS{Host: "hss1." + listDomain, Realm: listDomain},
			Identities: []api.RegisteredIdentity{
				{URI: "sip:" + listIMPI, Barred: true},
				{URI: "tel:+15551230001", DisplayName: "Alice"},
			},
			Contacts: []api.RegisteredContact{phoneStatus(listNow.Add(-time.Hour), listNow.Add(time.Hour))},
		}, {
			HSS:        api.HSS{Host: "hss2." + listDomain, Realm: listDomain},
			Identities: []api.RegisteredIdentity{{URI: "sip:alice@" + listDomain}},
			Contacts: []api.RegisteredContact{phoneStatus(listNow.Add(-2*time.Hour), listNow.Add(30*time.Minute)), {
				Contact:        tablet,
				Instance:       tabletInstance,
				RegID:          1,
				Q:              0.5,
				Media:          []string{"audio"},
				RegisteredAt:   listNow,
				ExpiresAt:      listNow.Add(time.Hour),
				Address:        "192.0.2.7:40000",
				Transport:      "tcp",
				SignallingPath: api.SignallingPathLost,
			}},
		}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// A set whose contacts have all expired is no longer registered (TS 23.228 §5.2.1a.0).
func TestRegistrationStatusSkipsExpiredSets(t *testing.T) {
	regs := []db.Registration{{
		IMPI:       listIMPI,
		Identities: []db.PublicIdentity{{URI: "tel:+15551230001"}},
		Bindings:   []db.Binding{binding(db.Contact{URI: "sip:ue@192.0.2.9:5060"}, listNow.Add(-time.Hour), listNow)},
	}, {
		IMPI:       listIMPI,
		Identities: []db.PublicIdentity{{URI: "tel:+15551230002"}},
		Bindings:   []db.Binding{binding(db.Contact{URI: "sip:ue@192.0.2.9:5060"}, listNow, listNow.Add(time.Hour))},
	}}

	got := registrationStatus(listIMPI, regs, nil, listNow)

	if len(got.ImplicitRegistrationSets) != 1 || got.ImplicitRegistrationSets[0].Identities[0].URI != "tel:+15551230002" {
		t.Fatalf("sets = %+v, want only the live one", got.ImplicitRegistrationSets)
	}
}

func TestRegistrationStatusWithoutFlow(t *testing.T) {
	regs := []db.Registration{{
		IMPI:     listIMPI,
		Bindings: []db.Binding{binding(db.Contact{URI: "sip:ue@192.0.2.9:5060", Params: `;not a param`}, listNow, listNow.Add(time.Hour))},
	}}

	got := registrationStatus(listIMPI, regs, nil, listNow)

	want := api.RegistrationStatus{
		IMPI: listIMPI,
		ImplicitRegistrationSets: []api.ImplicitRegistrationSet{{
			Identities: []api.RegisteredIdentity{},
			Contacts: []api.RegisteredContact{{
				Contact:        "sip:ue@192.0.2.9:5060",
				Q:              1,
				RegisteredAt:   listNow,
				ExpiresAt:      listNow.Add(time.Hour),
				SignallingPath: api.SignallingPathUnmonitored,
			}},
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
		b := binding(db.Contact{URI: "sip:ue@192.0.2.9:5060", Params: params}, listNow, listNow.Add(time.Hour))

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
				Contact: db.Contact{URI: "sip:" + impi + "@192.0.2.1"}, CallID: impi, CSeq: 1,
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

	set := func(ids ...api.RegisteredIdentity) []api.ImplicitRegistrationSet {
		return []api.ImplicitRegistrationSet{{Identities: ids}}
	}

	page := []api.RegistrationStatus{
		{IMPI: "phone", ImplicitRegistrationSets: set(api.RegisteredIdentity{URI: "tel:+15551230001"}, api.RegisteredIdentity{URI: "sip:phone@" + listDomain, Barred: true})},
		{IMPI: "desk", ImplicitRegistrationSets: set(api.RegisteredIdentity{URI: "tel:+15551230002"})},
	}

	if err := (coreView{&Server{database: d}}).registeredWith(t.Context(), page, listDomain, listNow); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		got  []string
		want []string
	}{
		{page[0].ImplicitRegistrationSets[0].Identities[0].RegisteredWith, []string{"watch"}},
		{page[0].ImplicitRegistrationSets[0].Identities[1].RegisteredWith, nil},
		{page[1].ImplicitRegistrationSets[0].Identities[0].RegisteredWith, nil},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("registered with %v, want %v", tc.got, tc.want)
		}
	}
}

// Flows sharing a contact URI each show the P-CSCF's registration of their own flow (RFC 5626), found by the flow
// token in their Path: after a flow is replaced from another address, the P-CSCF still has the old flow's
// registration until it learns of the replacement.
func TestRegistrationStatusFlows(t *testing.T) {
	const (
		uri      = "sip:001010000000001@192.0.2.7:5060"
		instance = "urn:gsma:imei:35000000-000001-0"
	)

	flow := func(regID int64, token string) db.Binding {
		c := db.Contact{URI: uri, Instance: instance, RegID: regID, Path: "<sip:" + token + "@pcscf." + listDomain + ";lr;ob>"}
		return binding(c, listNow, listNow.Add(time.Hour))
	}

	regs := []db.Registration{{IMPI: listIMPI, Bindings: []db.Binding{flow(1, "new1"), flow(2, "t2")}}}

	pcscf := func(regID int64, token, addr string) db.PCSCFRegistration {
		return db.PCSCFRegistration{
			IMPI: listIMPI, Instance: instance, RegID: regID, FlowToken: token, Transport: "UDP", UEAddress: netip.MustParseAddrPort(addr),
		}
	}

	flows := []db.PCSCFRegistration{pcscf(1, "old1", "192.0.2.7:40000"), pcscf(2, "t2", "192.0.2.7:40002"), pcscf(1, "new1", "192.0.2.9:40001")}

	got := registrationStatus(listIMPI, regs, flows, listNow)

	var addresses []string
	for _, c := range got.ImplicitRegistrationSets[0].Contacts {
		addresses = append(addresses, strconv.FormatInt(c.RegID, 10)+" "+c.Address)
	}

	if want := []string{"1 192.0.2.9:40001", "2 192.0.2.7:40002"}; !slices.Equal(addresses, want) {
		t.Fatalf("contacts %v, want %v", addresses, want)
	}
}
