package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrDuplicate = errors.New("duplicate")

func (d *DB) checkForeignKeys(ctx context.Context) error {
	rows, err := d.conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("check foreign keys: %w", err)
	}

	defer func() { _ = rows.Close() }()

	if rows.Next() {
		var (
			table  string
			rowid  sql.NullInt64
			parent string
			fkid   int
		)

		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("check foreign keys: %w", err)
		}

		return fmt.Errorf("database: a row of %s references no row of %s: the database was changed outside the IMS", table, parent)
	}

	return rows.Err()
}

func (d *DB) rekeyMCPTT(ctx context.Context) error {
	return d.inTx(ctx, "recompute MCPTT keys", func(tx *sql.Tx) error {
		for _, t := range []struct{ table, id, key string }{
			{"mcptt_users", "mcptt_id", "key"},
			{"mcptt_groups", "uri", "key"},
			{"mcptt_affiliation_clients", "mcptt_id", "user_key"},
		} {
			type change struct{ old, new string }

			var changes []change

			err := queryRows(ctx, tx, "recompute MCPTT keys", `SELECT `+t.id+`, `+t.key+` FROM `+t.table, func(rows *sql.Rows) error {
				var id, old string
				if err := rows.Scan(&id, &old); err != nil {
					return err
				}

				k, err := mcpttKey(id)
				if err != nil {
					return err
				}

				if k != old {
					changes = append(changes, change{old, k})
				}

				return nil
			})
			if err != nil {
				return err
			}

			for _, c := range changes {
				if _, err := tx.ExecContext(ctx, `UPDATE `+t.table+` SET `+t.key+` = ? WHERE `+t.key+` = ?`, c.new,
					c.old); err != nil {
					return fmt.Errorf("%s of key %s: %w", t.table, c.new, err)
				}
			}
		}

		return nil
	})
}

func (d *DB) inTx(ctx context.Context, what string, fn func(tx *sql.Tx) error) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	return nil
}
