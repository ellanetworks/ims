package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/mattn/go-sqlite3"
)

var excludedTables = []string{
	"jwt_secret", "oidc_cookie_key",
	"bindings", "registrations", "set_identities", "implicit_sets", "reg_subscriptions", "security_associations",
	"pcscf_registrations", "pcscf_subscriptions",
	"mcptt_registrations", "mcptt_affiliations", "mcptt_affiliation_clients",
	"ip_sm_gw_registrations", "ip_sm_gw_hss_registrations",
	"sessions", "oidc_used_states",
}

type Copy struct {
	Path   string
	Schema int
}

func SchemaVersion() int {
	return len(migrations)
}

func (d *DB) CopyTo(ctx context.Context, dir string) (_ Copy, err error) {
	defer d.observe(poolRead, &err)()

	f, err := os.CreateTemp(dir, ".backup-*.db")
	if err != nil {
		return Copy{}, fmt.Errorf("copy database: %w", err)
	}

	c := Copy{Path: f.Name()}

	if err := f.Close(); err != nil {
		_ = os.Remove(c.Path)
		return Copy{}, fmt.Errorf("copy database: %w", err)
	}

	if err := d.copyTo(ctx, c.Path, &c.Schema); err != nil {
		_ = os.Remove(c.Path)
		return Copy{}, fmt.Errorf("copy database: %w", err)
	}

	return c, nil
}

func (d *DB) copyTo(ctx context.Context, dest string, schema *int) error {
	conn, err := d.read.Conn(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		return err
	}

	defer func() { _, _ = conn.ExecContext(context.Background(), `ROLLBACK`) }()

	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(schema); err != nil {
		return err
	}

	destDB, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: dest}).EscapedPath())
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

func StripForBackup(ctx context.Context, path string) error {
	conn, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: path}).EscapedPath()+"?_secure_delete=on")
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	for _, t := range excludedTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	return conn.Close()
}
