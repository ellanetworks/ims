package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mattn/go-sqlite3"
)

// APIToken is a token with which a program calls the API as a user. Only the SHA-256 hash of its secret is stored;
// TokenID finds it.
type APIToken struct {
	ID        int64
	TokenID   string
	Name      string
	TokenHash []byte
	UserID    int64
	CreatedAt time.Time
	// ExpiresAt is zero for a token that does not expire.
	ExpiresAt time.Time
	// LastUsedAt is zero for a token never used.
	LastUsedAt time.Time
}

type NewAPIToken struct {
	TokenID   string
	Name      string
	TokenHash []byte
	UserID    int64
	CreatedAt time.Time
	// ExpiresAt is zero for a token that does not expire.
	ExpiresAt time.Time
}

// CreateAPIToken stores a token, or returns ErrDuplicate when its user has one of the same name, or when one has the
// same token ID.
func (d *DB) CreateAPIToken(ctx context.Context, t NewAPIToken) (_ int64, err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `INSERT INTO api_tokens (token_id, name, token_hash, user_id, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`, t.TokenID, t.Name, t.TokenHash, t.UserID, t.CreatedAt.UnixNano(), nullTime(t.ExpiresAt))
	if isConstraint(err, sqlite3.ErrConstraintUnique) {
		return 0, ErrDuplicate
	}

	if err != nil {
		return 0, fmt.Errorf("create API token: %w", err)
	}

	return res.LastInsertId()
}

// GetAPITokenByTokenID returns a token, whether or not it has expired.
func (d *DB) GetAPITokenByTokenID(ctx context.Context, tokenID string) (_ APIToken, err error) {
	defer d.observe(poolRead, &err)()

	t, err := scanAPIToken(d.read.QueryRowContext(ctx, `SELECT `+apiTokenColumns+` FROM api_tokens WHERE token_id = ?`,
		tokenID))
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}

	if err != nil {
		return APIToken{}, fmt.Errorf("get API token: %w", err)
	}

	return t, nil
}

// ListAPITokens returns a page of the tokens of a user, oldest first, and their total.
func (d *DB) ListAPITokens(ctx context.Context, userID int64, page, perPage int) (_ []APIToken, _ int, err error) {
	defer d.observe(poolRead, &err)()

	var total int
	if err := d.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE user_id = ?`, userID).
		Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list API tokens: %w", err)
	}

	rows, err := d.read.QueryContext(ctx, `SELECT `+apiTokenColumns+` FROM api_tokens WHERE user_id = ?
		ORDER BY id LIMIT ? OFFSET ?`, userID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("list API tokens: %w", err)
	}

	defer func() { _ = rows.Close() }()

	tokens := []APIToken{}

	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("list API tokens: %w", err)
		}

		tokens = append(tokens, t)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list API tokens: %w", err)
	}

	return tokens, total, nil
}

func (d *DB) CountAPITokens(ctx context.Context, userID int64) (_ int, err error) {
	defer d.observe(poolRead, &err)()

	var n int
	if err := d.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE user_id = ?`, userID).
		Scan(&n); err != nil {
		return 0, fmt.Errorf("count API tokens: %w", err)
	}

	return n, nil
}

// DeleteAPIToken deletes a token of a user, or returns ErrNotFound when the user has no token with that token ID.
func (d *DB) DeleteAPIToken(ctx context.Context, userID int64, tokenID string) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM api_tokens WHERE token_id = ? AND user_id = ?`, tokenID, userID)
	if err != nil {
		return fmt.Errorf("delete API token: %w", err)
	}

	return checkAffected(res)
}

// TouchAPIToken records that a token was used at a time.
func (d *DB) TouchAPIToken(ctx context.Context, id int64, at time.Time) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, at.UnixNano(),
		id); err != nil {
		return fmt.Errorf("touch API token: %w", err)
	}

	return nil
}

const apiTokenColumns = `id, token_id, name, token_hash, user_id, created_at, expires_at, last_used_at`

func scanAPIToken(row scanner) (APIToken, error) {
	var (
		t                     APIToken
		createdAt             int64
		expiresAt, lastUsedAt sql.Null[int64]
	)

	if err := row.Scan(&t.ID, &t.TokenID, &t.Name, &t.TokenHash, &t.UserID, &createdAt, &expiresAt,
		&lastUsedAt); err != nil {
		return APIToken{}, err
	}

	t.CreatedAt = time.Unix(0, createdAt).UTC()
	t.ExpiresAt = fromNullTime(expiresAt)
	t.LastUsedAt = fromNullTime(lastUsedAt)

	return t, nil
}

// nullTime stores the zero time as NULL.
func nullTime(t time.Time) sql.Null[int64] {
	if t.IsZero() {
		return sql.Null[int64]{}
	}

	return sql.Null[int64]{V: t.UnixNano(), Valid: true}
}

func fromNullTime(t sql.Null[int64]) time.Time {
	if !t.Valid {
		return time.Time{}
	}

	return time.Unix(0, t.V).UTC()
}
