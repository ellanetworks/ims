package db

import (
	"context"
	"crypto/rand"
	"fmt"
)

const jwtSecretLength = 32

// JWTSecret returns the key that signs the UI's access tokens. The first call generates and stores it, so that the
// tokens outlive a restart.
func (d *DB) JWTSecret(ctx context.Context) (_ []byte, err error) {
	defer d.observe(poolWrite, &err)()

	candidate := make([]byte, jwtSecretLength)
	_, _ = rand.Read(candidate) // It never fails.

	if _, err := d.conn.ExecContext(ctx, `INSERT OR IGNORE INTO jwt_secret (id, secret) VALUES (1, ?)`,
		candidate); err != nil {
		return nil, fmt.Errorf("get JWT secret: %w", err)
	}

	var secret []byte
	if err := d.conn.QueryRowContext(ctx, `SELECT secret FROM jwt_secret WHERE id = 1`).Scan(&secret); err != nil {
		return nil, fmt.Errorf("get JWT secret: %w", err)
	}

	return secret, nil
}
