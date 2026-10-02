package db

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

func testSecurityAssociation() SecurityAssociation {
	return SecurityAssociation{
		IMPI:         testIMPI,
		State:        SecurityAssociationEstablished,
		PCSCFAddress: netip.MustParseAddr("10.0.0.5"),
		UEAddress:    netip.MustParseAddr("2001:db8::1"),
		PCSCFPortC:   5064,
		PCSCFPortS:   5063,
		UEPortC:      6301,
		UEPortS:      6300,
		SPIPC:        70000,
		SPIPS:        70001,
		SPIUC:        25656,
		SPIUS:        25657,
		Integrity:    "hmac-sha-1-96",
		Encryption:   "null",
		ExpiresAt:    testNow.Add(time.Hour),
	}
}

func TestSecurityAssociationRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	saved, err := d.SaveSecurityAssociation(ctx, testSecurityAssociation())
	if err != nil {
		t.Fatal(err)
	}

	want := testSecurityAssociation()
	want.ID = saved.ID

	if saved.ID == 0 || !reflect.DeepEqual(saved, want) {
		t.Fatalf("saved = %+v, want %+v", saved, want)
	}

	saved.State = SecurityAssociationOld
	saved.ExpiresAt = testNow.Add(2 * time.Minute)

	updated, err := d.SaveSecurityAssociation(ctx, saved)
	if err != nil || !reflect.DeepEqual(updated, saved) {
		t.Fatalf("update = %+v, %v; want %+v", updated, err, saved)
	}

	other := testSecurityAssociation()
	other.UEAddress = netip.MustParseAddr("10.45.0.7")

	if _, err := d.SaveSecurityAssociation(ctx, other); err != nil {
		t.Fatal(err)
	}

	list, err := d.ListSecurityAssociations(ctx)
	if err != nil || len(list) != 2 || !reflect.DeepEqual(list[0], saved) || list[1].UEAddress != other.UEAddress {
		t.Fatalf("list = %+v, %v", list, err)
	}

	if err := d.DeleteSecurityAssociation(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteSecurityAssociation(ctx, saved.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}

	if _, err := d.SaveSecurityAssociation(ctx, saved); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a deleted row = %v, want ErrNotFound", err)
	}

	if list, _ := d.ListSecurityAssociations(ctx); len(list) != 1 {
		t.Fatalf("list after delete = %+v", list)
	}
}

func TestSecurityAssociationRejectsUnknownState(t *testing.T) {
	sa := testSecurityAssociation()
	sa.State = "temporary"

	if _, err := openTestDB(t).SaveSecurityAssociation(context.Background(), sa); err == nil {
		t.Fatal("saved a temporary set")
	}
}
