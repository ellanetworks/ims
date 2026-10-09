package db

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mattn/go-sqlite3"
)

var ErrIdentityConflict = errors.New("public identity in another registration set of the private identity")

type PublicIdentity struct {
	URI         string
	Key         string
	DisplayName string
	Barred      bool
}

type Registration struct {
	ID         int64
	IMPI       string
	IMPU       string
	Identities []PublicIdentity
	UserData   []byte
	Bindings   []Binding
}

// Contact is what a binding binds a registration set to (RFC 5626 §6, TS 24.229 §5.4.1.2.2 step 6): a
// registration flow, identified by its Instance and RegID, when the multiple registration mechanism
// applies, or else a contact address, identified by its URI; with its parameters and the Path to it.
type Contact struct {
	URI      string
	Instance string
	RegID    int64
	Params   string
	Path     string
}

// Flow reports whether the contact is a registration flow (RFC 5626).
func (c Contact) Flow() bool {
	return c.RegID != 0
}

type BindingEvent string

const (
	BindingRegistered BindingEvent = "registered"
	BindingRefreshed  BindingEvent = "refreshed"
)

// Binding is the binding of a registration set to a contact (TS 24.229 §5.4.1.2.2 steps 6 to 8): its ID
// is stable over refreshes, and over a flow replaced in place.
type Binding struct {
	ID           int64
	Contact      Contact
	CallID       string
	CSeq         int64
	ExpiresAt    time.Time
	Event        BindingEvent
	IMPU         string
	RegisteredAt time.Time
}

const (
	registrationColumns = `id, impi, impu, user_data`

	bindingColumns = `id, registration_id, uri, instance_id, reg_id, params, path, call_id, cseq, expires_at, event,
		impu, registered_at`
)

func (d *DB) SaveRegistration(ctx context.Context, r Registration) (_ Registration, err error) {
	defer d.observe(poolWrite, &err)()

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return Registration{}, fmt.Errorf("save registration: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if r.ID == 0 {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO registrations (impi, impu, user_data) VALUES (?, ?, ?)`, r.IMPI, r.IMPU, r.UserData)
		if err != nil {
			return Registration{}, fmt.Errorf("save registration: %w", err)
		}

		if r.ID, err = res.LastInsertId(); err != nil {
			return Registration{}, fmt.Errorf("save registration: %w", err)
		}
	} else {
		res, err := tx.ExecContext(ctx,
			`UPDATE registrations SET impu = ?, user_data = ? WHERE id = ? AND impi = ?`, r.IMPU, r.UserData, r.ID, r.IMPI)
		if err != nil {
			return Registration{}, fmt.Errorf("save registration: %w", err)
		}

		if err := checkAffected(res); err != nil {
			return Registration{}, fmt.Errorf("save registration %d: %w", r.ID, err)
		}
	}

	if err := saveIdentities(ctx, tx, r); err != nil {
		return Registration{}, fmt.Errorf("save registration: %w", err)
	}

	r.Bindings = append([]Binding(nil), r.Bindings...)
	kept := make([]any, 0, len(r.Bindings)+1)
	kept = append(kept, r.ID)

	for i := range r.Bindings {
		b := &r.Bindings[i]
		b.Event = cmp.Or(b.Event, BindingRegistered)

		if b.ID, err = saveBinding(ctx, tx, r.ID, *b); err != nil {
			return Registration{}, fmt.Errorf("save registration: %w", err)
		}

		kept = append(kept, b.ID)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM bindings WHERE registration_id = ? AND id NOT IN (`+placeholders(len(kept)-1)+`)`, kept...); err != nil {
		return Registration{}, fmt.Errorf("save registration: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Registration{}, fmt.Errorf("save registration: %w", err)
	}

	return r, nil
}

func saveIdentities(ctx context.Context, tx *sql.Tx, r Registration) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM registration_identities WHERE registration_id = ?`, r.ID); err != nil {
		return err
	}

	for i, id := range r.Identities {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO registration_identities (registration_id, impi, position, uri, key, display_name, barred)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.IMPI, i, id.URI, id.Key, nullableString(id.DisplayName), id.Barred)
		if isConstraint(err, sqlite3.ErrConstraintUnique) {
			return fmt.Errorf("%s: %w", id.URI, ErrIdentityConflict)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// saveBinding stores b over the registration's binding to the same contact: the same flow, or the same
// URI, and returns its ID.
func saveBinding(ctx context.Context, tx *sql.Tx, registrationID int64, b Binding) (int64, error) {
	conflict := `ON CONFLICT (registration_id, uri) WHERE reg_id IS NULL DO UPDATE SET instance_id = excluded.instance_id`
	if b.Contact.Flow() {
		conflict = `ON CONFLICT (registration_id, instance_id, reg_id) WHERE reg_id IS NOT NULL DO UPDATE SET uri = excluded.uri`
	}

	var id int64

	err := tx.QueryRowContext(ctx,
		`INSERT INTO bindings (registration_id, uri, instance_id, reg_id, params, path, call_id, cseq, expires_at, event, impu,
			registered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`+conflict+`, params = excluded.params, path = excluded.path, call_id = excluded.call_id, cseq = excluded.cseq,
			expires_at = excluded.expires_at, event = excluded.event, impu = excluded.impu, registered_at = excluded.registered_at
		RETURNING id`,
		registrationID, b.Contact.URI, nullableString(b.Contact.Instance), nullableInt(b.Contact.RegID), b.Contact.Params,
		nullableString(b.Contact.Path), b.CallID, b.CSeq, b.ExpiresAt.UTC().UnixNano(), b.Event, b.IMPU,
		b.RegisteredAt.UTC().UnixNano()).Scan(&id)

	return id, err
}

func (d *DB) DeleteRegistration(ctx context.Context, id int64) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM registrations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete registration %d: %w", id, err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete registration %d: %w", id, err)
	}

	return nil
}

func (d *DB) ListRegistrationsByIMPI(ctx context.Context, impi string) (_ []Registration, err error) {
	defer d.observe(poolWrite, &err)()

	regs, err := queryRegistrations(ctx, d.conn,
		`SELECT `+registrationColumns+` FROM registrations WHERE impi = ? ORDER BY id`, impi)
	if err != nil {
		return nil, fmt.Errorf("list registrations of %s: %w", impi, err)
	}

	return regs, nil
}

func (d *DB) ListRegistrationsByIdentity(ctx context.Context, key string) (_ []Registration, err error) {
	defer d.observe(poolWrite, &err)()

	regs, err := queryRegistrations(ctx, d.conn,
		`SELECT `+registrationColumns+` FROM registrations
		WHERE id IN (SELECT registration_id FROM registration_identities WHERE key = ?) ORDER BY id`, key)
	if err != nil {
		return nil, fmt.Errorf("list registrations of %s: %w", key, err)
	}

	return regs, nil
}

// ListRegistrationsByIdentities returns the registrations holding any of the identity keys, each once.
func (d *DB) ListRegistrationsByIdentities(ctx context.Context, keys []string) (_ []Registration, err error) {
	defer d.observe(poolWrite, &err)()

	if len(keys) == 0 {
		return []Registration{}, nil
	}

	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}

	regs, err := queryRegistrations(ctx, d.conn,
		`SELECT `+registrationColumns+` FROM registrations
		WHERE id IN (SELECT registration_id FROM registration_identities WHERE key IN (`+placeholders(len(keys))+`)) ORDER BY id`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("list registrations of %d identities: %w", len(keys), err)
	}

	return regs, nil
}

func (d *DB) ListRegistrations(ctx context.Context, page, perPage int) (_ []Registration, _ int, err error) {
	defer d.observe(poolWrite, &err)()

	var total int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM registrations`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list registrations: %w", err)
	}

	regs, err := queryRegistrations(ctx, d.conn,
		`SELECT `+registrationColumns+` FROM registrations ORDER BY id LIMIT ? OFFSET ?`, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("list registrations: %w", err)
	}

	return regs, total, nil
}

func (d *DB) ListExpiredIMPIs(ctx context.Context, now time.Time) (_ []string, err error) {
	defer d.observe(poolWrite, &err)()

	rows, err := d.conn.QueryContext(ctx,
		`SELECT r.impi FROM bindings b JOIN registrations r ON r.id = b.registration_id WHERE b.expires_at <= ?
		UNION SELECT impi FROM reg_subscriptions WHERE expires_at <= ?
		ORDER BY 1`, now.UTC().UnixNano(), now.UTC().UnixNano())
	if err != nil {
		return nil, fmt.Errorf("list expired private identities: %w", err)
	}

	defer func() { _ = rows.Close() }()

	impis := []string{}

	for rows.Next() {
		var impi string
		if err := rows.Scan(&impi); err != nil {
			return nil, fmt.Errorf("list expired private identities: %w", err)
		}

		impis = append(impis, impi)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list expired private identities: %w", err)
	}

	return impis, nil
}

func queryRegistrations(ctx context.Context, q *sql.DB, query string, args ...any) ([]Registration, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	regs := []Registration{}

	for rows.Next() {
		r, err := scanRegistration(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}

		regs = append(regs, r)
	}

	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	if len(regs) == 0 {
		return regs, nil
	}

	index := make(map[int64]int, len(regs))
	ids := make([]any, 0, len(regs))

	for i, r := range regs {
		index[r.ID] = i
		ids = append(ids, r.ID)
	}

	if err := loadIdentities(ctx, q, regs, index, ids); err != nil {
		return nil, err
	}

	if err := loadBindings(ctx, q, regs, index, ids); err != nil {
		return nil, err
	}

	return regs, nil
}

func loadIdentities(ctx context.Context, q *sql.DB, regs []Registration, index map[int64]int, ids []any) error {
	rows, err := q.QueryContext(ctx,
		`SELECT registration_id, uri, key, display_name, barred FROM registration_identities
		WHERE registration_id IN (`+placeholders(len(ids))+`) ORDER BY registration_id, position`, ids...)
	if err != nil {
		return err
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			registrationID int64
			identity       PublicIdentity
			displayName    sql.NullString
		)

		if err := rows.Scan(&registrationID, &identity.URI, &identity.Key, &displayName, &identity.Barred); err != nil {
			return err
		}

		identity.DisplayName = displayName.String

		r := &regs[index[registrationID]]
		r.Identities = append(r.Identities, identity)
	}

	return rows.Err()
}

func loadBindings(ctx context.Context, q *sql.DB, regs []Registration, index map[int64]int, ids []any) error {
	rows, err := q.QueryContext(ctx,
		`SELECT `+bindingColumns+` FROM bindings
		WHERE registration_id IN (`+placeholders(len(ids))+`) ORDER BY registration_id, id`, ids...)
	if err != nil {
		return err
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			registrationID, expiresAt, registeredAt int64
			b                                       Binding
			instance, path                          sql.NullString
			regID                                   sql.NullInt64
		)

		if err := rows.Scan(&b.ID, &registrationID, &b.Contact.URI, &instance, &regID, &b.Contact.Params, &path, &b.CallID,
			&b.CSeq, &expiresAt, &b.Event, &b.IMPU, &registeredAt); err != nil {
			return err
		}

		b.Contact.Instance, b.Contact.RegID, b.Contact.Path = instance.String, regID.Int64, path.String
		b.ExpiresAt = time.Unix(0, expiresAt).UTC()
		b.RegisteredAt = time.Unix(0, registeredAt).UTC()

		r := &regs[index[registrationID]]
		r.Bindings = append(r.Bindings, b)
	}

	return rows.Err()
}

func scanRegistration(row scanner) (Registration, error) {
	var r Registration

	if err := row.Scan(&r.ID, &r.IMPI, &r.IMPU, &r.UserData); err != nil {
		return Registration{}, err
	}

	return r, nil
}

// GetBinding returns the binding with the ID, and the private identity of its registration set.
func (d *DB) GetBinding(ctx context.Context, id int64) (_ Binding, _ string, err error) {
	defer d.observe(poolWrite, &err)()

	var (
		b                       Binding
		impi                    string
		registrationID          int64
		expiresAt, registeredAt int64
		instance, path          sql.NullString
		regID                   sql.NullInt64
	)

	err = d.conn.QueryRowContext(ctx,
		`SELECT b.id, b.registration_id, b.uri, b.instance_id, b.reg_id, b.params, b.path, b.call_id, b.cseq, b.expires_at,
			b.event, b.impu, b.registered_at, r.impi
		FROM bindings b JOIN registrations r ON r.id = b.registration_id WHERE b.id = ?`, id).Scan(
		&b.ID, &registrationID, &b.Contact.URI, &instance, &regID, &b.Contact.Params, &path, &b.CallID, &b.CSeq, &expiresAt,
		&b.Event, &b.IMPU, &registeredAt, &impi)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}

	if err != nil {
		return Binding{}, "", fmt.Errorf("get binding %d: %w", id, err)
	}

	b.Contact.Instance, b.Contact.RegID, b.Contact.Path = instance.String, regID.Int64, path.String
	b.ExpiresAt = time.Unix(0, expiresAt).UTC()
	b.RegisteredAt = time.Unix(0, registeredAt).UTC()

	return b, impi, nil
}
