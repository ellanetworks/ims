package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type SessionMethod string

const (
	SessionPassword SessionMethod = "password"
	SessionOIDC     SessionMethod = "oidc"
)

type Session struct {
	ID         int64
	UserID     int64
	TokenHash  []byte
	Method     SessionMethod
	IdentityID int64
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

type NewSession struct {
	UserID     int64
	TokenHash  []byte
	Method     SessionMethod
	IdentityID int64
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

func (d *DB) CreateSession(ctx context.Context, s NewSession, maxPerUser int) (_ int64, err error) {
	defer d.observe(poolWrite, &err)()

	var id int64

	err = d.inTx(ctx, "create session", func(tx *sql.Tx) error {
		id, err = createSession(ctx, tx, s, maxPerUser)
		return err
	})

	return id, err
}

func createSession(ctx context.Context, tx *sql.Tx, s NewSession, maxPerUser int) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO sessions (user_id, token_hash, method, identity_id, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`, s.UserID, s.TokenHash, s.Method, sql.NullInt64{Int64: s.IdentityID, Valid: s.IdentityID != 0},
		s.CreatedAt.UnixNano(), s.ExpiresAt.UnixNano())
	if err != nil {
		return 0, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id NOT IN
		(SELECT id FROM sessions WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?)`,
		s.UserID, s.UserID, maxPerUser)

	return id, err
}

func (d *DB) GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (_ Session, err error) {
	defer d.observe(poolRead, &err)()

	return getSession(d.read.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE token_hash = ?`,
		tokenHash))
}

func (d *DB) GetSession(ctx context.Context, id int64) (_ Session, err error) {
	defer d.observe(poolRead, &err)()

	return getSession(d.read.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id))
}

const sessionColumns = `id, user_id, token_hash, method, identity_id, created_at, expires_at`

func getSession(row *sql.Row) (Session, error) {
	var (
		s                    Session
		identityID           sql.NullInt64
		createdAt, expiresAt int64
	)

	err := row.Scan(&s.ID, &s.UserID, &s.TokenHash, &s.Method, &identityID, &createdAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}

	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}

	s.IdentityID = identityID.Int64
	s.CreatedAt = time.Unix(0, createdAt).UTC()
	s.ExpiresAt = time.Unix(0, expiresAt).UTC()

	return s, nil
}

func (d *DB) DeleteSessionByTokenHash(ctx context.Context, tokenHash []byte) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

func (d *DB) DeleteExpiredSessions(ctx context.Context, now time.Time) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.UnixNano()); err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}

	return nil
}
