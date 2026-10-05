package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()

	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() { _ = d.Close() })

	return d
}

func TestOpenReachesLatestVersion(t *testing.T) {
	d := openTestDB(t)

	var version int
	if err := d.conn.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}

	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}
}

func TestOpenUpgradesTheFirstSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ims.db")

	conn, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := conn.ExecContext(ctx, migrations[0]+"; PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	d, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() { _ = d.Close() })

	if _, err := d.ListRxTerminations(ctx); err != nil {
		t.Fatalf("ListRxTerminations after the upgrade: %v", err)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ims.db")

	for range 2 {
		d, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ims.db")

	d, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, err := d.conn.ExecContext(ctx, "PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}

	_ = d.Close()

	if d, err := Open(ctx, path); err == nil {
		_ = d.Close()

		t.Fatal("Open succeeded on a newer schema")
	}
}
