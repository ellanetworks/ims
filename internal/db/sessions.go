package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Session is a login of a user to the UI. Only the SHA-256 hash of its token is stored.
type Session struct {
	ID        int64
	UserID    int64
	TokenHash []byte
	CreatedAt time.Time
	ExpiresAt time.Time
}

type NewSession struct {
	UserID    int64
	TokenHash []byte
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateSession stores a session, deletes the oldest sessions of its user beyond the newest maxPerUser, and returns
// its ID.
func (d *DB) CreateSession(ctx context.Context, s NewSession, maxPerUser int) (_ int64, err error) {
	defer d.observe(poolWrite, &err)()

	var id int64

	err = d.inTx(ctx, "create session", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO sessions (user_id, token_hash, created_at, expires_at)
			VALUES (?, ?, ?, ?)`, s.UserID, s.TokenHash, s.CreatedAt.UnixNano(), s.ExpiresAt.UnixNano())
		if err != nil {
			return err
		}

		if id, err = res.LastInsertId(); err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id NOT IN
			(SELECT id FROM sessions WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?)`,
			s.UserID, s.UserID, maxPerUser)

		return err
	})

	return id, err
}

// GetSessionByTokenHash returns a session, whether or not it has expired.
func (d *DB) GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (_ Session, err error) {
	defer d.observe(poolRead, &err)()

	return getSession(d.read.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE token_hash = ?`,
		tokenHash))
}

// GetSession returns a session, whether or not it has expired.
func (d *DB) GetSession(ctx context.Context, id int64) (_ Session, err error) {
	defer d.observe(poolRead, &err)()

	return getSession(d.read.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id))
}

const sessionColumns = `id, user_id, token_hash, created_at, expires_at`

func getSession(row *sql.Row) (Session, error) {
	var (
		s                    Session
		createdAt, expiresAt int64
	)

	err := row.Scan(&s.ID, &s.UserID, &s.TokenHash, &createdAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}

	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}

	s.CreatedAt = time.Unix(0, createdAt).UTC()
	s.ExpiresAt = time.Unix(0, expiresAt).UTC()

	return s, nil
}

// DeleteSessionByTokenHash deletes a session. Deleting one that does not exist is not an error, so that logging out
// twice is not one either.
func (d *DB) DeleteSessionByTokenHash(ctx context.Context, tokenHash []byte) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

// DeleteExpiredSessions deletes the sessions that expire at or before now.
func (d *DB) DeleteExpiredSessions(ctx context.Context, now time.Time) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.UnixNano()); err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}

	return nil
}
