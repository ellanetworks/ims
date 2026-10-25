package db

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

const (
	testIMPI   = "001010000000001@ims.mnc001.mcc001.3gppnetwork.org"
	testDomain = "ims.mnc001.mcc001.3gppnetwork.org"
)

func identity(uri string, barred bool) PublicIdentity {
	return PublicIdentity{URI: uri, Key: uri, Barred: barred}
}

const testInstance = "urn:gsma:imei:35000000-000000-0"

func testContact(host string) Contact {
	return Contact{
		URI:      "sip:ue@[" + host + "]:5100",
		Instance: testInstance,
		Params:   `;+sip.instance="<` + testInstance + `>";+g.3gpp.smsip`,
		Path:     "<sip:term@pcscf." + testDomain + ";lr>",
	}
}

func testFlow(host string, regID int64) Contact {
	c := testContact(host)
	c.RegID = regID
	c.Path = "<sip:flow" + strconv.FormatInt(regID, 10) + "@pcscf." + testDomain + ";lr;ob>"

	return c
}

func testRegistration(impi, msisdn string) Registration {
	msisdnURI := "sip:+" + msisdn + "@" + testDomain + ";user=phone"

	return Registration{
		IMPI: impi,
		IMPU: "sip:" + impi,
		Identities: []PublicIdentity{
			identity("sip:"+impi, true),
			{URI: msisdnURI, Key: msisdnURI, DisplayName: "Alice"},
			identity("tel:+"+msisdn, false),
		},
		UserData: []byte("<IMSSubscription/>"),
		HSS:      HSS{Host: "hss1." + testDomain, Realm: testDomain},
		Bindings: []Binding{{
			Contact:      testContact("2001:db8::1"),
			CallID:       "reg-" + msisdn,
			CSeq:         1,
			ExpiresAt:    testNow.Add(time.Hour),
			Event:        BindingRegistered,
			IMPU:         msisdnURI,
			RegisteredAt: testNow,
		}},
	}
}

func mustSaveRegistration(t *testing.T, d *DB, r Registration) Registration {
	t.Helper()

	saved, err := d.SaveRegistration(context.Background(), r)
	if err != nil {
		t.Fatalf("SaveRegistration: %v", err)
	}

	return saved
}

func listByIMPI(t *testing.T, d *DB) []Registration {
	t.Helper()

	regs, err := d.ListRegistrationsByIMPI(context.Background(), testIMPI)
	if err != nil {
		t.Fatalf("ListRegistrationsByIMPI: %v", err)
	}

	return regs
}

func TestRegistrationRoundTrip(t *testing.T) {
	d := openTestDB(t)

	want := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))
	if want.ID == 0 || want.Bindings[0].ID == 0 {
		t.Fatalf("saved = %+v, want IDs set", want)
	}

	if got := listByIMPI(t, d); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("registrations = %+v, want %+v", got, want)
	}
}

func TestSaveRegistrationUpdatesTheContactPath(t *testing.T) {
	d := openTestDB(t)

	stale := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))
	stale.Bindings[0].Contact.Path = "<sip:term@pcscf2." + testDomain + ";lr>"

	saved := mustSaveRegistration(t, d, stale)

	if c := saved.Bindings[0].Contact; c.Path != stale.Bindings[0].Contact.Path {
		t.Fatalf("Path = %q, want the S-CSCF's update", c.Path)
	}

	if got := listByIMPI(t, d); len(got) != 1 || !reflect.DeepEqual(got[0], saved) {
		t.Fatalf("registrations = %+v, want %+v", got, saved)
	}
}

func TestSaveRegistrationUpdates(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	r := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))
	bindingID := r.Bindings[0].ID

	set, err := d.GetSet(ctx, r.SetID)
	if err != nil {
		t.Fatalf("GetSet: %v", err)
	}

	set.Identities = set.Identities[1:]
	set.UserData = []byte("<IMSSubscription>2</IMSSubscription>")
	set.HSS = HSS{Host: "hss2." + testDomain, Realm: testDomain}

	if _, err := d.SaveSet(ctx, set); err != nil {
		t.Fatalf("SaveSet: %v", err)
	}

	r.IMPU = "tel:+15551230001"
	r.Identities, r.UserData, r.HSS = set.Identities, set.UserData, set.HSS
	r.Bindings[0].CSeq = 2
	r.Bindings[0].ExpiresAt = testNow.Add(2 * time.Hour)
	r.Bindings[0].Contact.Path = "<sip:term@pcscf2." + testDomain + ";lr>"
	r.Bindings[0].ID = 0

	saved := mustSaveRegistration(t, d, r)
	if saved.Bindings[0].ID != bindingID {
		t.Fatalf("binding ID = %d, want %d: the same URI is the same binding", saved.Bindings[0].ID, bindingID)
	}

	if got := listByIMPI(t, d); len(got) != 1 || !reflect.DeepEqual(got[0], saved) {
		t.Fatalf("registrations = %+v, want %+v", got, saved)
	}
}

// A registration with a set leaves the set's profile: only SaveSet changes it, whoever registered the set.
func TestSaveRegistrationLeavesTheSet(t *testing.T) {
	d := openTestDB(t)

	r := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))
	want := r.Identities

	r.Identities, r.UserData = nil, []byte("<IMSSubscription>stale</IMSSubscription>")
	mustSaveRegistration(t, d, r)

	if got := listByIMPI(t, d); !reflect.DeepEqual(got[0].Identities, want) || string(got[0].UserData) != "<IMSSubscription/>" {
		t.Fatalf("registration = %+v, want the set unchanged", got[0])
	}
}

// TS 24.229 §5.4.1.2.2 step 6d, RFC 5626 §6: each registration set has its own binding to a contact, with
// its own Path, so that one set's change leaves the other's.
func TestSetsBindAContactApart(t *testing.T) {
	d := openTestDB(t)

	a := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	b := testRegistration(testIMPI, "15551230002")
	b.IMPU = "sip:second@" + testDomain
	b.Identities = []PublicIdentity{identity(b.IMPU, false)}
	b = mustSaveRegistration(t, d, b)

	if a.Bindings[0].ID == b.Bindings[0].ID {
		t.Fatalf("binding %d shared by both sets", a.Bindings[0].ID)
	}

	a.Bindings[0].Contact.Path = "<sip:other@pcscf." + testDomain + ";lr>"
	mustSaveRegistration(t, d, a)

	regs := listByIMPI(t, d)
	if len(regs) != 2 || regs[1].Bindings[0].Contact.Path != b.Bindings[0].Contact.Path {
		t.Fatalf("registrations = %+v, want the second set's Path kept", regs)
	}

	a.Bindings = nil
	mustSaveRegistration(t, d, a)

	if regs := listByIMPI(t, d); len(regs[0].Bindings) != 0 || len(regs[1].Bindings) != 1 {
		t.Fatalf("registrations = %+v", regs)
	}
}

// TS 23.228 §4.3.3.4: the private identities that share a public identity register the one set that holds it.
func TestSharedIdentity(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	phone := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	tablet := testRegistration("tablet@"+testDomain, "15551230001")
	tablet.Bindings[0].Contact = testContact("2001:db8::2")
	tablet = mustSaveRegistration(t, d, tablet)

	if tablet.SetID != phone.SetID {
		t.Fatalf("sets %d and %d, want the one set", phone.SetID, tablet.SetID)
	}

	regs, err := d.ListRegistrationsByIdentity(ctx, "tel:+15551230001")
	if err != nil {
		t.Fatalf("ListRegistrationsByIdentity: %v", err)
	}

	if len(regs) != 2 || regs[0].ID != phone.ID || regs[1].ID != tablet.ID {
		t.Fatalf("registrations = %+v, want the phone's and the tablet's", regs)
	}

	if regs, err := d.ListRegistrationsBySet(ctx, phone.SetID); err != nil || len(regs) != 2 {
		t.Fatalf("ListRegistrationsBySet = %+v, %v; want both", regs, err)
	}

	if err := d.DeleteRegistration(ctx, phone.ID); err != nil {
		t.Fatalf("DeleteRegistration: %v", err)
	}

	if _, err := d.GetSet(ctx, phone.SetID); err != nil {
		t.Fatalf("GetSet = %v, want the set kept for the tablet", err)
	}

	if err := d.DeleteRegistration(ctx, tablet.ID); err != nil {
		t.Fatalf("DeleteRegistration: %v", err)
	}

	if _, err := d.GetSet(ctx, phone.SetID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSet = %v, want the set gone with its last registration", err)
	}
}

func TestIdentityConflict(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	for _, uri := range []string{"tel:+15551230001", "sip:second@" + testDomain} {
		if _, err := d.SaveSet(ctx, ImplicitSet{Identities: []PublicIdentity{identity(uri, false)}}); err != nil {
			t.Fatalf("SaveSet: %v", err)
		}
	}

	r := testRegistration(testIMPI, "15551230001")
	r.Identities = []PublicIdentity{identity("sip:second@"+testDomain, false), identity("tel:+15551230001", false)}

	if _, err := d.SaveRegistration(ctx, r); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("SaveRegistration err = %v, want ErrIdentityConflict", err)
	}

	if got := listByIMPI(t, d); len(got) != 0 {
		t.Fatalf("registrations = %+v, want none", got)
	}
}

// TS 29.228 §6.5.2.1: an identity the HSS moves to another set leaves the first.
func TestSaveSetMovesIdentities(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	a, err := d.SaveSet(ctx, ImplicitSet{Identities: []PublicIdentity{identity("sip:a@x", false), identity("sip:b@x", false)}})
	if err != nil {
		t.Fatalf("SaveSet: %v", err)
	}

	b, err := d.SaveSet(ctx, ImplicitSet{Identities: []PublicIdentity{identity("sip:c@x", false), identity("sip:b@x", false)}})
	if err != nil {
		t.Fatalf("SaveSet: %v", err)
	}

	if got, _ := d.GetSet(ctx, a.ID); !reflect.DeepEqual(got.Identities, a.Identities[:1]) {
		t.Fatalf("first set = %+v, want b moved out", got)
	}

	if sets, err := d.FindSets(ctx, []string{"sip:b@x"}); err != nil || len(sets) != 1 || sets[0].ID != b.ID {
		t.Fatalf("FindSets = %+v, %v; want the second set", sets, err)
	}
}

// TS 29.228 §6.1.2.1: the sets kept for identities that are not registered, by expiry.
func TestUnregisteredSets(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	psi, err := d.SaveSet(ctx, ImplicitSet{
		UserData: []byte("<IMSSubscription/>"), HSS: HSS{Host: "hss", Realm: "realm"},
		Unregistered: &Unregistered{Identity: "sip:conf1@x", Wildcard: "sip:conf!.*!@x", ExpiresAt: testNow},
	})
	if err != nil {
		t.Fatalf("SaveSet: %v", err)
	}

	user, err := d.SaveSet(ctx, ImplicitSet{
		Identities:   []PublicIdentity{identity("sip:alice@x", false)},
		Unregistered: &Unregistered{Identity: "sip:alice@x", ExpiresAt: testNow.Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("SaveSet: %v", err)
	}

	if sets, err := d.FindSets(ctx, []string{"sip:alice@x"}); err != nil || len(sets) != 1 || sets[0].ID != user.ID {
		t.Fatalf("FindSets = %+v, %v; want the user's set", sets, err)
	}

	sets, err := d.ListUnregisteredSets(ctx)
	if err != nil || len(sets) != 2 || !reflect.DeepEqual(sets[0], psi) {
		t.Fatalf("ListUnregisteredSets = %+v, %v; want %+v first", sets, err, psi)
	}

	// A registration takes the set over.
	r := testRegistration(testIMPI, "15551230001")
	r.SetID = user.ID
	r = mustSaveRegistration(t, d, r)

	if err := d.DeleteSet(ctx, user.ID); err == nil {
		t.Fatal("DeleteSet of a registered set succeeded")
	}

	if err := d.DeleteSet(ctx, psi.ID); err != nil {
		t.Fatalf("DeleteSet: %v", err)
	}

	if err := d.DeleteSet(ctx, psi.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteSet again = %v, want ErrNotFound", err)
	}

	if regs := listByIMPI(t, d); len(regs) != 1 || regs[0].ID != r.ID {
		t.Fatalf("registrations = %+v", regs)
	}
}

func TestDeleteOrphanSets(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	orphan, err := d.SaveSet(ctx, ImplicitSet{Identities: []PublicIdentity{identity("sip:a@x", false)}})
	if err != nil {
		t.Fatalf("SaveSet: %v", err)
	}

	kept, err := d.SaveSet(ctx, ImplicitSet{Unregistered: &Unregistered{Identity: "sip:b@x", ExpiresAt: testNow}})
	if err != nil {
		t.Fatalf("SaveSet: %v", err)
	}

	r := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	if err := d.DeleteOrphanSets(ctx); err != nil {
		t.Fatalf("DeleteOrphanSets: %v", err)
	}

	if _, err := d.GetSet(ctx, orphan.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphan set: %v, want it gone", err)
	}

	for _, id := range []int64{kept.ID, r.SetID} {
		if _, err := d.GetSet(ctx, id); err != nil {
			t.Fatalf("set %d: %v, want it kept", id, err)
		}
	}
}

func TestSaveRegistrationOfOtherIMPI(t *testing.T) {
	d := openTestDB(t)

	r := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))
	r.IMPI = "other@" + testDomain

	if _, err := d.SaveRegistration(context.Background(), r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SaveRegistration err = %v, want ErrNotFound", err)
	}
}

func TestListRegistrations(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	for i, msisdn := range []string{"15551230001", "15551230002", "15551230003"} {
		r := testRegistration("ue"+msisdn+"@"+testDomain, msisdn)
		r.Bindings[0].Contact = testContact("2001:db8::" + string(rune('1'+i)))
		mustSaveRegistration(t, d, r)
	}

	regs, total, err := d.ListRegistrations(ctx, 2, 2)
	if err != nil {
		t.Fatalf("ListRegistrations: %v", err)
	}

	if total != 3 || len(regs) != 1 || regs[0].IMPI != "ue15551230003@"+testDomain || len(regs[0].Bindings) != 1 {
		t.Fatalf("ListRegistrations = %+v, %d", regs, total)
	}
}

func TestDeleteRegistrationCascades(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	r := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	if _, err := d.PutRegSubscription(ctx, testSubscription()); err != nil {
		t.Fatalf("PutRegSubscription: %v", err)
	}

	if err := d.DeleteRegistration(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRegistration: %v", err)
	}

	if subs, err := d.ListRegSubscriptions(ctx, testIMPI); err != nil || len(subs) != 1 {
		t.Fatalf("ListRegSubscriptions = %v, %v; want the subscription kept", subs, err)
	}

	for _, table := range []string{"implicit_sets", "set_identities", "bindings"} {
		var n int
		if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}

		if n != 0 {
			t.Errorf("%d rows left in %s", n, table)
		}
	}

	if err := d.DeleteRegistration(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteRegistration again err = %v, want ErrNotFound", err)
	}
}

func TestListExpiredIMPIs(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	expired := testRegistration(testIMPI, "15551230001")
	expired.Bindings[0].ExpiresAt = testNow
	mustSaveRegistration(t, d, expired)

	live := testRegistration("live@"+testDomain, "15551230002")
	live.Bindings[0].Contact = testContact("2001:db8::2")
	mustSaveRegistration(t, d, live)

	impis, err := d.ListExpiredIMPIs(ctx, testNow)
	if err != nil {
		t.Fatalf("ListExpiredIMPIs: %v", err)
	}

	if !reflect.DeepEqual(impis, []string{testIMPI}) {
		t.Fatalf("ListExpiredIMPIs = %v, want [%s]", impis, testIMPI)
	}
}

// RFC 5626 §6: a flow is bound by its instance ID and reg-id. Several flows share a URI, a flow keeps
// its binding over a new URI, and a contact address without reg-id is another binding.
func TestFlowBindings(t *testing.T) {
	d := openTestDB(t)

	r := testRegistration(testIMPI, "15551230001")
	plain := r.Bindings[0]

	flow := func(regID int64, host string) Binding {
		b := plain
		b.Contact = testFlow(host, regID)

		return b
	}

	r.Bindings = []Binding{plain, flow(1, "2001:db8::1"), flow(2, "2001:db8::1")}
	saved := mustSaveRegistration(t, d, r)

	ids := map[int64]bool{}
	for _, b := range saved.Bindings {
		ids[b.ID] = true
	}

	if len(ids) != 3 {
		t.Fatalf("contacts %+v, want three bindings over one URI", saved.Bindings)
	}

	id := saved.Bindings[2].ID
	moved := flow(2, "2001:db8::2")
	saved.Bindings[2] = moved

	again := mustSaveRegistration(t, d, saved)
	if c := again.Bindings[2].Contact; again.Bindings[2].ID != id || c.URI != moved.Contact.URI || !c.Flow() ||
		c.Instance != testInstance || c.RegID != 2 {
		t.Fatalf("flow 2 = %+v, want its binding with the new URI", c)
	}

	if got := listByIMPI(t, d); len(got) != 1 || !reflect.DeepEqual(got[0], again) {
		t.Fatalf("registrations = %+v, want %+v", got, again)
	}
}

func TestRegIDWithoutInstance(t *testing.T) {
	d := openTestDB(t)

	r := testRegistration(testIMPI, "15551230001")
	r.Bindings[0].Contact.Instance = ""
	r.Bindings[0].Contact.RegID = 1

	if _, err := d.SaveRegistration(context.Background(), r); err == nil {
		t.Fatal("saved a reg-id without an instance ID")
	}
}
