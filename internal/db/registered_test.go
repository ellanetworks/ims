package db

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func listRegistered(t *testing.T, d *DB, search string, now time.Time, page, perPage int) ([]string, int) {
	t.Helper()

	impis, total, err := d.ListRegisteredIMPIs(context.Background(), search, now, page, perPage)
	if err != nil {
		t.Fatalf("ListRegisteredIMPIs: %v", err)
	}

	return impis, total
}

func TestListRegisteredIMPIs(t *testing.T) {
	d := openTestDB(t)

	const (
		alice = "001010000000001@" + testDomain
		bob   = "001010000000002@" + testDomain
		carol = "001010000000003@" + testDomain
	)

	mustSaveRegistration(t, d, testRegistration(carol, "15551230003"))
	mustSaveRegistration(t, d, testRegistration(alice, "15551230001"))

	// Two registration sets of one private identity count once.
	second := testRegistration(alice, "15551230009")
	second.IMPU = "sip:second@" + testDomain
	second.Identities = []PublicIdentity{identity("sip:second@"+testDomain, false)}
	second.Bindings[0].IMPU = second.IMPU
	mustSaveRegistration(t, d, second)

	expired := testRegistration(bob, "15551230002")
	expired.Bindings[0].ExpiresAt = testNow
	mustSaveRegistration(t, d, expired)

	for _, tt := range []struct {
		name          string
		search        string
		page, perPage int
		want          []string
		total         int
	}{
		{"all, in order, without the expired", "", 1, 25, []string{alice, carol}, 2},
		{"a page", "", 2, 1, []string{carol}, 2},
		{"past the last page", "", 3, 1, []string{}, 2},
		{"by IMSI", "0000000003", 1, 25, []string{carol}, 1},
		{"by number", "+15551230001", 1, 25, []string{alice}, 1},
		{"by an identity of another set", "second@", 1, 25, []string{alice}, 1},
		{"expired", "15551230002", 1, 25, []string{}, 0},
		{"LIKE wildcards are literal", "%", 1, 25, []string{}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, total := listRegistered(t, d, tt.search, testNow, tt.page, tt.perPage)
			if !reflect.DeepEqual(got, tt.want) || total != tt.total {
				t.Fatalf("got %v (%d), want %v (%d)", got, total, tt.want, tt.total)
			}
		})
	}

	// The private identity of two registration sets counts once, and the expired one not at all.
	if n, err := d.CountRegisteredIMPIs(context.Background(), testNow); err != nil || n != 2 {
		t.Fatalf("CountRegisteredIMPIs = %d, %v, want 2", n, err)
	}
}

func TestListPCSCFRegistrationsByIMPI(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	want, err := d.SavePCSCFRegistration(ctx, testPCSCFRegistration())
	if err != nil {
		t.Fatalf("SavePCSCFRegistration: %v", err)
	}

	other := testPCSCFRegistration()
	other.IMPI = "001010000000002@" + testDomain
	other.FlowToken = "f2"

	if _, err := d.SavePCSCFRegistration(ctx, other); err != nil {
		t.Fatalf("SavePCSCFRegistration: %v", err)
	}

	got, err := d.ListPCSCFRegistrationsByIMPI(ctx, testIMPI)
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("ListPCSCFRegistrationsByIMPI = %+v, %v; want [%+v]", got, err, want)
	}
}
