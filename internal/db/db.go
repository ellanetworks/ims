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
	// conn is the only connection that writes, so that writes never wait on each other's locks.
	conn *sql.DB
	// read serves the reads that may take long, such as searches of the call records, so that they never hold up
	// conn: in WAL mode, readers and the writer do not wait on each other.
	read *sql.DB
	// path is the database file, whose size the metrics report.
	path    string
	metrics metrics
}

const maxReaders = 4

var migrations = []string{
	`CREATE TABLE registrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		impi TEXT NOT NULL,
		impu TEXT NOT NULL,
		user_data BLOB,
		hss_host TEXT NOT NULL,
		hss_realm TEXT NOT NULL
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
	CREATE TABLE bindings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		registration_id INTEGER NOT NULL REFERENCES registrations (id) ON DELETE CASCADE,
		uri TEXT NOT NULL,
		instance_id TEXT,
		reg_id INTEGER CHECK (reg_id IS NULL OR (reg_id > 0 AND instance_id IS NOT NULL)),
		params TEXT NOT NULL,
		path TEXT,
		call_id TEXT NOT NULL,
		cseq INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		event TEXT NOT NULL CHECK (event IN ('registered', 'refreshed')),
		impu TEXT NOT NULL,
		registered_at INTEGER NOT NULL
	);
	CREATE UNIQUE INDEX bindings_flow ON bindings (registration_id, instance_id, reg_id) WHERE reg_id IS NOT NULL;
	CREATE UNIQUE INDEX bindings_uri ON bindings (registration_id, uri) WHERE reg_id IS NULL;
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
		instance_id TEXT,
		reg_id INTEGER CHECK (reg_id IS NULL OR (reg_id > 0 AND instance_id IS NOT NULL)),
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
		instance_id TEXT,
		reg_id INTEGER CHECK (reg_id IS NULL OR (reg_id > 0 AND instance_id IS NOT NULL)),
		pcscf_address TEXT NOT NULL,
		contacts TEXT NOT NULL,
		associated_uris TEXT NOT NULL,
		sets TEXT NOT NULL,
		service_route TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		policy_endpoint TEXT,
		policy_session_id TEXT,
		policy_ref TEXT,
		signalling_lost INTEGER NOT NULL CHECK (signalling_lost IN (0, 1))
	);
	CREATE UNIQUE INDEX pcscf_registrations_flow ON pcscf_registrations (impi, ue_address, instance_id, reg_id)
		WHERE reg_id IS NOT NULL;
	CREATE UNIQUE INDEX pcscf_registrations_ue ON pcscf_registrations (impi, ue_address) WHERE reg_id IS NULL;
	CREATE TABLE pcscf_subscriptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		impi TEXT NOT NULL UNIQUE,
		impu TEXT NOT NULL,
		call_id TEXT NOT NULL,
		local_tag TEXT NOT NULL,
		dialog BLOB,
		version INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE TABLE call_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		icid TEXT NOT NULL UNIQUE,
		session_id TEXT NOT NULL,
		calling_party TEXT,
		caller_impi TEXT,
		requested_party TEXT NOT NULL,
		called_party TEXT,
		callee_impi TEXT,
		requested_at INTEGER NOT NULL,
		delivery_start_at INTEGER,
		delivery_end_at INTEGER,
		sip_status INTEGER CHECK (sip_status BETWEEN 200 AND 699),
		outcome TEXT CHECK (outcome IN ('answered', 'cancelled', 'busy', 'rejected', 'no_answer', 'unavailable',
			'failed')),
		ended_by TEXT CHECK (ended_by IN ('caller', 'callee', 'network')),
		alerted INTEGER NOT NULL CHECK (alerted IN (0, 1)),
		media TEXT,
		incomplete INTEGER NOT NULL CHECK (incomplete IN (0, 1)),
		CHECK ((sip_status IS NULL) = (outcome IS NULL)),
		CHECK ((sip_status IS NULL) = (delivery_start_at IS NULL)),
		CHECK ((outcome = 'answered') = (sip_status BETWEEN 200 AND 299)),
		CHECK (ended_by IS NULL OR sip_status IS NOT NULL),
		CHECK (incomplete = 0 OR ended_by IS NULL),
		CHECK ((delivery_end_at IS NOT NULL) = (outcome = 'answered' AND ended_by IS NOT NULL))
	);
	CREATE INDEX call_records_requested_at ON call_records (requested_at, id);
	CREATE INDEX call_records_outcome ON call_records (outcome);
	CREATE INDEX call_records_session ON call_records (session_id, caller_impi);
	CREATE INDEX call_records_open ON call_records (id) WHERE ended_by IS NULL AND incomplete = 0;
	CREATE TABLE operator (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		mcc TEXT NOT NULL,
		mnc TEXT NOT NULL,
		country_code TEXT NOT NULL,
		national_prefix TEXT NOT NULL,
		international_prefix TEXT NOT NULL
	);
	INSERT INTO operator VALUES (1, '001', '01', '1', '1', '011');
	CREATE TABLE diameter_peers (
		id TEXT PRIMARY KEY,
		host TEXT NOT NULL COLLATE NOCASE,
		address TEXT NOT NULL,
		port INTEGER NOT NULL,
		transport TEXT NOT NULL,
		applications TEXT NOT NULL,
		priority INTEGER NOT NULL CHECK (priority BETWEEN 0 AND 65535)
	);
	CREATE UNIQUE INDEX diameter_peers_host ON diameter_peers (host) WHERE host != '';
	CREATE TABLE diameter_routes (
		application TEXT PRIMARY KEY CHECK (application IN ('cx', 'rx')),
		realm TEXT NOT NULL
	);
	INSERT INTO diameter_routes VALUES ('cx', ''), ('rx', '');
	CREATE TABLE policy (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		interface TEXT NOT NULL,
		pcf_uri TEXT NOT NULL
	);
	INSERT INTO policy VALUES (1, 'none', '');
	CREATE TABLE call_record_settings (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		retention_days INTEGER NOT NULL
	);
	INSERT INTO call_record_settings VALUES (1, 90);`,
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

	d := &DB{conn: conn, path: path, metrics: newMetrics()}
	if err := d.migrate(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	read, err := sql.Open("sqlite3", "file:"+path+"?_query_only=true&_busy_timeout=5000")
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}

	read.SetMaxOpenConns(maxReaders)

	if err := read.PingContext(ctx); err != nil {
		_ = read.Close()
		_ = conn.Close()

		return nil, fmt.Errorf("open database: %w", err)
	}

	d.read = read

	return d, nil
}

func (d *DB) Close() error {
	return errors.Join(d.read.Close(), d.conn.Close())
}

func (d *DB) migrate(ctx context.Context) error {
	var version int
	if err := d.conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	if version > len(migrations) {
		return fmt.Errorf("schema version %d is newer than this binary supports (%d): use a newer binary, or a new database",
			version, len(migrations))
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

func nullableInt(n int64) sql.NullInt64 {
	return sql.NullInt64{Int64: n, Valid: n != 0}
}
