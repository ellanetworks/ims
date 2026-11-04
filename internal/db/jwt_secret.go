package db

import (
	"context"
	"crypto/rand"
	"fmt"
)

const jwtSecretLength = 32

func (d *DB) JWTSecret(ctx context.Context) (_ []byte, err error) {
	defer d.observe(poolWrite, &err)()

	return d.singletonSecret(ctx, "get JWT secret", "jwt_secret", "secret", jwtSecretLength)
}

func (d *DB) singletonSecret(ctx context.Context, what, table, column string, length int) ([]byte, error) {
	candidate := make([]byte, length)
	_, _ = rand.Read(candidate)

	if _, err := d.conn.ExecContext(ctx, `INSERT OR IGNORE INTO `+table+` (id, `+column+`) VALUES (1, ?)`,
		candidate); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}

	var secret []byte
	if err := d.conn.QueryRowContext(ctx, `SELECT `+column+` FROM `+table+` WHERE id = 1`).Scan(&secret); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}

	return secret, nil
}
