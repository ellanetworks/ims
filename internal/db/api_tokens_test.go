package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAPITokens(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	id := createTestUser(t, d, "ota@example.com", RoleNetworkManager)
	other := createTestUser(t, d, "other@example.com", RoleNetworkManager)

	never := NewAPIToken{TokenID: "id-never", Name: "never", TokenHash: []byte("h1"), UserID: id, CreatedAt: now}
	expiring := NewAPIToken{
		TokenID: "id-expiring", Name: "expiring", TokenHash: []byte("h2"), UserID: id,
		CreatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour),
	}

	var ids []int64

	for _, tok := range []NewAPIToken{never, expiring} {
		tokenID, err := d.CreateAPIToken(ctx, tok)
		if err != nil {
			t.Fatalf("CreateAPIToken %s: %v", tok.Name, err)
		}

		ids = append(ids, tokenID)
	}

	got, err := d.GetAPITokenByTokenID(ctx, "id-expiring")
	if err != nil {
		t.Fatalf("GetAPITokenByTokenID: %v", err)
	}

	want := APIToken{
		ID: ids[1], TokenID: "id-expiring", Name: "expiring", TokenHash: []byte("h2"), UserID: id,
		CreatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetAPITokenByTokenID = %+v, want %+v", got, want)
	}

	if got, err := d.GetAPITokenByTokenID(ctx, "id-never"); err != nil || !got.ExpiresAt.IsZero() ||
		!got.LastUsedAt.IsZero() {
		t.Fatalf("token without expiry = %+v, %v", got, err)
	}

	if err := d.TouchAPIToken(ctx, ids[0], now.Add(time.Minute)); err != nil {
		t.Fatalf("TouchAPIToken: %v", err)
	}

	if got, err := d.GetAPITokenByTokenID(ctx, "id-never"); err != nil || !got.LastUsedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("LastUsedAt = %v, %v, want %v", got.LastUsedAt, err, now.Add(time.Minute))
	}

	for name, tok := range map[string]NewAPIToken{
		"same name for the same user": {TokenID: "id-3", Name: "never", TokenHash: []byte("h3"), UserID: id, CreatedAt: now},
		"same token ID":               {TokenID: "id-never", Name: "x", TokenHash: []byte("h3"), UserID: other, CreatedAt: now},
	} {
		if _, err := d.CreateAPIToken(ctx, tok); !errors.Is(err, ErrDuplicate) {
			t.Errorf("%s: %v, want ErrDuplicate", name, err)
		}
	}

	if _, err := d.CreateAPIToken(ctx, NewAPIToken{
		TokenID: "id-4", Name: "never", TokenHash: []byte("h4"),
		UserID: other, CreatedAt: now,
	}); err != nil {
		t.Fatalf("same name for another user: %v", err)
	}

	if n, err := d.CountAPITokens(ctx, id); err != nil || n != 2 {
		t.Fatalf("CountAPITokens = %d, %v, want 2", n, err)
	}

	tokens, total, err := d.ListAPITokens(ctx, id, 2, 1)
	if err != nil || total != 2 || len(tokens) != 1 || tokens[0].ID != ids[1] {
		t.Fatalf("ListAPITokens page 2 = %+v (total %d), %v", tokens, total, err)
	}

	if err := d.DeleteAPIToken(ctx, other, "id-never"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteAPIToken of another user's token: %v, want ErrNotFound", err)
	}

	if err := d.DeleteAPIToken(ctx, id, "id-never"); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}

	if _, err := d.GetAPITokenByTokenID(ctx, "id-never"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetAPITokenByTokenID after delete: %v, want ErrNotFound", err)
	}
}
