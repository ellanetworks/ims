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
		UNIQUE (impi, uri)
	);
	CREATE TABLE bindings (
		registration_id INTEGER NOT NULL REFERENCES registrations (id) ON DELETE CASCADE,
		contact_id INTEGER NOT NULL REFERENCES contacts (id) ON DELETE CASCADE,
		call_id TEXT NOT NULL,
		cseq INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		event TEXT NOT NULL CHECK (event IN ('registered', 'refreshed')),
		impu TEXT NOT NULL,
		registered_at INTEGER NOT NULL,
		PRIMARY KEY (registration_id, contact_id)
	);
	CREATE INDEX bindings_contact_id ON bindings (contact_id);
	CREATE INDEX bindings_expires_at ON bindings (expires_at);
	CREATE TABLE reg_subscriptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		impi TEXT NOT NULL,
		impu TEXT NOT NULL,
		subscriber TEXT NOT NULL CHECK (subscriber IN ('ue', 'pcscf')),
		call_id TEXT NOT NULL,
		local_tag TEXT NOT NULL,
		remote_tag TEXT NOT NULL,
		remote_target TEXT NOT NULL,
		dialog BLOB NOT NULL,
		version INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		UNIQUE (call_id, local_tag, remote_tag)
	);
	CREATE INDEX reg_subscriptions_impi ON reg_subscriptions (impi);
	CREATE INDEX reg_subscriptions_expires_at ON reg_subscriptions (expires_at);
	CREATE TABLE security_associations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		impi TEXT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('established', 'old')),
		pcscf_address TEXT NOT NULL,
		ue_address TEXT NOT NULL,
		pcscf_port_c INTEGER NOT NULL,
		pcscf_port_s INTEGER NOT NULL,
		ue_port_c INTEGER NOT NULL,
		ue_port_s INTEGER NOT NULL,
		spi_pc INTEGER NOT NULL,
		spi_ps INTEGER NOT NULL,
		spi_uc INTEGER NOT NULL,
		spi_us INTEGER NOT NULL,
		alg TEXT NOT NULL,
		ealg TEXT NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE INDEX security_associations_impi ON security_associations (impi);
	CREATE TABLE pcscf_registrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		impi TEXT NOT NULL,
		flow_token TEXT NOT NULL UNIQUE,
		transport TEXT NOT NULL,
		protected INTEGER NOT NULL CHECK (protected IN (0, 1)),
		ue_address TEXT NOT NULL,
		ue_port INTEGER NOT NULL,
		pcscf_address TEXT NOT NULL,
		contacts TEXT NOT NULL,
		associated_uris TEXT NOT NULL,
		sets TEXT NOT NULL,
		service_route TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		policy_endpoint TEXT,
		policy_session_id TEXT,
		policy_ref TEXT,
		signalling_lost INTEGER NOT NULL CHECK (signalling_lost IN (0, 1)),
		UNIQUE (impi, ue_address)
	);
	CREATE TABLE pcscf_subscriptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		impi TEXT NOT NULL UNIQUE,
		impu TEXT NOT NULL,
		call_id TEXT NOT NULL,
		local_tag TEXT NOT NULL,
		dialog BLOB,
		version INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);`,
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
