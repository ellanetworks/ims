package db

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func testSubscription() RegSubscription {
	return RegSubscription{
		IMPI:         testIMPI,
		CallID:       "sub-1",
		RemoteTag:    "ue-tag",
		LocalTag:     "scscf-tag",
		RemoteTarget: "sip:[2001:db8::1]:5100",
		RemoteCSeq:   1,
		LocalCSeq:    1,
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

	expires := testNow.Add(2 * time.Hour)
	if err := d.RefreshRegSubscription(ctx, id, 2, expires); err != nil {
		t.Fatalf("RefreshRegSubscription: %v", err)
	}

	cseq, version, err := d.NextNotify(ctx, id)
	if err != nil {
		t.Fatalf("NextNotify: %v", err)
	}

	if cseq != 2 || version != 1 {
		t.Fatalf("NextNotify = (%d, %d), want (2, 1)", cseq, version)
	}

	subs, err = d.ListRegSubscriptions(ctx, testIMPI)
	if err != nil {
		t.Fatalf("ListRegSubscriptions: %v", err)
	}

	if s := subs[0]; s.RemoteCSeq != 2 || !s.ExpiresAt.Equal(expires) || s.LocalCSeq != 2 || s.Version != 1 {
		t.Fatalf("subscription = %+v", s)
	}

	if err := d.DeleteRegSubscription(ctx, id); err != nil {
		t.Fatalf("DeleteRegSubscription: %v", err)
	}

	for name, err := range map[string]error{
		"RefreshRegSubscription": d.RefreshRegSubscription(ctx, id, 3, expires),
		"DeleteRegSubscription":  d.DeleteRegSubscription(ctx, id),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s after delete err = %v, want ErrNotFound", name, err)
		}
	}

	if _, _, err := d.NextNotify(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("NextNotify after delete err = %v, want ErrNotFound", err)
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
}

func TestNextNotifyConcurrent(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.PutRegSubscription(ctx, testSubscription())
	if err != nil {
		t.Fatalf("PutRegSubscription: %v", err)
	}

	const callers = 50

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		versions = map[int64]bool{}
	)

	for range callers {
		wg.Go(func() {
			cseq, version, err := d.NextNotify(ctx, id)
			if err != nil {
				t.Errorf("NextNotify: %v", err)
				return
			}

			if cseq != version+1 {
				t.Errorf("NextNotify = (%d, %d), CSeq and version out of step", cseq, version)
			}

			mu.Lock()
			versions[version] = true
			mu.Unlock()
		})
	}

	wg.Wait()

	for v := int64(1); v <= callers; v++ {
		if !versions[v] {
			t.Fatalf("version %d was never returned; got %v", v, versions)
		}
	}
}
