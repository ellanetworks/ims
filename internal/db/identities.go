package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mattn/go-sqlite3"
)

type Identity struct {
	ID          int64
	UserID      int64
	Issuer      string
	Subject     string
	Email       string
	CreatedAt   time.Time
	LastLoginAt time.Time
}

type NewIdentity struct {
	Issuer  string
	Subject string
	Email   string
	At      time.Time
}

func (d *DB) GetIdentity(ctx context.Context, issuer, subject string) (_ Identity, err error) {
	ctx, done := d.observe(ctx, "GetIdentity", poolRead)
	defer done(&err)

	var (
		i                    Identity
		createdAt, lastLogin int64
	)

	err = d.reader().QueryRowContext(ctx, `SELECT id, user_id, issuer, subject, email, created_at, last_login_at
		FROM user_identities WHERE issuer = ? AND subject = ?`, issuer, subject).Scan(&i.ID, &i.UserID, &i.Issuer,
		&i.Subject, &i.Email, &createdAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrNotFound
	}

	if err != nil {
		return Identity{}, fmt.Errorf("get identity: %w", err)
	}

	i.CreatedAt = time.Unix(0, createdAt).UTC()
	i.LastLoginAt = time.Unix(0, lastLogin).UTC()

	return i, nil
}

func (d *DB) CreateIdentity(ctx context.Context, userID int64, i NewIdentity) (_ int64, err error) {
	ctx, done := d.observe(ctx, "CreateIdentity", poolWrite)
	defer done(&err)

	return run[int64](ctx, d, cmdCreateIdentity, "create identity", createIdentityCmd{UserID: userID, Identity: i})
}

type createIdentityCmd struct {
	UserID   int64
	Identity NewIdentity
}

func applyCreateIdentity(a *applyCtx, p createIdentityCmd) (int64, error) {
	return createIdentity(a.ctx, a.tx, p.UserID, p.Identity)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func createIdentity(ctx context.Context, conn execer, userID int64, i NewIdentity) (int64, error) {
	res, err := conn.ExecContext(ctx, `INSERT INTO user_identities
		(user_id, issuer, subject, email, created_at, last_login_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, i.Issuer, i.Subject, i.Email, i.At.UnixNano(), i.At.UnixNano())
	if isConstraint(err, sqlite3.ErrConstraintUnique) {
		return 0, ErrDuplicate
	}

	if err != nil {
		return 0, err
	}

	return res.LastInsertId()
}

func (d *DB) DeleteIdentities(ctx context.Context, userID int64) (err error) {
	ctx, done := d.observe(ctx, "DeleteIdentities", poolWrite)
	defer done(&err)

	_, err = run[struct{}](ctx, d, cmdDeleteIdentities, "delete identities", userID)

	return err
}

func applyDeleteIdentities(a *applyCtx, userID int64) (struct{}, error) {
	var linked bool
	if err := a.tx.QueryRowContext(a.ctx, `SELECT EXISTS (SELECT 1 FROM user_identities JOIN oidc
		ON oidc.issuer = user_identities.issuer WHERE user_identities.user_id = ?)`, userID).Scan(&linked); err != nil {
		return struct{}{}, err
	}

	if !linked {
		return struct{}{}, ErrNotFound
	}

	_, err := a.tx.ExecContext(a.ctx, `DELETE FROM user_identities WHERE user_id = ?`, userID)

	return struct{}{}, err
}

func init() {
	register(cmdCreateIdentity, handle(applyCreateIdentity))
	register(cmdDeleteIdentities, handle(applyDeleteIdentities))
}
