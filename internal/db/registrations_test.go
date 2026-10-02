package db

import (
	"context"
	"errors"
	"reflect"
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

func testContact(host string) Contact {
	return Contact{
		URI:    "sip:ue@[" + host + "]:5100",
		Params: `;+sip.instance="<urn:gsma:imei:35000000-000000-0>";+g.3gpp.smsip`,
		Path:   "<sip:term@pcscf." + testDomain + ";lr>",
	}
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
		Bindings: []Binding{{
			Contact:   testContact("2001:db8::1"),
			CallID:    "reg-" + msisdn,
			CSeq:      1,
			ExpiresAt: testNow.Add(time.Hour),
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
	if want.ID == 0 || want.Bindings[0].Contact.ID == 0 || want.Bindings[0].Contact.IMPI != testIMPI {
		t.Fatalf("saved = %+v, want IDs and the contact's IMPI set", want)
	}

	if got := listByIMPI(t, d); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("registrations = %+v, want %+v", got, want)
	}
}

func TestSaveRegistrationKeepsTheRxSession(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	stale := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))
	id := stale.Bindings[0].Contact.ID

	if err := d.SetContactRxSession(ctx, id, "pcscf.ims;1;2"); err != nil {
		t.Fatalf("SetContactRxSession: %v", err)
	}

	stale.Bindings[0].Contact.Path = "<sip:term@pcscf2." + testDomain + ";lr>"

	saved := mustSaveRegistration(t, d, stale)

	c := saved.Bindings[0].Contact
	if c.RxSessionID != "pcscf.ims;1;2" {
		t.Fatalf("contact = %+v, want the P-CSCF's Rx session", c)
	}

	if c.Path != stale.Bindings[0].Contact.Path {
		t.Fatalf("Path = %q, want the S-CSCF's update", c.Path)
	}

	if got := listByIMPI(t, d); len(got) != 1 || !reflect.DeepEqual(got[0], saved) {
		t.Fatalf("registrations = %+v, want %+v", got, saved)
	}

	if err := d.SetContactRxSession(ctx, id+1, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetContactRxSession unknown err = %v, want ErrNotFound", err)
	}
}

func TestSaveRegistrationUpdates(t *testing.T) {
	d := openTestDB(t)

	r := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))
	contactID := r.Bindings[0].Contact.ID

	r.IMPU = "tel:+15551230001"
	r.Identities = r.Identities[1:]
	r.UserData = []byte("<IMSSubscription>2</IMSSubscription>")
	r.Bindings[0].CSeq = 2
	r.Bindings[0].ExpiresAt = testNow.Add(2 * time.Hour)
	r.Bindings[0].Contact.Path = "<sip:term@pcscf2." + testDomain + ";lr>"
	r.Bindings[0].Contact.ID = 0

	saved := mustSaveRegistration(t, d, r)
	if saved.Bindings[0].Contact.ID != contactID {
		t.Fatalf("contact ID = %d, want %d: the same URI is the same contact", saved.Bindings[0].Contact.ID, contactID)
	}

	if got := listByIMPI(t, d); len(got) != 1 || !reflect.DeepEqual(got[0], saved) {
		t.Fatalf("registrations = %+v, want %+v", got, saved)
	}
}

func TestSeveralSetsShareAContact(t *testing.T) {
	d := openTestDB(t)

	a := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	b := testRegistration(testIMPI, "15551230002")
	b.IMPU = "sip:second@" + testDomain
	b.Identities = []PublicIdentity{identity(b.IMPU, false)}
	b = mustSaveRegistration(t, d, b)

	if a.Bindings[0].Contact.ID != b.Bindings[0].Contact.ID {
		t.Fatalf("contacts %d and %d, want one contact bound to both sets", a.Bindings[0].Contact.ID, b.Bindings[0].Contact.ID)
	}

	a.Bindings = nil
	mustSaveRegistration(t, d, a)

	regs := listByIMPI(t, d)
	if len(regs) != 2 || len(regs[0].Bindings) != 0 || len(regs[1].Bindings) != 1 {
		t.Fatalf("registrations = %+v", regs)
	}

	if err := d.DeleteRegistration(context.Background(), b.ID); err != nil {
		t.Fatalf("DeleteRegistration: %v", err)
	}

	var contacts int
	if err := d.conn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM contacts`).Scan(&contacts); err != nil {
		t.Fatal(err)
	}

	if contacts != 0 {
		t.Fatalf("%d contacts left, want none once unbound", contacts)
	}
}

func TestSharedIdentity(t *testing.T) {
	d := openTestDB(t)

	phone := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	tablet := testRegistration("tablet@"+testDomain, "15551230001")
	tablet.Bindings[0].Contact = testContact("2001:db8::2")
	tablet = mustSaveRegistration(t, d, tablet)

	regs, err := d.ListRegistrationsByIdentity(context.Background(), "tel:+15551230001")
	if err != nil {
		t.Fatalf("ListRegistrationsByIdentity: %v", err)
	}

	if len(regs) != 2 || regs[0].ID != phone.ID || regs[1].ID != tablet.ID {
		t.Fatalf("registrations = %+v, want the phone's and the tablet's", regs)
	}
}

func TestIdentityConflictWithinIMPI(t *testing.T) {
	d := openTestDB(t)

	a := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	b := testRegistration(testIMPI, "15551230001")
	b.IMPU = "sip:second@" + testDomain
	b.Identities = []PublicIdentity{identity(b.IMPU, false), identity("tel:+15551230001", false)}

	if _, err := d.SaveRegistration(context.Background(), b); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("SaveRegistration err = %v, want ErrIdentityConflict", err)
	}

	if got := listByIMPI(t, d); len(got) != 1 || !reflect.DeepEqual(got[0], a) {
		t.Fatalf("registrations = %+v, want only %+v", got, a)
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

func TestSetContactRxSession(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	r := mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))
	id := r.Bindings[0].Contact.ID

	if err := d.SetContactRxSession(ctx, id, "pcscf.ims;1;2"); err != nil {
		t.Fatalf("SetContactRxSession: %v", err)
	}

	if got := listByIMPI(t, d)[0].Bindings[0].Contact.RxSessionID; got != "pcscf.ims;1;2" {
		t.Fatalf("RxSessionID = %q", got)
	}

	if err := d.SetContactRxSession(ctx, id, ""); err != nil {
		t.Fatalf("SetContactRxSession clear: %v", err)
	}

	if got := listByIMPI(t, d)[0].Bindings[0].Contact.RxSessionID; got != "" {
		t.Fatalf("RxSessionID = %q, want cleared", got)
	}

	if err := d.SetContactRxSession(ctx, id+1, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetContactRxSession unknown err = %v, want ErrNotFound", err)
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

	for _, table := range []string{"registration_identities", "bindings", "contacts"} {
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
