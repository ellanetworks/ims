package db

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"
)

// TS 29.228 clause 6.5.2.1
var ErrIdentityConflict = errors.New("public identity in another implicit registration set")

type PublicIdentity struct {
	URI         string
	Key         string
	DisplayName string
	Barred      bool
}

// TS 29.228 clause 6.5.1.1
// TS 29.228 clause 6.5.2.1
// TS 23.228 clause 4.3.3.4
type ImplicitSet struct {
	ID           int64
	Identities   []PublicIdentity
	UserData     []byte
	HSS          HSS
	Unregistered *Unregistered
}

type Unregistered struct {
	Identity  string
	Wildcard  string
	ExpiresAt time.Time
}

type Registration struct {
	ID         int64
	IMPI       string
	IMPU       string
	SetID      int64
	Identities []PublicIdentity
	UserData   []byte
	HSS        HSS
	Bindings   []Binding
}

// TS 29.229 clause 7.2
type HSS struct {
	Host     string
	Realm    string
	Features uint32
}

// RFC 5626 section 6
// TS 24.229 clause 5.4.1.2.2
type Contact struct {
	URI      string
	Instance string
	RegID    int64
	Params   string
	Path     string
}

func (c Contact) Flow() bool {
	return c.RegID != 0
}

type BindingEvent string

const (
	BindingRegistered BindingEvent = "registered"
	BindingRefreshed  BindingEvent = "refreshed"
)

// TS 24.229 clause 5.4.1.2.2
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
	registrationColumns = `r.id, r.impi, r.impu, r.set_id, s.user_data, s.hss_host, s.hss_realm, s.hss_features`
	registrationFrom    = ` FROM registrations r JOIN implicit_sets s ON s.id = r.set_id`

	setColumns = `id, user_data, hss_host, hss_realm, hss_features, unregistered_identity, wildcard, expires_at`

	bindingColumns = `id, registration_id, uri, instance_id, reg_id, params, path, call_id, cseq, expires_at, event,
		impu, registered_at`
)

func (d *DB) SaveSet(ctx context.Context, s ImplicitSet) (_ ImplicitSet, err error) {
	ctx, done := d.observe(ctx, "SaveSet", poolLocal)
	defer done(&err)

	tx, err := d.local.BeginTx(ctx, nil)
	if err != nil {
		return ImplicitSet{}, fmt.Errorf("save implicit set: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if s, err = saveSet(ctx, tx, s); err != nil {
		return ImplicitSet{}, fmt.Errorf("save implicit set: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return ImplicitSet{}, fmt.Errorf("save implicit set: %w", err)
	}

	return s, nil
}

func saveSet(ctx context.Context, tx *sql.Tx, s ImplicitSet) (ImplicitSet, error) {
	var (
		identity, wildcard sql.NullString
		expires            sql.NullInt64
	)

	if u := s.Unregistered; u != nil {
		identity = sql.NullString{String: u.Identity, Valid: true}
		wildcard = nullableString(u.Wildcard)
		expires = sql.NullInt64{Int64: u.ExpiresAt.UTC().UnixNano(), Valid: true}
	}

	data := s.UserData
	if data == nil {
		data = []byte{}
	}

	if s.ID == 0 {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO implicit_sets (user_data, hss_host, hss_realm, hss_features, unregistered_identity, wildcard, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			data, s.HSS.Host, s.HSS.Realm, s.HSS.Features, identity, wildcard, expires)
		if err != nil {
			return ImplicitSet{}, err
		}

		if s.ID, err = res.LastInsertId(); err != nil {
			return ImplicitSet{}, err
		}
	} else {
		res, err := tx.ExecContext(ctx,
			`UPDATE implicit_sets SET user_data = ?, hss_host = ?, hss_realm = ?, hss_features = ?, unregistered_identity = ?,
				wildcard = ?, expires_at = ? WHERE id = ?`,
			data, s.HSS.Host, s.HSS.Realm, s.HSS.Features, identity, wildcard, expires, s.ID)
		if err != nil {
			return ImplicitSet{}, err
		}

		if err := checkAffected(res); err != nil {
			return ImplicitSet{}, fmt.Errorf("set %d: %w", s.ID, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM set_identities WHERE set_id = ?`, s.ID); err != nil {
		return ImplicitSet{}, err
	}

	for i, id := range s.Identities {
		var registered bool

		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM registrations WHERE set_id = i.set_id)
			FROM set_identities i WHERE i.key = ?`, id.Key).Scan(&registered)

		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return ImplicitSet{}, err
		case registered:
			return ImplicitSet{}, fmt.Errorf("%s: %w", id.URI, ErrIdentityConflict)
		default:
			if _, err := tx.ExecContext(ctx, `DELETE FROM set_identities WHERE key = ?`, id.Key); err != nil {
				return ImplicitSet{}, err
			}
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO set_identities (set_id, position, uri, key, display_name, barred) VALUES (?, ?, ?, ?, ?, ?)`,
			s.ID, i, id.URI, id.Key, nullableString(id.DisplayName), id.Barred); err != nil {
			return ImplicitSet{}, err
		}
	}

	return s, nil
}

func (d *DB) GetSet(ctx context.Context, id int64) (_ ImplicitSet, err error) {
	ctx, done := d.observe(ctx, "GetSet", poolLocal)
	defer done(&err)

	sets, err := querySets(ctx, d.localRead, `SELECT `+setColumns+` FROM implicit_sets WHERE id = ?`, id)
	if err != nil {
		return ImplicitSet{}, fmt.Errorf("get implicit set %d: %w", id, err)
	}

	if len(sets) == 0 {
		return ImplicitSet{}, fmt.Errorf("get implicit set %d: %w", id, ErrNotFound)
	}

	return sets[0], nil
}

func (d *DB) FindSets(ctx context.Context, keys []string) (_ []ImplicitSet, err error) {
	ctx, done := d.observe(ctx, "FindSets", poolLocal)
	defer done(&err)

	if len(keys) == 0 {
		return []ImplicitSet{}, nil
	}

	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}

	sets, err := querySets(ctx, d.localRead, `SELECT `+setColumns+` FROM implicit_sets
		WHERE id IN (SELECT set_id FROM set_identities WHERE key IN (`+placeholders(len(keys))+`)) ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("find implicit sets of %d identities: %w", len(keys), err)
	}

	return sets, nil
}

func (d *DB) ListUnregisteredSets(ctx context.Context) (_ []ImplicitSet, err error) {
	ctx, done := d.observe(ctx, "ListUnregisteredSets", poolLocal)
	defer done(&err)

	sets, err := querySets(ctx, d.localRead,
		`SELECT `+setColumns+` FROM implicit_sets WHERE expires_at IS NOT NULL ORDER BY expires_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list unregistered implicit sets: %w", err)
	}

	return sets, nil
}

func (d *DB) FindUnregisteredSets(ctx context.Context, keys []string) (_ []ImplicitSet, err error) {
	ctx, done := d.observe(ctx, "FindUnregisteredSets", poolLocal)
	defer done(&err)

	if len(keys) == 0 {
		return []ImplicitSet{}, nil
	}

	args := make([]any, 0, 2*len(keys))
	for _, k := range keys {
		args = append(args, k)
	}

	args = append(args, args...)

	sets, err := querySets(ctx, d.localRead, `SELECT `+setColumns+` FROM implicit_sets WHERE expires_at IS NOT NULL AND (
		id IN (SELECT set_id FROM set_identities WHERE key IN (`+placeholders(len(keys))+`))
		OR unregistered_identity IN (`+placeholders(len(keys))+`) OR wildcard IS NOT NULL) ORDER BY expires_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("find unregistered implicit sets of %d identities: %w", len(keys), err)
	}

	return sets, nil
}

func (d *DB) DeleteSet(ctx context.Context, id int64) (err error) {
	ctx, done := d.observe(ctx, "DeleteSet", poolLocal)
	defer done(&err)

	res, err := d.local.ExecContext(ctx,
		`DELETE FROM implicit_sets WHERE id = ? AND NOT EXISTS (SELECT 1 FROM registrations WHERE set_id = ?)`, id, id)
	if err != nil {
		return fmt.Errorf("delete implicit set %d: %w", id, err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete implicit set %d: %w", id, err)
	}

	return nil
}

func querySets(ctx context.Context, q *sql.DB, query string, args ...any) ([]ImplicitSet, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	sets := []ImplicitSet{}

	for rows.Next() {
		var (
			s                  ImplicitSet
			identity, wildcard sql.NullString
			expires            sql.NullInt64
		)

		if err := rows.Scan(&s.ID, &s.UserData, &s.HSS.Host, &s.HSS.Realm, &s.HSS.Features, &identity, &wildcard,
			&expires); err != nil {
			_ = rows.Close()
			return nil, err
		}

		if identity.Valid {
			s.Unregistered = &Unregistered{
				Identity: identity.String, Wildcard: wildcard.String, ExpiresAt: time.Unix(0, expires.Int64).UTC(),
			}
		}

		sets = append(sets, s)
	}

	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	if len(sets) == 0 {
		return sets, nil
	}

	index := make(map[int64]int, len(sets))
	ids := make([]any, 0, len(sets))

	for i, s := range sets {
		index[s.ID] = i
		ids = append(ids, s.ID)
	}

	identities, err := loadIdentities(ctx, q, ids)
	if err != nil {
		return nil, err
	}

	for id, l := range identities {
		sets[index[id]].Identities = l
	}

	return sets, nil
}

func (d *DB) SaveRegistration(ctx context.Context, r Registration) (_ Registration, err error) {
	ctx, done := d.observe(ctx, "SaveRegistration", poolLocal)
	defer done(&err)

	tx, err := d.local.BeginTx(ctx, nil)
	if err != nil {
		return Registration{}, fmt.Errorf("save registration: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if r.SetID == 0 {
		if r.SetID, err = setOf(ctx, tx, r); err != nil {
			return Registration{}, fmt.Errorf("save registration: %w", err)
		}
	}

	if r, err = saveRegistration(ctx, tx, r); err != nil {
		return Registration{}, fmt.Errorf("save registration: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Registration{}, fmt.Errorf("save registration: %w", err)
	}

	return r, nil
}

func (d *DB) SaveAssignment(ctx context.Context, s ImplicitSet, r Registration, replaced []int64, losing []ImplicitSet,
) (_ ImplicitSet, _ Registration, err error) {
	ctx, done := d.observe(ctx, "SaveAssignment", poolLocal)
	defer done(&err)

	tx, err := d.local.BeginTx(ctx, nil)
	if err != nil {
		return ImplicitSet{}, Registration{}, fmt.Errorf("save assignment: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var left []int64

	for _, id := range replaced {
		var setID int64

		err := tx.QueryRowContext(ctx, `DELETE FROM registrations WHERE id = ? AND impi = ? RETURNING set_id`, id, r.IMPI).
			Scan(&setID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}

		if err != nil {
			return ImplicitSet{}, Registration{}, fmt.Errorf("save assignment: %w", err)
		}

		left = append(left, setID)
	}

	for _, l := range losing {
		if _, err := saveSet(ctx, tx, l); err != nil {
			return ImplicitSet{}, Registration{}, fmt.Errorf("save assignment: set %d: %w", l.ID, err)
		}
	}

	if s, err = saveSet(ctx, tx, s); err != nil {
		return ImplicitSet{}, Registration{}, fmt.Errorf("save assignment: %w", err)
	}

	r.SetID = s.ID

	if r, err = saveRegistration(ctx, tx, r); err != nil {
		return ImplicitSet{}, Registration{}, fmt.Errorf("save assignment: %w", err)
	}

	for _, id := range left {
		if _, err := tx.ExecContext(ctx, `DELETE FROM implicit_sets WHERE id = ? AND expires_at IS NULL
			AND NOT EXISTS (SELECT 1 FROM registrations WHERE set_id = ?)`, id, id); err != nil {
			return ImplicitSet{}, Registration{}, fmt.Errorf("save assignment: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return ImplicitSet{}, Registration{}, fmt.Errorf("save assignment: %w", err)
	}

	return s, r, nil
}

func saveRegistration(ctx context.Context, tx *sql.Tx, r Registration) (Registration, error) {
	if r.ID == 0 {
		res, err := tx.ExecContext(ctx, `INSERT INTO registrations (impi, impu, set_id) VALUES (?, ?, ?)`, r.IMPI, r.IMPU, r.SetID)
		if err != nil {
			return Registration{}, err
		}

		if r.ID, err = res.LastInsertId(); err != nil {
			return Registration{}, err
		}
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE registrations SET impu = ? WHERE id = ? AND impi = ? AND set_id = ?`,
			r.IMPU, r.ID, r.IMPI, r.SetID)
		if err != nil {
			return Registration{}, err
		}

		if err := checkAffected(res); err != nil {
			return Registration{}, fmt.Errorf("registration %d: %w", r.ID, err)
		}
	}

	r.Bindings = append([]Binding(nil), r.Bindings...)
	kept := make([]any, 0, len(r.Bindings)+1)
	kept = append(kept, r.ID)

	for i := range r.Bindings {
		b := &r.Bindings[i]
		b.Event = cmp.Or(b.Event, BindingRegistered)

		var err error
		if b.ID, err = saveBinding(ctx, tx, r.ID, *b); err != nil {
			return Registration{}, err
		}

		kept = append(kept, b.ID)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM bindings WHERE registration_id = ? AND id NOT IN (`+placeholders(len(kept)-1)+`)`, kept...); err != nil {
		return Registration{}, err
	}

	return r, nil
}

func setOf(ctx context.Context, tx *sql.Tx, r Registration) (int64, error) {
	var found int64

	for _, id := range r.Identities {
		var setID int64

		err := tx.QueryRowContext(ctx, `SELECT set_id FROM set_identities WHERE key = ?`, id.Key).Scan(&setID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}

		if err != nil {
			return 0, err
		}

		if found != 0 && found != setID {
			return 0, ErrIdentityConflict
		}

		found = setID
	}

	if found != 0 {
		return found, nil
	}

	saved, err := saveSet(ctx, tx, ImplicitSet{Identities: r.Identities, UserData: r.UserData, HSS: r.HSS})
	if err != nil {
		return 0, err
	}

	return saved.ID, nil
}

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
	ctx, done := d.observe(ctx, "DeleteRegistration", poolLocal)
	defer done(&err)

	tx, err := d.local.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete registration %d: %w", id, err)
	}

	defer func() { _ = tx.Rollback() }()

	var setID int64

	err = tx.QueryRowContext(ctx, `DELETE FROM registrations WHERE id = ? RETURNING set_id`, id).Scan(&setID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}

	if err != nil {
		return fmt.Errorf("delete registration %d: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM implicit_sets WHERE id = ? AND expires_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM registrations WHERE set_id = ?)`, setID, setID); err != nil {
		return fmt.Errorf("delete registration %d: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete registration %d: %w", id, err)
	}

	return nil
}

func (d *DB) ListRegistrationsByIMPI(ctx context.Context, impi string) (_ []Registration, err error) {
	ctx, done := d.observe(ctx, "ListRegistrationsByIMPI", poolLocal)
	defer done(&err)

	regs, err := queryRegistrations(ctx, d.localRead,
		`SELECT `+registrationColumns+registrationFrom+` WHERE r.impi = ? ORDER BY r.id`, impi)
	if err != nil {
		return nil, fmt.Errorf("list registrations of %s: %w", impi, err)
	}

	return regs, nil
}

func (d *DB) ListRegistrationsBySet(ctx context.Context, setID int64) (_ []Registration, err error) {
	ctx, done := d.observe(ctx, "ListRegistrationsBySet", poolLocal)
	defer done(&err)

	regs, err := queryRegistrations(ctx, d.localRead,
		`SELECT `+registrationColumns+registrationFrom+` WHERE r.set_id = ? ORDER BY r.id`, setID)
	if err != nil {
		return nil, fmt.Errorf("list registrations of set %d: %w", setID, err)
	}

	return regs, nil
}

func (d *DB) ListRegistrationsByIdentity(ctx context.Context, key string) (_ []Registration, err error) {
	ctx, done := d.observe(ctx, "ListRegistrationsByIdentity", poolLocal)
	defer done(&err)

	regs, err := queryRegistrations(ctx, d.localRead,
		`SELECT `+registrationColumns+registrationFrom+`
		WHERE r.set_id IN (SELECT set_id FROM set_identities WHERE key = ?) ORDER BY r.id`, key)
	if err != nil {
		return nil, fmt.Errorf("list registrations of %s: %w", key, err)
	}

	return regs, nil
}

func (d *DB) ListRegistrationsByIdentities(ctx context.Context, keys []string) (_ []Registration, err error) {
	ctx, done := d.observe(ctx, "ListRegistrationsByIdentities", poolLocal)
	defer done(&err)

	if len(keys) == 0 {
		return []Registration{}, nil
	}

	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}

	regs, err := queryRegistrations(ctx, d.localRead,
		`SELECT `+registrationColumns+registrationFrom+`
		WHERE r.set_id IN (SELECT set_id FROM set_identities WHERE key IN (`+placeholders(len(keys))+`)) ORDER BY r.id`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("list registrations of %d identities: %w", len(keys), err)
	}

	return regs, nil
}

func (d *DB) ListRegistrations(ctx context.Context, page, perPage int) (_ []Registration, _ int, err error) {
	ctx, done := d.observe(ctx, "ListRegistrations", poolLocal)
	defer done(&err)

	var total int
	if err := d.localRead.QueryRowContext(ctx, `SELECT COUNT(*) FROM registrations`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list registrations: %w", err)
	}

	regs, err := queryRegistrations(ctx, d.localRead,
		`SELECT `+registrationColumns+registrationFrom+` ORDER BY r.id LIMIT ? OFFSET ?`, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("list registrations: %w", err)
	}

	return regs, total, nil
}

func (d *DB) ListExpiredIMPIs(ctx context.Context, now time.Time) (_ []string, err error) {
	ctx, done := d.observe(ctx, "ListExpiredIMPIs", poolLocal)
	defer done(&err)

	rows, err := d.localRead.QueryContext(ctx,
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

	var sets []any

	for i, r := range regs {
		index[r.ID] = i
		ids = append(ids, r.ID)

		if !slices.Contains(sets, any(r.SetID)) {
			sets = append(sets, r.SetID)
		}
	}

	identities, err := loadIdentities(ctx, q, sets)
	if err != nil {
		return nil, err
	}

	for i := range regs {
		regs[i].Identities = slices.Clone(identities[regs[i].SetID])
	}

	if err := loadBindings(ctx, q, regs, index, ids); err != nil {
		return nil, err
	}

	return regs, nil
}

func loadIdentities(ctx context.Context, q *sql.DB, sets []any) (map[int64][]PublicIdentity, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT set_id, uri, key, display_name, barred FROM set_identities
		WHERE set_id IN (`+placeholders(len(sets))+`) ORDER BY set_id, position`, sets...)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	out := make(map[int64][]PublicIdentity, len(sets))

	for rows.Next() {
		var (
			setID       int64
			identity    PublicIdentity
			displayName sql.NullString
		)

		if err := rows.Scan(&setID, &identity.URI, &identity.Key, &displayName, &identity.Barred); err != nil {
			return nil, err
		}

		identity.DisplayName = displayName.String
		out[setID] = append(out[setID], identity)
	}

	return out, rows.Err()
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

	if err := row.Scan(&r.ID, &r.IMPI, &r.IMPU, &r.SetID, &r.UserData, &r.HSS.Host, &r.HSS.Realm,
		&r.HSS.Features); err != nil {
		return Registration{}, err
	}

	return r, nil
}

func (d *DB) GetBinding(ctx context.Context, id int64) (_ Binding, _ string, err error) {
	ctx, done := d.observe(ctx, "GetBinding", poolLocal)
	defer done(&err)

	var (
		b                       Binding
		impi                    string
		registrationID          int64
		expiresAt, registeredAt int64
		instance, path          sql.NullString
		regID                   sql.NullInt64
	)

	err = d.localRead.QueryRowContext(ctx,
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
