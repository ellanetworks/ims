package db

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

func testPCSCFRegistration() PCSCFRegistration {
	return PCSCFRegistration{
		IMPI:           testIMPI,
		FlowToken:      "f1",
		Transport:      "UDP",
		UEAddress:      netip.MustParseAddrPort("[2001:db8::1]:5100"),
		PCSCFAddress:   netip.MustParseAddr("2001:db8::10"),
		Contacts:       []string{"sip:ue@[2001:db8::1]:5100"},
		AssociatedURIs: []string{"sip:+15551230001@" + testDomain, "tel:+15551230001"},
		ServiceRoute:   []string{"<sip:orig-1@scscf." + testDomain + ";lr>"},
		ExpiresAt:      testNow.Add(time.Hour),
	}
}

func TestPCSCFRegistrationLifecycle(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	want, err := d.SavePCSCFRegistration(ctx, testPCSCFRegistration())
	if err != nil {
		t.Fatalf("SavePCSCFRegistration: %v", err)
	}

	update := testPCSCFRegistration()
	update.ExpiresAt = testNow.Add(2 * time.Hour)
	update.AssociatedURIs = update.AssociatedURIs[:1]

	got, err := d.SavePCSCFRegistration(ctx, update)
	if err != nil || got.ID != want.ID {
		t.Fatalf("SavePCSCFRegistration again = %+v, %v; want the same row", got, err)
	}

	regs, err := d.ListPCSCFRegistrations(ctx)
	if err != nil || len(regs) != 1 || !reflect.DeepEqual(regs[0], got) {
		t.Fatalf("ListPCSCFRegistrations = %+v, %v; want %+v", regs, err, got)
	}

	if err := d.DeletePCSCFRegistration(ctx, got.ID); err != nil {
		t.Fatalf("DeletePCSCFRegistration: %v", err)
	}

	if err := d.DeletePCSCFRegistration(ctx, got.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeletePCSCFRegistration again err = %v, want ErrNotFound", err)
	}
}

func TestPCSCFSubscriptionLifecycle(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	s := PCSCFSubscription{
		IMPI: testIMPI, IMPU: "sip:+15551230001@" + testDomain, CallID: "c1", LocalTag: "t1",
		Version: -1, ExpiresAt: testNow.Add(time.Hour),
	}

	first, err := d.SavePCSCFSubscription(ctx, s)
	if err != nil {
		t.Fatalf("SavePCSCFSubscription: %v", err)
	}

	s.Dialog = []byte(`{}`)
	s.Version = 2

	second, err := d.SavePCSCFSubscription(ctx, s)
	if err != nil || second.ID != first.ID || second.Version != 2 || string(second.Dialog) != "{}" {
		t.Fatalf("SavePCSCFSubscription again = %+v, %v", second, err)
	}

	subs, err := d.ListPCSCFSubscriptions(ctx)
	if err != nil || len(subs) != 1 || !reflect.DeepEqual(subs[0], second) {
		t.Fatalf("ListPCSCFSubscriptions = %+v, %v", subs, err)
	}

	if err := d.DeletePCSCFSubscription(ctx, testIMPI); err != nil {
		t.Fatalf("DeletePCSCFSubscription: %v", err)
	}

	if err := d.DeletePCSCFSubscription(ctx, testIMPI); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeletePCSCFSubscription again err = %v, want ErrNotFound", err)
	}
}
