package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mattn/go-sqlite3"
)

type Position struct {
	Index              uint64
	Term               uint64
	Configuration      []byte
	ConfigurationIndex uint64
	Schema             int `json:"-"`
}

func readPosition(ctx context.Context, q querier) (Position, error) {
	var p Position

	err := q.QueryRowContext(ctx, `SELECT applied_index, applied_term, configuration, configuration_index
		FROM fsm_state WHERE id = 1`).Scan(&p.Index, &p.Term, &p.Configuration, &p.ConfigurationIndex)

	return p, err
}

func (d *DB) Position(ctx context.Context) (_ Position, err error) {
	defer d.observe(poolRead, &err)()

	return readPosition(ctx, d.reader())
}

func (d *DB) RecordConfiguration(index, term uint64, configuration []byte) error {
	d.applyMu.Lock()
	defer d.applyMu.Unlock()

	if index <= d.applied.Load() {
		return nil
	}

	if _, err := d.writer.ExecContext(context.Background(), `UPDATE fsm_state SET applied_index = ?, applied_term = ?,
		configuration = ?, configuration_index = ? WHERE id = 1`, index, term, configuration, index); err != nil {
		return &FatalError{Index: index, Err: err}
	}

	d.setApplied(index)

	return nil
}

func (d *DB) MakeDurable(ctx context.Context, p Position) error {
	d.applyMu.Lock()
	defer d.applyMu.Unlock()

	conn, err := d.writer.Conn(ctx)
	if err != nil {
		return fmt.Errorf("make durable: %w", err)
	}

	defer func() { _ = conn.Close() }()

	var previous int
	if err := conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&previous); err != nil {
		return fmt.Errorf("make durable: %w", err)
	}

	if _, err := conn.ExecContext(ctx, `PRAGMA synchronous = FULL`); err != nil {
		return fmt.Errorf("make durable: %w", err)
	}

	defer func() {
		_, _ = conn.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA synchronous = %d`, previous))
	}()

	if _, err := conn.ExecContext(ctx, `UPDATE fsm_state SET
		applied_term = CASE WHEN applied_index < ?1 THEN ?2 ELSE applied_term END,
		applied_index = MAX(applied_index, ?1),
		configuration = CASE WHEN ?3 IS NOT NULL AND configuration_index <= ?4 THEN ?3 ELSE configuration END,
		configuration_index = CASE WHEN ?3 IS NOT NULL AND configuration_index <= ?4 THEN ?4 ELSE configuration_index END,
		durable_writes = durable_writes + 1
		WHERE id = 1`, p.Index, p.Term, p.Configuration, p.ConfigurationIndex); err != nil {
		return fmt.Errorf("make durable: %w", err)
	}

	d.setApplied(max(d.applied.Load(), p.Index))

	return nil
}

func (d *DB) ResetPosition(ctx context.Context) error {
	d.applyMu.Lock()
	defer d.applyMu.Unlock()

	if _, err := d.writer.ExecContext(ctx, `UPDATE fsm_state SET applied_index = 0, applied_term = 0, configuration = NULL,
		configuration_index = 0 WHERE id = 1`); err != nil {
		return fmt.Errorf("reset position: %w", err)
	}

	if _, err := d.writer.ExecContext(ctx, `DELETE FROM applied_requests`); err != nil {
		return fmt.Errorf("reset position: %w", err)
	}

	d.setApplied(0)

	return nil
}

func (d *DB) LeaderStart(ctx context.Context, via Proposer, nodeID string) (_ uint64, err error) {
	defer d.observe(poolWrite, &err)()

	term, _, err := submitVia[uint64](ctx, via, cmdLeaderStart, nodeID)
	if err != nil {
		return 0, fmt.Errorf("leader start: %w", err)
	}

	return term, nil
}

func applyLeaderStart(a *applyCtx, _ string) (uint64, error) {
	return a.term, nil
}

const (
	snapshotMagic         = "IMSN"
	snapshotHeaderSize    = 16
	snapshotFormatVersion = 1
	ProtocolVersion       = 2
)

var ErrSnapshotHeader = errors.New("unsupported snapshot")

func SnapshotHeader(schema int) []byte {
	h := make([]byte, snapshotHeaderSize)
	copy(h, snapshotMagic)
	binary.BigEndian.PutUint32(h[4:], snapshotFormatVersion)
	binary.BigEndian.PutUint32(h[8:], uint32(schema))
	binary.BigEndian.PutUint32(h[12:], ProtocolVersion)

	return h
}

func checkSnapshotHeader(h []byte) error {
	switch {
	case len(h) != snapshotHeaderSize || !bytes.Equal(h[:4], []byte(snapshotMagic)):
		return fmt.Errorf("%w: not an IMS snapshot", ErrSnapshotHeader)
	case binary.BigEndian.Uint32(h[4:]) > snapshotFormatVersion:
		return fmt.Errorf("%w: format %d", ErrSnapshotHeader, binary.BigEndian.Uint32(h[4:]))
	case binary.BigEndian.Uint32(h[8:]) > uint32(len(migrations)):
		return fmt.Errorf("%w: schema %d is newer than this binary's %d", ErrSnapshotHeader,
			binary.BigEndian.Uint32(h[8:]), len(migrations))
	case binary.BigEndian.Uint32(h[12:]) > ProtocolVersion:
		return fmt.Errorf("%w: protocol %d", ErrSnapshotHeader, binary.BigEndian.Uint32(h[12:]))
	}

	return nil
}

type Copy struct {
	Position Position
	Path     string
}

func (d *DB) CopyTo(ctx context.Context, dir string) (_ Copy, err error) {
	defer d.observe(poolWrite, &err)()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Copy{}, fmt.Errorf("copy database: %w", err)
	}

	f, err := os.CreateTemp(dir, "copy-*.db")
	if err != nil {
		return Copy{}, fmt.Errorf("copy database: %w", err)
	}

	dest := f.Name()
	_ = f.Close()

	c := Copy{Path: dest}

	if err := d.copyPinned(ctx, dest, &c.Position); err != nil {
		_ = os.Remove(dest)
		return Copy{}, fmt.Errorf("copy database: %w", err)
	}

	return c, nil
}

func (d *DB) copyPinned(ctx context.Context, dest string, p *Position) error {
	conn, err := d.reader().Conn(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		return err
	}

	defer func() { _, _ = conn.ExecContext(context.Background(), `ROLLBACK`) }()

	if *p, err = readPosition(ctx, conn); err != nil {
		return err
	}

	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&p.Schema); err != nil {
		return err
	}

	destDB, err := sql.Open("sqlite3", dsn(dest))
	if err != nil {
		return err
	}

	defer func() { _ = destDB.Close() }()

	destConn, err := destDB.Conn(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = destConn.Close() }()

	return conn.Raw(func(src any) error {
		srcRaw, ok := src.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("unexpected driver connection %T", src)
		}

		return destConn.Raw(func(dst any) error {
			dstRaw, ok := dst.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("unexpected driver connection %T", dst)
			}

			backup, err := dstRaw.Backup("main", srcRaw, "main")
			if err != nil {
				return err
			}

			done, err := backup.Step(-1)
			if err != nil {
				_ = backup.Finish()
				return err
			}

			if !done {
				_ = backup.Finish()
				return errors.New("backup did not copy every page")
			}

			return backup.Finish()
		})
	})
}

func ReadSnapshot(r io.Reader, dir string) (string, error) {
	h := make([]byte, snapshotHeaderSize)
	if _, err := io.ReadFull(r, h); err != nil {
		return "", fmt.Errorf("read snapshot header: %w", err)
	}

	if err := checkSnapshotHeader(h); err != nil {
		return "", err
	}

	f, err := os.CreateTemp(dir, "install-*.db")
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())

		return "", fmt.Errorf("write snapshot: %w", err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())

		return "", fmt.Errorf("write snapshot: %w", err)
	}

	return f.Name(), f.Close()
}

func (d *DB) Install(ctx context.Context, src string, at *Position) (err error) {
	defer d.observe(poolWrite, &err)()

	if err := d.install(ctx, src, at); err != nil {
		return err
	}

	d.publish(Events{Replaced: true, Settings: true, OIDC: true, MCPTT: true, Members: true})

	return nil
}

func (d *DB) install(ctx context.Context, src string, at *Position) error {
	d.applyMu.Lock()
	defer d.applyMu.Unlock()

	if at != nil {
		if err := stampPosition(ctx, src, *at); err != nil {
			return fmt.Errorf("install database: %w", err)
		}
	}

	backup := replacedPath(d.path)

	if err := d.closeWriter(); err != nil {
		_ = d.openConns(ctx)
		return fmt.Errorf("install database: %w", err)
	}

	if err := os.Rename(d.path, backup); err != nil {
		_ = d.openConns(ctx)
		return fmt.Errorf("install database: %w", err)
	}

	restore := func(cause error) error {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(d.path + suffix)
		}

		_ = os.Rename(backup, d.path)

		if err := d.openConns(ctx); err != nil {
			return errors.Join(fmt.Errorf("install database: %w", cause), err)
		}

		return fmt.Errorf("install database: %w", cause)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(d.path + suffix)
	}

	if err := os.Rename(src, d.path); err != nil {
		return restore(err)
	}

	syncDir(filepath.Dir(d.path))

	if err := d.openConns(ctx); err != nil {
		return restore(err)
	}

	_ = os.Remove(backup)

	state, err := readFSMState(ctx, d.writer)
	if err != nil {
		return fmt.Errorf("install database: %w", err)
	}

	d.setApplied(state.appliedIndex)
	d.leaderTime.Store(state.leaderTime)

	if l, ok := d.currentProposer().(*localProposer); ok {
		l.mu.Lock()
		l.last = max(l.last, state.leaderTime)
		l.mu.Unlock()
	}

	return nil
}

func replacedPath(path string) string {
	return path + ".replaced"
}

func recoverInstall(path string) error {
	backup := replacedPath(path)

	if _, err := os.Stat(backup); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}

	if _, err := os.Stat(path); err == nil {
		return os.Remove(backup)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	if err := os.Rename(backup, path); err != nil {
		return err
	}

	syncDir(filepath.Dir(path))

	return nil
}

func stampPosition(ctx context.Context, path string, p Position) error {
	db, err := sql.Open("sqlite3", dsn(path))
	if err != nil {
		return err
	}

	defer func() { _ = db.Close() }()

	_, err = db.ExecContext(ctx, `UPDATE fsm_state SET applied_index = ?, applied_term = ?, configuration = ?,
		configuration_index = ? WHERE id = 1`, p.Index, p.Term, p.Configuration, p.ConfigurationIndex)

	return err
}

func syncDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
}

func SchemaVersion() int {
	return len(migrations)
}

func (d *DB) Schema() int {
	return int(d.schema.Load())
}

func Sequences(ctx context.Context, path string) (map[string]int64, error) {
	conn, err := sql.Open("sqlite3", dsn(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}

	defer func() { _ = conn.Close() }()

	return sequences(ctx, conn)
}

func (d *DB) Sequences(ctx context.Context) (_ map[string]int64, err error) {
	defer d.observe(poolRead, &err)()

	return sequences(ctx, d.reader())
}

func sequences(ctx context.Context, q querier) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, seq FROM sqlite_sequence ORDER BY name`)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	out := map[string]int64{}

	for rows.Next() {
		var (
			name string
			seq  int64
		)

		if err := rows.Scan(&name, &seq); err != nil {
			return nil, err
		}

		out[name] = seq
	}

	return out, rows.Err()
}

func RaiseSequences(ctx context.Context, path string, floor map[string]int64) error {
	conn, err := sql.Open("sqlite3", dsn(path))
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	for name, seq := range floor {
		res, err := conn.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = MAX(seq, ?) WHERE name = ?`, seq, name)
		if err != nil {
			return err
		}

		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			if _, err := conn.ExecContext(ctx, `INSERT INTO sqlite_sequence (name, seq) VALUES (?, ?)`, name, seq); err != nil {
				return err
			}
		}
	}

	return nil
}

func (d *DB) Wipe(ctx context.Context, scratch string) (err error) {
	defer d.observe(poolWrite, &err)()

	path := filepath.Join(scratch, "empty.db")

	_ = os.Remove(path)

	if err := createEmpty(ctx, path); err != nil {
		return fmt.Errorf("create an empty database: %w", err)
	}

	if err := d.clearLocal(ctx); err != nil {
		return err
	}

	return d.Install(ctx, path, &Position{})
}

func createEmpty(ctx context.Context, path string) error {
	conn, err := sql.Open("sqlite3", dsn(path)+"?"+dsnParams)
	if err != nil {
		return err
	}

	if err := migrateFile(ctx, conn, migrations[:baselineSchema], "the database"); err != nil {
		_ = conn.Close()
		return err
	}

	return conn.Close()
}

var localTables = []string{
	"reg_subscriptions", "bindings", "registrations", "set_identities", "implicit_sets", "mcptt_affiliations",
	"mcptt_affiliation_clients", "mcptt_registrations", "security_associations", "pcscf_registrations",
	"pcscf_subscriptions", "ip_sm_gw_registrations", "ip_sm_gw_hss_registrations",
}

func (d *DB) clearLocal(ctx context.Context) error {
	tx, err := d.local.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("clear the node database: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	for _, t := range localTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t); err != nil {
			return fmt.Errorf("clear the node database: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clear the node database: %w", err)
	}

	return nil
}

var (
	secretTables   = []string{"jwt_secret", "oidc_cookie_key", "cluster", "cluster_join_tokens"}
	clusterTables  = append([]string{"cluster_members", "cluster_removed_members"}, secretTables...)
	strippedTables = append(append([]string{}, secretTables...), runtimeTables...)
)

func StripSecrets(ctx context.Context, path string) error {
	conn, err := sql.Open("sqlite3", dsn(path)+"?_secure_delete=on")
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	for _, t := range strippedTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	return conn.Close()
}

func (d *DB) KeepClusterState(ctx context.Context, path string) (err error) {
	defer d.observe(poolWrite, &err)()

	conn, err := sql.Open("sqlite3", dsn(path))
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	c, err := conn.Conn(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = c.Close() }()

	if _, err := c.ExecContext(ctx, `ATTACH DATABASE ? AS live`, dsn(d.path)+"?mode=ro"); err != nil {
		return err
	}

	defer func() { _, _ = c.ExecContext(context.Background(), `DETACH DATABASE live`) }()

	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	for _, t := range clusterTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM main.`+t); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO main.`+t+` SELECT * FROM live.`+t); err != nil {
			return err
		}
	}

	return tx.Commit()
}
