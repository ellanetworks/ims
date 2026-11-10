package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrDuplicate = errors.New("duplicate")

func checkForeignKeys(ctx context.Context, q querier) error {
	rows, err := q.QueryContext(ctx, "PRAGMA foreign_key_check")
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
