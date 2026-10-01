package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mattn/go-sqlite3"
)

var ErrSubscriptionExists = errors.New("subscription dialog already exists")

// RegSubscription is a phone's subscription to its own registration state
// (RFC 3680). LocalCSeq and Version are those of the last NOTIFY built: a new
// subscription is stored with the values of its initial NOTIFY, and NextNotify
// returns the values for each later one.
type RegSubscription struct {
	ID           int64
	IMPI         string
	CallID       string
	RemoteTag    string
	LocalTag     string
	RemoteTarget string
	RemoteCSeq   int64
	LocalCSeq    int64
	Version      int64
	ExpiresAt    time.Time
}

const regSubscriptionColumns = `id, impi, call_id, remote_tag, local_tag, remote_target,
	remote_cseq, local_cseq, version, expires_at`

func (d *DB) PutRegSubscription(ctx context.Context, s RegSubscription) (int64, error) {
	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO reg_subscriptions (impi, call_id, remote_tag, local_tag, remote_target,
			remote_cseq, local_cseq, version, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.IMPI, s.CallID, s.RemoteTag, s.LocalTag, s.RemoteTarget,
		s.RemoteCSeq, s.LocalCSeq, s.Version, s.ExpiresAt.UTC().UnixNano())

	switch {
	case isConstraint(err, sqlite3.ErrConstraintUnique):
		return 0, fmt.Errorf("put reg subscription: %w", ErrSubscriptionExists)
	case err != nil:
		return 0, fmt.Errorf("put reg subscription: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("put reg subscription: %w", err)
	}

	return id, nil
}

// RefreshRegSubscription records an accepted in-dialog SUBSCRIBE.
func (d *DB) RefreshRegSubscription(ctx context.Context, id int64, remoteCSeq int64, expiresAt time.Time) error {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE reg_subscriptions SET remote_cseq = ?, expires_at = ? WHERE id = ?`,
		remoteCSeq, expiresAt.UTC().UnixNano(), id)
	if err != nil {
		return fmt.Errorf("refresh reg subscription: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("refresh reg subscription: %w", err)
	}

	return nil
}

// NextNotify increments the subscription's NOTIFY CSeq and reginfo version
// together, and returns the new values.
func (d *DB) NextNotify(ctx context.Context, id int64) (cseq, version int64, err error) {
	err = d.conn.QueryRowContext(ctx,
		`UPDATE reg_subscriptions SET local_cseq = local_cseq + 1, version = version + 1
		WHERE id = ? RETURNING local_cseq, version`, id).Scan(&cseq, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound
	}

	if err != nil {
		return 0, 0, fmt.Errorf("next notify: %w", err)
	}

	return cseq, version, nil
}

func (d *DB) ListRegSubscriptions(ctx context.Context, impi string) ([]RegSubscription, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT `+regSubscriptionColumns+` FROM reg_subscriptions WHERE impi = ? ORDER BY id`, impi)
	if err != nil {
		return nil, fmt.Errorf("list reg subscriptions: %w", err)
	}

	defer func() { _ = rows.Close() }()

	subs := []RegSubscription{}

	for rows.Next() {
		var (
			s         RegSubscription
			expiresAt int64
		)

		if err := rows.Scan(&s.ID, &s.IMPI, &s.CallID, &s.RemoteTag, &s.LocalTag, &s.RemoteTarget,
			&s.RemoteCSeq, &s.LocalCSeq, &s.Version, &expiresAt); err != nil {
			return nil, fmt.Errorf("list reg subscriptions: %w", err)
		}

		s.ExpiresAt = time.Unix(0, expiresAt).UTC()
		subs = append(subs, s)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reg subscriptions: %w", err)
	}

	return subs, nil
}

func (d *DB) DeleteRegSubscription(ctx context.Context, id int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM reg_subscriptions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete reg subscription: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete reg subscription: %w", err)
	}

	return nil
}
