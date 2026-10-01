package db

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

const testIMPI = "001010000000001@ims.mnc001.mcc001.3gppnetwork.org"

func testSAs(base uint32) *SecurityAssociations {
	return &SecurityAssociations{
		UEPortC:    5100,
		UEPortS:    5101,
		PCSCFPortC: 5200,
		PCSCFPortS: 5201,
		SPIUC:      base,
		SPIUS:      base + 1,
		SPIPC:      base + 2,
		SPIPS:      base + 3,
		Integrity:  IntegrityHMACSHA196,
		Encryption: EncryptionNull,
	}
}

func testRegistration(impi, msisdn string) Registration {
	return Registration{
		IMPI:       impi,
		Contact:    "<sip:" + msisdn + "@[2001:db8::1]:5100>",
		InstanceID: "<urn:gsma:imei:35000000-000000-0>",
		CallID:     "reg-" + msisdn,
		CSeq:       1,
		UEAddress:  netip.MustParseAddr("2001:db8::1"),
		Path:       "<sip:term@pcscf.ims.mnc001.mcc001.3gppnetwork.org;lr>",
		IPsec:      testSAs(4096),
		Identities: []PublicIdentity{
			{URI: "sip:" + impi, Barred: true},
			{URI: "sip:+" + msisdn + "@ims.mnc001.mcc001.3gppnetwork.org"},
			{URI: "tel:+" + msisdn},
		},
		RegisteredAt: testNow,
		ExpiresAt:    testNow.Add(time.Hour),
	}
}

func mustPutRegistration(t *testing.T, d *DB, r Registration) int64 {
	t.Helper()

	id, err := d.PutRegistration(context.Background(), r)
	if err != nil {
		t.Fatalf("PutRegistration: %v", err)
	}

	return id
}

func TestRegistrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	want := testRegistration(testIMPI, "15551230001")
	want.RxSessionID = "pcscf.ims;1;2"
	want.ID = mustPutRegistration(t, d, want)

	got, err := d.GetRegistration(ctx, testIMPI)
	if err != nil {
		t.Fatalf("GetRegistration: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetRegistration =\n%+v\nwant\n%+v", got, want)
	}

	got, err = d.GetRegistrationByIdentity(ctx, "tel:+15551230001")
	if err != nil {
		t.Fatalf("GetRegistrationByIdentity: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetRegistrationByIdentity =\n%+v\nwant\n%+v", got, want)
	}
}

func TestPlainSIPRegistration(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	r := testRegistration(testIMPI, "15551230001")
	r.IPsec = nil
	r.InstanceID = ""
	r.UEAddress = netip.MustParseAddr("10.45.0.2")
	mustPutRegistration(t, d, r)

	got, err := d.GetRegistration(ctx, testIMPI)
	if err != nil {
		t.Fatalf("GetRegistration: %v", err)
	}

	if got.IPsec != nil || got.InstanceID != "" || got.RxSessionID != "" || got.UEAddress != r.UEAddress {
		t.Fatalf("registration = %+v", got)
	}
}

func TestRegistrationRejectsPartialSAs(t *testing.T) {
	d := openTestDB(t)

	if _, err := d.conn.ExecContext(context.Background(),
		`INSERT INTO registrations (impi, contact, call_id, cseq, ue_address, alg, ealg, registered_at, expires_at)
		VALUES ('x', 'c', 'i', 1, '10.0.0.1', 'hmac-sha-1-96', 'null', 0, 0)`); err == nil {
		t.Fatal("insert with partial SAs succeeded")
	}
}

func TestRegistrationRejectsUnknownAlgorithm(t *testing.T) {
	d := openTestDB(t)

	r := testRegistration(testIMPI, "15551230001")
	r.IPsec.Integrity = "hmac-sha-256-128"

	if _, err := d.PutRegistration(context.Background(), r); err == nil {
		t.Fatal("PutRegistration succeeded with an unknown algorithm")
	}
}

func TestPutRegistrationReplaces(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	oldID := mustPutRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	if _, err := d.PutRegSubscription(ctx, testSubscription(oldID)); err != nil {
		t.Fatalf("PutRegSubscription: %v", err)
	}

	r := testRegistration(testIMPI, "15551230009")
	r.Identities = r.Identities[1:]
	newID := mustPutRegistration(t, d, r)

	if newID == oldID {
		t.Fatalf("replacement kept id %d", oldID)
	}

	if _, err := d.GetRegistrationByIdentity(ctx, "tel:+15551230001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old identity lookup err = %v, want ErrNotFound", err)
	}

	got, err := d.GetRegistration(ctx, testIMPI)
	if err != nil {
		t.Fatalf("GetRegistration: %v", err)
	}

	if got.ID != newID || !reflect.DeepEqual(got.Identities, r.Identities) {
		t.Fatalf("registration = %+v", got)
	}

	subs, err := d.ListRegSubscriptions(ctx, oldID)
	if err != nil {
		t.Fatalf("ListRegSubscriptions: %v", err)
	}

	if len(subs) != 0 {
		t.Fatalf("old subscriptions = %+v, want none", subs)
	}
}

func TestPutRegistrationIdentityConflict(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	first := testRegistration(testIMPI, "15551230001")
	firstID := mustPutRegistration(t, d, first)

	second := testRegistration("001010000000002@ims.mnc001.mcc001.3gppnetwork.org", "15551230001")
	if _, err := d.PutRegistration(ctx, second); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("PutRegistration err = %v, want ErrIdentityConflict", err)
	}

	if _, err := d.GetRegistration(ctx, second.IMPI); !errors.Is(err, ErrNotFound) {
		t.Fatalf("conflicting registration was stored: err = %v", err)
	}

	got, err := d.GetRegistrationByIdentity(ctx, "tel:+15551230001")
	if err != nil {
		t.Fatalf("GetRegistrationByIdentity: %v", err)
	}

	if got.ID != firstID {
		t.Fatalf("identity moved to registration %d, want %d", got.ID, firstID)
	}
}

func TestPutRegistrationConflictKeepsReplacedRow(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	mustPutRegistration(t, d, testRegistration("001010000000002@ims.mnc001.mcc001.3gppnetwork.org", "15551230002"))
	originalID := mustPutRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	conflicting := testRegistration(testIMPI, "15551230002")
	if _, err := d.PutRegistration(ctx, conflicting); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("PutRegistration err = %v, want ErrIdentityConflict", err)
	}

	got, err := d.GetRegistration(ctx, testIMPI)
	if err != nil {
		t.Fatalf("GetRegistration: %v", err)
	}

	if got.ID != originalID {
		t.Fatalf("registration id = %d, want the original %d", got.ID, originalID)
	}
}

func TestRefreshRegistration(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	r := testRegistration(testIMPI, "15551230001")
	mustPutRegistration(t, d, r)

	u := RegistrationRefresh{
		CallID:    "reg-2",
		CSeq:      2,
		Path:      "<sip:term@pcscf2.ims.mnc001.mcc001.3gppnetwork.org;lr>",
		IPsec:     testSAs(5000),
		ExpiresAt: testNow.Add(2 * time.Hour),
	}
	if err := d.RefreshRegistration(ctx, testIMPI, u); err != nil {
		t.Fatalf("RefreshRegistration: %v", err)
	}

	got, err := d.GetRegistration(ctx, testIMPI)
	if err != nil {
		t.Fatalf("GetRegistration: %v", err)
	}

	if got.CallID != u.CallID || got.CSeq != u.CSeq || got.Path != u.Path || *got.IPsec != *u.IPsec || !got.ExpiresAt.Equal(u.ExpiresAt) ||
		!got.RegisteredAt.Equal(r.RegisteredAt) || len(got.Identities) != len(r.Identities) {
		t.Fatalf("registration = %+v", got)
	}

	if err := d.RefreshRegistration(ctx, "unknown", u); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RefreshRegistration unknown err = %v, want ErrNotFound", err)
	}
}

func TestSetRegistrationRxSession(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	mustPutRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	if err := d.SetRegistrationRxSession(ctx, testIMPI, "pcscf.ims;1;2"); err != nil {
		t.Fatalf("SetRegistrationRxSession: %v", err)
	}

	got, err := d.GetRegistration(ctx, testIMPI)
	if err != nil {
		t.Fatalf("GetRegistration: %v", err)
	}

	if got.RxSessionID != "pcscf.ims;1;2" {
		t.Fatalf("RxSessionID = %q", got.RxSessionID)
	}

	if err := d.SetRegistrationRxSession(ctx, "unknown", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetRegistrationRxSession unknown err = %v, want ErrNotFound", err)
	}
}

func TestGetRegistrationNotFound(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	if _, err := d.GetRegistration(ctx, testIMPI); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRegistration err = %v, want ErrNotFound", err)
	}

	if _, err := d.GetRegistrationByIdentity(ctx, "tel:+15551230001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRegistrationByIdentity err = %v, want ErrNotFound", err)
	}
}

func TestListRegistrations(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	var ids []int64

	for i := range 5 {
		r := testRegistration(
			"00101000000000"+string(rune('1'+i))+"@ims.mnc001.mcc001.3gppnetwork.org",
			"1555123000"+string(rune('1'+i)))
		ids = append(ids, mustPutRegistration(t, d, r))
	}

	regs, total, err := d.ListRegistrations(ctx, 2, 2)
	if err != nil {
		t.Fatalf("ListRegistrations: %v", err)
	}

	if total != 5 || len(regs) != 2 || regs[0].ID != ids[2] || regs[1].ID != ids[3] {
		t.Fatalf("ListRegistrations = %d rows, total %d", len(regs), total)
	}

	for _, r := range regs {
		if len(r.Identities) != 3 {
			t.Fatalf("registration %d identities = %+v", r.ID, r.Identities)
		}
	}
}

func TestDeleteRegistrationCascades(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id := mustPutRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	if _, err := d.PutRegSubscription(ctx, testSubscription(id)); err != nil {
		t.Fatalf("PutRegSubscription: %v", err)
	}

	if err := d.DeleteRegistration(ctx, testIMPI); err != nil {
		t.Fatalf("DeleteRegistration: %v", err)
	}

	assertCount(t, d, "registration_identities", 0)
	assertCount(t, d, "reg_subscriptions", 0)

	if err := d.DeleteRegistration(ctx, testIMPI); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteRegistration err = %v, want ErrNotFound", err)
	}
}

func TestDeleteExpiredRegistrations(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	expired := testRegistration(testIMPI, "15551230001")
	expired.ExpiresAt = testNow
	expiredID := mustPutRegistration(t, d, expired)

	if _, err := d.PutRegSubscription(ctx, testSubscription(expiredID)); err != nil {
		t.Fatalf("PutRegSubscription: %v", err)
	}

	live := testRegistration("001010000000002@ims.mnc001.mcc001.3gppnetwork.org", "15551230002")
	live.ExpiresAt = testNow.Add(time.Nanosecond)
	liveID := mustPutRegistration(t, d, live)

	deleted, err := d.DeleteExpiredRegistrations(ctx, testNow)
	if err != nil {
		t.Fatalf("DeleteExpiredRegistrations: %v", err)
	}

	if len(deleted) != 1 || deleted[0].ID != expiredID || len(deleted[0].Identities) != 3 || deleted[0].IPsec == nil {
		t.Fatalf("deleted = %+v", deleted)
	}

	regs, _, err := d.ListRegistrations(ctx, 1, 10)
	if err != nil {
		t.Fatalf("ListRegistrations: %v", err)
	}

	if len(regs) != 1 || regs[0].ID != liveID {
		t.Fatalf("remaining = %+v", regs)
	}

	assertCount(t, d, "reg_subscriptions", 0)
	assertCount(t, d, "registration_identities", 3)
}

func assertCount(t *testing.T, d *DB, table string, want int) {
	t.Helper()

	var n int
	if err := d.conn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}

	if n != want {
		t.Fatalf("%s rows = %d, want %d", table, n, want)
	}
}
