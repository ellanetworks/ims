package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const (
	jwtSecretLength     = 32
	oidcCookieKeyLength = 32
	joinKeyLength       = 32
)

type Secrets struct {
	JWT           []byte
	OIDCCookieKey []byte
	ClusterID     string
	JoinKey       []byte
}

func (d *DB) Secrets(ctx context.Context) (Secrets, error) {
	return d.InitSecrets(ctx, d.currentProposer())
}

func (d *DB) ReadSecrets(ctx context.Context) (_ Secrets, err error) {
	defer d.observe(poolRead, &err)()

	return readSecrets(ctx, d.reader())
}

func (d *DB) InitSecrets(ctx context.Context, via Proposer) (_ Secrets, err error) {
	defer d.observe(poolWrite, &err)()

	s, err := readSecrets(ctx, d.reader())
	if err == nil {
		return s, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return Secrets{}, fmt.Errorf("read secrets: %w", err)
	}

	candidate := Secrets{
		JWT:           make([]byte, jwtSecretLength),
		OIDCCookieKey: make([]byte, oidcCookieKeyLength),
		ClusterID:     uuid.Must(uuid.NewV7()).String(),
		JoinKey:       make([]byte, joinKeyLength),
	}
	_, _ = rand.Read(candidate.JWT)
	_, _ = rand.Read(candidate.OIDCCookieKey)
	_, _ = rand.Read(candidate.JoinKey)

	s, _, err = submitVia[Secrets](ctx, via, cmdInitSecrets, candidate)
	if err != nil {
		return Secrets{}, fmt.Errorf("init secrets: %w", err)
	}

	return s, nil
}

func (d *DB) JWTSecret(ctx context.Context) ([]byte, error) {
	s, err := d.Secrets(ctx)
	return s.JWT, err
}

func (d *DB) OIDCCookieKey(ctx context.Context) ([]byte, error) {
	s, err := d.Secrets(ctx)
	return s.OIDCCookieKey, err
}

func applyInitSecrets(a *applyCtx, candidate Secrets) (Secrets, error) {
	ctx, tx := a.ctx, a.tx

	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO jwt_secret (id, secret) VALUES (1, ?)`,
		candidate.JWT); err != nil {
		return Secrets{}, err
	}

	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO oidc_cookie_key (id, key) VALUES (1, ?)`,
		candidate.OIDCCookieKey); err != nil {
		return Secrets{}, err
	}

	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO cluster (id, cluster_id, join_key) VALUES (1, ?, ?)`,
		candidate.ClusterID, candidate.JoinKey); err != nil {
		return Secrets{}, err
	}

	return readSecrets(ctx, tx)
}

func readSecrets(ctx context.Context, q querier) (Secrets, error) {
	var s Secrets

	err := q.QueryRowContext(ctx, `SELECT j.secret, o.key, c.cluster_id, c.join_key
		FROM jwt_secret j, oidc_cookie_key o, cluster c WHERE j.id = 1 AND o.id = 1 AND c.id = 1`).
		Scan(&s.JWT, &s.OIDCCookieKey, &s.ClusterID, &s.JoinKey)

	return s, err
}
