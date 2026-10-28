package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSessions(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 123, time.UTC)

	id := createTestUser(t, d, "a@example.com", RoleAdmin)

	want := Session{UserID: id, TokenHash: []byte("token"), CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)}
	if _, err := d.CreateSession(ctx, NewSession{
		UserID: want.UserID, TokenHash: want.TokenHash,
		CreatedAt: want.CreatedAt, ExpiresAt: want.ExpiresAt,
	}, 10); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := d.GetSessionByTokenHash(ctx, want.TokenHash)
	if err != nil {
		t.Fatalf("GetSessionByTokenHash: %v", err)
	}

	want.ID = got.ID
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetSessionByTokenHash = %+v, want %+v", got, want)
	}

	if byID, err := d.GetSession(ctx, got.ID); err != nil || !reflect.DeepEqual(byID, want) {
		t.Fatalf("GetSession = %+v, %v, want %+v", byID, err, want)
	}

	if _, err := d.GetSession(ctx, got.ID+1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession of an unknown ID: %v, want ErrNotFound", err)
	}

	if _, err := d.CreateSession(ctx, NewSession{
		UserID: id, TokenHash: []byte("token"), CreatedAt: now,
		ExpiresAt: now,
	}, 10); err == nil {
		t.Fatal("CreateSession with an existing token hash: no error")
	}

	for range 2 {
		if err := d.DeleteSessionByTokenHash(ctx, want.TokenHash); err != nil {
			t.Fatalf("DeleteSessionByTokenHash: %v", err)
		}
	}

	if _, err := d.GetSessionByTokenHash(ctx, want.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSessionByTokenHash after delete: %v, want ErrNotFound", err)
	}
}

func TestCreateSessionKeepsTheNewestPerUser(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	id := createTestUser(t, d, "a@example.com", RoleAdmin)
	other := createTestUser(t, d, "b@example.com", RoleAdmin)

	if _, err := d.CreateSession(ctx, NewSession{
		UserID: other, TokenHash: []byte("other"), CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}, 3); err != nil {
		t.Fatal(err)
	}

	for i := range 5 {
		at := now.Add(time.Duration(i) * time.Minute)
		if _, err := d.CreateSession(ctx, NewSession{
			UserID: id, TokenHash: []byte{byte(i)}, CreatedAt: at,
			ExpiresAt: at.Add(time.Hour),
		}, 3); err != nil {
			t.Fatal(err)
		}
	}

	for i, want := range []bool{false, false, true, true, true} {
		_, err := d.GetSessionByTokenHash(ctx, []byte{byte(i)})
		if got := err == nil; got != want {
			t.Errorf("session %d kept = %t (%v), want %t", i, got, err, want)
		}
	}

	if _, err := d.GetSessionByTokenHash(ctx, []byte("other")); err != nil {
		t.Errorf("another user's session: %v", err)
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	id := createTestUser(t, d, "a@example.com", RoleAdmin)

	for name, expiresAt := range map[string]time.Time{
		"expired": now.Add(-time.Second), "expiring": now, "live": now.Add(time.Second),
	} {
		if _, err := d.CreateSession(ctx, NewSession{
			UserID: id, TokenHash: []byte(name), CreatedAt: now.Add(-time.Hour),
			ExpiresAt: expiresAt,
		}, 10); err != nil {
			t.Fatal(err)
		}
	}

	if err := d.DeleteExpiredSessions(ctx, now); err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}

	for name, want := range map[string]bool{"expired": false, "expiring": false, "live": true} {
		if _, err := d.GetSessionByTokenHash(ctx, []byte(name)); (err == nil) != want {
			t.Errorf("%s session kept = %t (%v), want %t", name, err == nil, err, want)
		}
	}
}
