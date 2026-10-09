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

type Subscriber string

const (
	SubscriberUE    Subscriber = "ue"
	SubscriberPCSCF Subscriber = "pcscf"
)

type RegSubscription struct {
	ID           int64
	IMPI         string
	IMPU         string
	Subscriber   Subscriber
	CallID       string
	LocalTag     string
	RemoteTag    string
	RemoteTarget string
	Dialog       []byte
	Version      int64
	ExpiresAt    time.Time
}

const regSubscriptionColumns = `id, impi, impu, subscriber, call_id, local_tag, remote_tag, remote_target,
	dialog, version, expires_at`

func (d *DB) PutRegSubscription(ctx context.Context, s RegSubscription) (_ int64, err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO reg_subscriptions (impi, impu, subscriber, call_id, local_tag, remote_tag, remote_target,
			dialog, version, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.IMPI, s.IMPU, s.Subscriber, s.CallID, s.LocalTag, s.RemoteTag, s.RemoteTarget,
		s.Dialog, s.Version, s.ExpiresAt.UTC().UnixNano())

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

func (d *DB) UpdateRegSubscription(ctx context.Context, s RegSubscription) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx,
		`UPDATE reg_subscriptions SET remote_target = ?, dialog = ?, version = ?, expires_at = ? WHERE id = ?`,
		s.RemoteTarget, s.Dialog, s.Version, s.ExpiresAt.UTC().UnixNano(), s.ID)
	if err != nil {
		return fmt.Errorf("update reg subscription: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("update reg subscription: %w", err)
	}

	return nil
}

func (d *DB) GetRegSubscription(ctx context.Context, callID, localTag, remoteTag string) (_ RegSubscription, err error) {
	defer d.observe(poolWrite, &err)()

	s, err := scanRegSubscription(d.conn.QueryRowContext(ctx,
		`SELECT `+regSubscriptionColumns+` FROM reg_subscriptions WHERE call_id = ? AND local_tag = ? AND remote_tag = ?`,
		callID, localTag, remoteTag))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}

	if err != nil {
		return RegSubscription{}, fmt.Errorf("get reg subscription: %w", err)
	}

	return s, nil
}

func (d *DB) GetRegSubscriptionByID(ctx context.Context, id int64) (_ RegSubscription, err error) {
	defer d.observe(poolWrite, &err)()

	s, err := scanRegSubscription(d.conn.QueryRowContext(ctx,
		`SELECT `+regSubscriptionColumns+` FROM reg_subscriptions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}

	if err != nil {
		return RegSubscription{}, fmt.Errorf("get reg subscription %d: %w", id, err)
	}

	return s, nil
}

func (d *DB) ListRegSubscriptions(ctx context.Context, impi string) (_ []RegSubscription, err error) {
	defer d.observe(poolWrite, &err)()

	rows, err := d.conn.QueryContext(ctx,
		`SELECT `+regSubscriptionColumns+` FROM reg_subscriptions WHERE impi = ? ORDER BY id`, impi)
	if err != nil {
		return nil, fmt.Errorf("list reg subscriptions: %w", err)
	}

	defer func() { _ = rows.Close() }()

	subs := []RegSubscription{}

	for rows.Next() {
		s, err := scanRegSubscription(rows)
		if err != nil {
			return nil, fmt.Errorf("list reg subscriptions: %w", err)
		}

		subs = append(subs, s)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reg subscriptions: %w", err)
	}

	return subs, nil
}

func (d *DB) DeleteRegSubscription(ctx context.Context, id int64) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM reg_subscriptions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete reg subscription: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete reg subscription: %w", err)
	}

	return nil
}

func scanRegSubscription(row scanner) (RegSubscription, error) {
	var (
		s         RegSubscription
		expiresAt int64
	)

	if err := row.Scan(&s.ID, &s.IMPI, &s.IMPU, &s.Subscriber, &s.CallID, &s.LocalTag, &s.RemoteTag, &s.RemoteTarget,
		&s.Dialog, &s.Version, &expiresAt); err != nil {
		return RegSubscription{}, err
	}

	s.ExpiresAt = time.Unix(0, expiresAt).UTC()

	return s, nil
}
