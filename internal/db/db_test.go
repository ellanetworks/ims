package db

import (
	"context"
	"database/sql"
	"errors"
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
	if err := d.writer.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
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

	if _, err := d.writer.ExecContext(ctx, "PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}

	_ = d.Close()

	if d, err := Open(ctx, path); err == nil {
		_ = d.Close()

		t.Fatal("Open succeeded on a newer schema")
	}
}

func TestReadsDoNotHoldUpWrites(t *testing.T) {
	d := openTestDB(t)

	tx, err := d.reader().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
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

	if _, err := d.reader().ExecContext(t.Context(), `DELETE FROM call_records`); err == nil {
		t.Fatal("the readers wrote")
	}
}

func TestDatabaseIsPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ims.db")

	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	if _, err := d.CreateUser(context.Background(), NewUser{Email: "a@example.com", Role: RoleAdmin}, 100); err != nil {
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

func TestReadsSucceedDuringInstall(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)

	for range 3 {
		c, err := d.CopyTo(ctx, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		stop := make(chan struct{})
		failed := make(chan error, 1)

		go func() {
			for {
				select {
				case <-stop:
					close(failed)
					return
				default:
				}

				if _, err := d.GetSettings(ctx); err != nil {
					failed <- err
					return
				}
			}
		}()

		if err := d.Install(ctx, c.Path, &c.Position); err != nil {
			t.Fatal(err)
		}

		close(stop)

		if err := <-failed; err != nil {
			t.Fatalf("read during the install: %v", err)
		}
	}
}

func TestWipeClearsTheNodeDatabase(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)

	if _, err := d.SaveSecurityAssociation(ctx, testSecurityAssociation()); err != nil {
		t.Fatal(err)
	}

	scratch := t.TempDir()

	if err := d.Wipe(ctx, scratch); err != nil {
		t.Fatal(err)
	}

	if sas, err := d.ListSecurityAssociations(ctx); err != nil || len(sas) != 0 {
		t.Fatalf("security associations after a wipe = %v, %v; want none", sas, err)
	}

	if _, err := os.Stat(filepath.Join(scratch, localFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("node database in the scratch directory: %v", err)
	}
}
