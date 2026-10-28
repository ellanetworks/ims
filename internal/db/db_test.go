package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestReadsDoNotHoldUpWrites checks that a read in progress on the readers lets the writer write, and that the
// readers cannot write.
func TestReadsDoNotHoldUpWrites(t *testing.T) {
	d := openTestDB(t)

	tx, err := d.read.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM call_records`).Scan(&n); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	if errs, err := d.SaveCallRecords(ctx, []*CallRecord{attempt("ICID1", callT0)}, nil); err != nil || errs != nil {
		t.Fatalf("SaveCallRecords during a read = %v, %v", errs, err)
	}

	if _, err := d.read.ExecContext(t.Context(), `DELETE FROM call_records`); err == nil {
		t.Fatal("the readers wrote")
	}
}

// TestDatabaseIsPrivate checks that only its owner can read the database, which holds the password hashes and the key
// that signs the access tokens, and its journal.
func TestDatabaseIsPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ims.db")

	// An existing database that others can read is made private.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	if _, err := d.CreateUser(context.Background(), NewUser{Email: "a@example.com", Role: RoleAdmin}); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"ims.db", "ims.db-wal", "ims.db-shm"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}

		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, mode)
		}
	}
}
