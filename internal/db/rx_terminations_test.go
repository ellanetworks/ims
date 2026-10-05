package db

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

func TestRxTerminationLifecycle(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	a := RxTermination{
		SessionID: "ims;1;1", IMPI: testIMPI, UEAddress: netip.MustParseAddr("2001:db8::1"), Cause: 4,
		Class: [][]byte{{0x01, 0x02}}, CreatedAt: testNow,
	}
	b := RxTermination{
		SessionID: "ims;1;2", IMPI: testIMPI, UEAddress: netip.MustParseAddr("10.0.0.1"), Cause: 1,
		CreatedAt: testNow.Add(time.Second),
	}

	for _, r := range []RxTermination{a, b} {
		if err := d.SaveRxTermination(ctx, r); err != nil {
			t.Fatalf("SaveRxTermination: %v", err)
		}
	}

	later := a
	later.Cause, later.Class, later.CreatedAt = 0, nil, testNow.Add(time.Hour)

	if err := d.SaveRxTermination(ctx, later); err != nil {
		t.Fatalf("SaveRxTermination without a cause: %v", err)
	}

	if got, err := d.GetRxTermination(ctx, a.SessionID); err != nil || !reflect.DeepEqual(got, a) {
		t.Fatalf("GetRxTermination = %+v, %v; want %+v kept", got, err, a)
	}

	a.Cause = 6
	if err := d.SaveRxTermination(ctx, a); err != nil {
		t.Fatalf("SaveRxTermination with a cause: %v", err)
	}

	got, err := d.ListRxTerminations(ctx)
	if err != nil {
		t.Fatalf("ListRxTerminations: %v", err)
	}

	if want := []RxTermination{a, b}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListRxTerminations = %+v, want %+v", got, want)
	}

	if err := d.DeleteRxTermination(ctx, a.SessionID); err != nil {
		t.Fatalf("DeleteRxTermination: %v", err)
	}

	if err := d.DeleteRxTermination(ctx, a.SessionID); err != nil {
		t.Fatalf("DeleteRxTermination of a deleted row: %v", err)
	}

	if _, err := d.GetRxTermination(ctx, a.SessionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRxTermination after the delete: %v, want ErrNotFound", err)
	}

	if got, _ := d.ListRxTerminations(ctx); !reflect.DeepEqual(got, []RxTermination{b}) {
		t.Fatalf("ListRxTerminations = %+v, want only %s", got, b.SessionID)
	}

	if err := d.SaveRxTermination(ctx, RxTermination{SessionID: "x"}); err == nil {
		t.Fatal("SaveRxTermination without an address succeeded")
	}
}
