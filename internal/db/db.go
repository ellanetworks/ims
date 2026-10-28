package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
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
	`CREATE TABLE implicit_sets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_data BLOB NOT NULL,
		hss_host TEXT NOT NULL,
		hss_realm TEXT NOT NULL,
		hss_features INTEGER NOT NULL DEFAULT 0,
		unregistered_identity TEXT,
		wildcard TEXT,
		expires_at INTEGER,
		CHECK ((unregistered_identity IS NULL) = (expires_at IS NULL))
	);
	CREATE INDEX implicit_sets_expires_at ON implicit_sets (expires_at) WHERE expires_at IS NOT NULL;
	CREATE TABLE set_identities (
		set_id INTEGER NOT NULL REFERENCES implicit_sets (id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		uri TEXT NOT NULL,
		key TEXT NOT NULL UNIQUE,
		display_name TEXT,
		barred INTEGER NOT NULL CHECK (barred IN (0, 1)),
		PRIMARY KEY (set_id, position)
	);
	CREATE TABLE registrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		impi TEXT NOT NULL,
		impu TEXT NOT NULL,
		set_id INTEGER NOT NULL REFERENCES implicit_sets (id),
		UNIQUE (impi, set_id)
	);
	CREATE INDEX registrations_set ON registrations (set_id);
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
	CREATE INDEX bindings_registration ON bindings (registration_id);
	CREATE TABLE reg_subscriptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		impi TEXT NOT NULL,
		impu TEXT NOT NULL,
		subscriber TEXT NOT NULL CHECK (subscriber IN ('ue', 'pcscf', 'as')),
		call_id TEXT NOT NULL,
		local_tag TEXT NOT NULL,
		remote_tag TEXT NOT NULL,
		remote_target TEXT NOT NULL,
		dialog BLOB NOT NULL,
		version INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		icid TEXT NOT NULL,
		term_ioi TEXT NOT NULL,
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
		priority INTEGER NOT NULL CHECK (priority BETWEEN 0 AND 65535),
		service_centres TEXT NOT NULL
	);
	CREATE UNIQUE INDEX diameter_peers_host ON diameter_peers (host) WHERE host != '';
	CREATE TABLE diameter_routes (
		application TEXT PRIMARY KEY CHECK (application IN ('cx', 'rx', 'sh', 's6c')),
		realm TEXT NOT NULL
	);
	INSERT INTO diameter_routes VALUES ('cx', ''), ('rx', ''), ('sh', ''), ('s6c', '');
	CREATE TABLE policy (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		interface TEXT NOT NULL,
		pcf_uri TEXT NOT NULL
	);
	INSERT INTO policy VALUES (1, 'rx', '');
	CREATE TABLE call_record_settings (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		retention_days INTEGER NOT NULL
	);
	INSERT INTO call_record_settings VALUES (1, 90);
	-- The MCPTT application server (mcptt_*.go). Its settings, but its PSIs, which are of the home domain, and but what
	-- the server fixes to the defaults of the MCPTT settings package (the floor control timers but T2, a user's N2 and
	-- N6, a group's call rules and encodings, a member's priority and acknowledgement): an optional element absent
	-- from them is NULL, so that a later release's defaults apply; the domain of the organisation empty, the home
	-- domain's. A duration is in
	-- nanoseconds, of a column named _ns. Members and implicit affiliations reference rows by id: a user deleted
	-- leaves its groups, and an implicit affiliation is of a member of the group, which goes with its membership. key is the identity as the MCPTT settings
	-- package's Key has it, by which a user or group is found. Its runtime state through the IMS, kept across
	-- restarts: the bindings of service authorisation, a client at one IMPU at a time, and the affiliations of the
	-- clients. They are keyed by identity, not by row of users: the server reconciles them with the settings when it
	-- restores them. Their times are Unix milliseconds.
	CREATE TABLE mcptt_service_configuration (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		domain TEXT NOT NULL,
		time_limit_ns INTEGER
	);
	INSERT INTO mcptt_service_configuration VALUES (1, '', NULL);
	CREATE TABLE mcptt_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		mcptt_id TEXT NOT NULL,
		key TEXT NOT NULL UNIQUE,
		alias TEXT NOT NULL
	);
	CREATE TABLE mcptt_user_impus (
		user_id INTEGER NOT NULL REFERENCES mcptt_users (id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		impu TEXT NOT NULL,
		PRIMARY KEY (user_id, position)
	);
	CREATE TABLE mcptt_groups (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uri TEXT NOT NULL,
		key TEXT NOT NULL UNIQUE,
		display_name TEXT NOT NULL,
		t4_ns INTEGER,
		tng3_ns INTEGER NOT NULL,
		release_on_initiator_leave INTEGER NOT NULL CHECK (release_on_initiator_leave IN (0, 1))
	);
	CREATE TABLE mcptt_group_members (
		group_id INTEGER NOT NULL REFERENCES mcptt_groups (id) ON DELETE CASCADE,
		user_id INTEGER NOT NULL REFERENCES mcptt_users (id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		recvonly INTEGER NOT NULL CHECK (recvonly IN (0, 1)),
		PRIMARY KEY (group_id, user_id)
	);
	CREATE INDEX mcptt_group_members_user_id ON mcptt_group_members (user_id);
	CREATE TABLE mcptt_user_implicit_affiliations (
		user_id INTEGER NOT NULL,
		group_id INTEGER NOT NULL,
		position INTEGER NOT NULL,
		PRIMARY KEY (user_id, group_id),
		FOREIGN KEY (group_id, user_id) REFERENCES mcptt_group_members (group_id, user_id) ON DELETE CASCADE
	);
	CREATE INDEX mcptt_user_implicit_affiliations_group_id ON mcptt_user_implicit_affiliations (group_id);
	CREATE TABLE mcptt_registrations (
		client_id TEXT PRIMARY KEY,
		mcptt_id TEXT NOT NULL,
		impu TEXT NOT NULL,
		impus TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		since INTEGER NOT NULL,
		scscf TEXT NOT NULL,
		as_uri TEXT NOT NULL,
		token TEXT NOT NULL,
		instance TEXT NOT NULL,
		reg_id TEXT NOT NULL,
		contact TEXT NOT NULL,
		call_id TEXT NOT NULL,
		cseq INTEGER NOT NULL,
		icid TEXT NOT NULL,
		orig_ioi TEXT NOT NULL,
		charging_function_addresses TEXT NOT NULL
	);
	CREATE TABLE mcptt_affiliation_clients (
		user_key TEXT NOT NULL,
		client_id TEXT NOT NULL,
		mcptt_id TEXT NOT NULL,
		etag TEXT NOT NULL,
		PRIMARY KEY (user_key, client_id)
	);
	CREATE TABLE mcptt_affiliations (
		user_key TEXT NOT NULL,
		client_id TEXT NOT NULL,
		position INTEGER NOT NULL,
		group_uri TEXT NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('affiliating', 'affiliated')),
		expires_at INTEGER NOT NULL,
		pid TEXT NOT NULL,
		configured INTEGER NOT NULL CHECK (configured IN (0, 1)),
		PRIMARY KEY (user_key, client_id, position),
		FOREIGN KEY (user_key, client_id) REFERENCES mcptt_affiliation_clients (user_key, client_id)
			ON DELETE CASCADE ON UPDATE CASCADE
	);
	-- The IP-SM-GW (ipsmgw.go). Its number, empty until set; the registrations of the third-party REGISTERs, each of
	-- an IMPU; and the users the HSS has the IP-SM-GW registered for, of the number it was given.
	CREATE TABLE ip_sm_gw (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		number TEXT NOT NULL
	);
	INSERT INTO ip_sm_gw VALUES (1, '');
	CREATE TABLE ip_sm_gw_registrations (
		impu_key TEXT PRIMARY KEY,
		impu TEXT NOT NULL,
		impus TEXT NOT NULL,
		msisdn TEXT NOT NULL,
		imsi TEXT NOT NULL,
		scscf TEXT NOT NULL,
		as_uri TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		available INTEGER NOT NULL CHECK (available IN (0, 1))
	);
	CREATE TABLE ip_sm_gw_hss_registrations (
		user_key TEXT PRIMARY KEY,
		msisdn TEXT NOT NULL,
		impu TEXT NOT NULL,
		number TEXT NOT NULL CHECK (number != '')
	);
	-- The users of the API and the UI (users.go), their sessions in the UI (sessions.go), their API tokens
	-- (api_tokens.go), and the key that signs the access tokens (jwt_secret.go). Only hashes of the secrets are stored.
	-- Times are Unix nanoseconds.
	CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL UNIQUE COLLATE NOCASE,
		role_id INTEGER NOT NULL CHECK (role_id IN (1, 2, 3)),
		password_hash TEXT NOT NULL
	);
	CREATE TABLE sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
		token_hash BLOB NOT NULL UNIQUE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE INDEX sessions_user_id ON sessions (user_id, created_at);
	CREATE INDEX sessions_expires_at ON sessions (expires_at);
	CREATE TABLE api_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		token_hash BLOB NOT NULL,
		user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER,
		last_used_at INTEGER,
		UNIQUE (user_id, name)
	);
	CREATE TABLE jwt_secret (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		secret BLOB NOT NULL CHECK (length(secret) >= 32)
	);`,
}

func Open(ctx context.Context, path string) (*DB, error) {
	// The path of a URI: escaped, of a "?", "#" or "%" that would end or escape it (https://sqlite.org/uri.html).
	file := (&url.URL{Path: path}).EscapedPath()

	if err := restrict(path); err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	conn, err := sql.Open("sqlite3", "file:"+file+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_stmt_cache_size=64")
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

	if err := d.checkForeignKeys(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	if err := d.rekeyMCPTT(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	read, err := sql.Open("sqlite3", "file:"+file+"?_query_only=true&_busy_timeout=5000&_stmt_cache_size=64")
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

// Ping checks that the database answers. It is not observed: see TestEveryCallIsObserved.
func (d *DB) Ping(ctx context.Context) error {
	return d.conn.PingContext(ctx)
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

// restrict creates the database file if absent, readable and writable by its owner only, and makes an existing one so:
// it holds the password hashes and the key that signs the access tokens. SQLite gives its journal files the
// permissions of the database file.
func restrict(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	return os.Chmod(path, 0o600)
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
