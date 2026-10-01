package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mattn/go-sqlite3"
)

var ErrNotFound = errors.New("not found")

type DB struct {
	conn *sql.DB
}

// The project is unreleased: the schema stays in migrations[0] and is edited
// in place. The first release freezes it; later changes append migrations.
var migrations = []string{
	`CREATE TABLE registrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		impi TEXT NOT NULL,
		impu TEXT NOT NULL,
		user_data BLOB
	);
	CREATE INDEX registrations_impi ON registrations (impi);
	CREATE TABLE registration_identities (
		registration_id INTEGER NOT NULL REFERENCES registrations (id) ON DELETE CASCADE,
		impi TEXT NOT NULL,
		position INTEGER NOT NULL,
		uri TEXT NOT NULL,
		key TEXT NOT NULL,
		display_name TEXT,
		barred INTEGER NOT NULL CHECK (barred IN (0, 1)),
		PRIMARY KEY (registration_id, position),
		UNIQUE (impi, key)
	);
	CREATE INDEX registration_identities_key ON registration_identities (key);
	CREATE TABLE contacts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		impi TEXT NOT NULL,
		uri TEXT NOT NULL,
		params TEXT NOT NULL,
		path TEXT,
		ue_address TEXT NOT NULL,
		ue_port_c INTEGER,
		ue_port_s INTEGER,
		pcscf_port_c INTEGER,
		pcscf_port_s INTEGER,
		spi_uc INTEGER,
		spi_us INTEGER,
		spi_pc INTEGER,
		spi_ps INTEGER,
		alg TEXT CHECK (alg IN ('hmac-md5-96', 'hmac-sha-1-96')),
		ealg TEXT CHECK (ealg IN ('null', 'aes-cbc')),
		rx_session_id TEXT,
		UNIQUE (impi, uri),
		CHECK ((alg IS NULL) = (ealg IS NULL)
			AND (alg IS NULL) = (ue_port_c IS NULL) AND (alg IS NULL) = (ue_port_s IS NULL)
			AND (alg IS NULL) = (pcscf_port_c IS NULL) AND (alg IS NULL) = (pcscf_port_s IS NULL)
			AND (alg IS NULL) = (spi_uc IS NULL) AND (alg IS NULL) = (spi_us IS NULL)
			AND (alg IS NULL) = (spi_pc IS NULL) AND (alg IS NULL) = (spi_ps IS NULL))
	);
	CREATE TABLE bindings (
		registration_id INTEGER NOT NULL REFERENCES registrations (id) ON DELETE CASCADE,
		contact_id INTEGER NOT NULL REFERENCES contacts (id) ON DELETE CASCADE,
		call_id TEXT NOT NULL,
		cseq INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		PRIMARY KEY (registration_id, contact_id)
	);
	CREATE INDEX bindings_contact_id ON bindings (contact_id);
	CREATE INDEX bindings_expires_at ON bindings (expires_at);
	CREATE TABLE reg_subscriptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		impi TEXT NOT NULL,
		call_id TEXT NOT NULL,
		remote_tag TEXT NOT NULL,
		local_tag TEXT NOT NULL,
		remote_target TEXT NOT NULL,
		remote_cseq INTEGER NOT NULL,
		local_cseq INTEGER NOT NULL,
		version INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		UNIQUE (call_id, remote_tag, local_tag)
	);
	CREATE INDEX reg_subscriptions_impi ON reg_subscriptions (impi);
	CREATE TABLE calls (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		call_id TEXT NOT NULL,
		caller TEXT NOT NULL,
		callee TEXT NOT NULL,
		started_at INTEGER NOT NULL,
		answered_at INTEGER,
		ended_at INTEGER NOT NULL,
		outcome TEXT NOT NULL CHECK (outcome IN ('answered', 'busy', 'no_answer', 'cancelled', 'rejected', 'not_found', 'unreachable', 'failed')),
		sip_status INTEGER NOT NULL,
		ended_by TEXT NOT NULL CHECK (ended_by IN ('caller', 'callee', 'network')),
		reason TEXT
	);
	CREATE INDEX calls_ended_at ON calls (ended_at);
	CREATE INDEX calls_caller ON calls (caller, id);
	CREATE INDEX calls_callee ON calls (callee, id);`,
}

func Open(ctx context.Context, path string) (*DB, error) {
	conn, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	conn.SetMaxOpenConns(1)

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}

	d := &DB{conn: conn}
	if err := d.migrate(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return d, nil
}

func (d *DB) Close() error {
	return d.conn.Close()
}

func (d *DB) migrate(ctx context.Context) error {
	var version int
	if err := d.conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	if version > len(migrations) {
		return fmt.Errorf("schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}

	for i := version; i < len(migrations); i++ {
		tx, err := d.conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}

		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}

		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}

	return nil
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type scanner interface {
	Scan(dest ...any) error
}

func isConstraint(err error, code sqlite3.ErrNoExtended) bool {
	var e sqlite3.Error

	return errors.As(err, &e) && e.ExtendedCode == code
}

func checkAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}

	if n == 0 {
		return ErrNotFound
	}

	return nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func nullableString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
