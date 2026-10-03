package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func testSubscription() RegSubscription {
	return RegSubscription{
		IMPI:         testIMPI,
		IMPU:         "sip:+15551230001@" + testDomain,
		Subscriber:   SubscriberUE,
		CallID:       "sub-1",
		RemoteTag:    "ue-tag",
		LocalTag:     "scscf-tag",
		RemoteTarget: "sip:[2001:db8::1]:5100",
		Dialog:       []byte(`{"ID":{}}`),
		Version:      0,
		ExpiresAt:    testNow.Add(time.Hour),
	}
}

func TestRegSubscriptionLifecycle(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	want := testSubscription()

	id, err := d.PutRegSubscription(ctx, want)
	if err != nil {
		t.Fatalf("PutRegSubscription: %v", err)
	}

	want.ID = id

	subs, err := d.ListRegSubscriptions(ctx, testIMPI)
	if err != nil {
		t.Fatalf("ListRegSubscriptions: %v", err)
	}

	if len(subs) != 1 || !reflect.DeepEqual(subs[0], want) {
		t.Fatalf("subscriptions = %+v, want %+v", subs, want)
	}

	got, err := d.GetRegSubscription(ctx, want.CallID, want.LocalTag, want.RemoteTag)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetRegSubscription = %+v, %v", got, err)
	}

	want.Dialog = []byte(`{"ID":{"CallID":"sub-1"}}`)
	want.Version = 3
	want.RemoteTarget = "sip:[2001:db8::2]:5100"
	want.ExpiresAt = testNow.Add(2 * time.Hour)

	if err := d.UpdateRegSubscription(ctx, want); err != nil {
		t.Fatalf("UpdateRegSubscription: %v", err)
	}

	if got, err := d.GetRegSubscriptionByID(ctx, id); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetRegSubscriptionByID = %+v, %v; want %+v", got, err, want)
	}

	if err := d.DeleteRegSubscription(ctx, id); err != nil {
		t.Fatalf("DeleteRegSubscription: %v", err)
	}

	for name, err := range map[string]error{
		"UpdateRegSubscription": d.UpdateRegSubscription(ctx, want),
		"DeleteRegSubscription": d.DeleteRegSubscription(ctx, id),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s after delete err = %v, want ErrNotFound", name, err)
		}
	}

	if _, err := d.GetRegSubscription(ctx, want.CallID, want.LocalTag, want.RemoteTag); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRegSubscription after delete err = %v, want ErrNotFound", err)
	}
}

func TestPutRegSubscriptionErrors(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	if _, err := d.PutRegSubscription(ctx, testSubscription()); err != nil {
		t.Fatalf("PutRegSubscription: %v", err)
	}

	if _, err := d.PutRegSubscription(ctx, testSubscription()); !errors.Is(err, ErrSubscriptionExists) {
		t.Fatalf("duplicate PutRegSubscription err = %v, want ErrSubscriptionExists", err)
	}

	bad := testSubscription()
	bad.CallID = "sub-2"
	bad.Subscriber = "as"

	if _, err := d.PutRegSubscription(ctx, bad); err == nil {
		t.Fatal("PutRegSubscription accepted an unknown subscriber")
	}
}

func TestListExpiredIMPIsIncludesSubscriptions(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	if impis, err := d.ListExpiredIMPIs(ctx, testNow); err != nil || len(impis) != 0 {
		t.Fatalf("ListExpiredIMPIs on an empty database = %v, %v", impis, err)
	}

	sub := testSubscription()
	sub.ExpiresAt = testNow.Add(time.Minute)

	if _, err := d.PutRegSubscription(ctx, sub); err != nil {
		t.Fatalf("PutRegSubscription: %v", err)
	}

	mustSaveRegistration(t, d, testRegistration(testIMPI, "15551230001"))

	impis, err := d.ListExpiredIMPIs(ctx, testNow.Add(2*time.Minute))
	if err != nil || len(impis) != 1 || impis[0] != testIMPI {
		t.Fatalf("ListExpiredIMPIs = %v, %v; want the subscription's IMPI", impis, err)
	}
}
