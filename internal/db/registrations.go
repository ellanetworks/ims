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

// ErrIdentityConflict is a public identity that a set holds, of a registration, which another set would take, or a
// registration without a set whose identities are in several sets: the HSS removes an identity from its set before it
// adds it to another (TS 29.228 §6.5.2.1).
var ErrIdentityConflict = errors.New("public identity in another implicit registration set")

type PublicIdentity struct {
	URI         string
	Key         string
	DisplayName string
	Barred      bool
}

// ImplicitSet is an implicit registration set and its user profile, as the HSS downloaded or pushed it (TS 29.228
// §6.5.1.1, §6.5.2.1): one for all the private identities that register it (TS 23.228 §4.3.3.4). A public identity is
// in one set at most.
type ImplicitSet struct {
	ID int64
	// Identities are the public user identities of the set, in the order of the profile: the first not barred is the
	// default one.
	Identities []PublicIdentity
	UserData   []byte
	// HSS is the HSS that serves the set, where the S-CSCF sends its Cx requests (TS 29.229 §5.5).
	HSS HSS
	// Unregistered, when set, tells that the S-CSCF keeps the set for a public identity that is not registered, until
	// it clears its name at the HSS (TS 23.228 §5.12.1, TS 29.228 §6.1.2.1): no registration refers to it.
	Unregistered *Unregistered
}

// Unregistered is what the S-CSCF keeps of a set downloaded for a public identity, a user's or a PSI's, that is not
// registered (SAR UNREGISTERED_USER).
type Unregistered struct {
	// Identity is the canonical public identity of the SAR, and Wildcard the wildcarded identity the HSS said it falls
	// in, if any (TS 29.228 Table 6.1.2.2).
	Identity  string
	Wildcard  string
	ExpiresAt time.Time
}

// Registration is the registration of an implicit set by a private identity. Identities, UserData and HSS are those
// of the set as read: SaveRegistration stores them only for a registration without a set yet.
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

// HSS is the Diameter identity of an HSS, from the Origin-Host and Origin-Realm of its answers, and the Cx features it
// supports, from the Supported-Features of its SAA (TS 29.229 §7.2). Empty is unknown.
type HSS struct {
	Host     string
	Realm    string
	Features uint32
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
	registrationColumns = `r.id, r.impi, r.impu, r.set_id, s.user_data, s.hss_host, s.hss_realm, s.hss_features`
	registrationFrom    = ` FROM registrations r JOIN implicit_sets s ON s.id = r.set_id`

	setColumns = `id, user_data, hss_host, hss_realm, hss_features, unregistered_identity, wildcard, expires_at`

	bindingColumns = `id, registration_id, uri, instance_id, reg_id, params, path, call_id, cseq, expires_at, event,
		impu, registered_at`
)

// SaveSet stores the set, and its identities in place of those it had. An identity that a set no registration refers
// to holds, one kept unregistered, leaves it; one that a registered set holds is ErrIdentityConflict.
func (d *DB) SaveSet(ctx context.Context, s ImplicitSet) (_ ImplicitSet, err error) {
	defer d.observe(poolWrite, &err)()

	tx, err := d.conn.BeginTx(ctx, nil)
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

// GetSet returns the set with the ID.
func (d *DB) GetSet(ctx context.Context, id int64) (_ ImplicitSet, err error) {
	defer d.observe(poolWrite, &err)()

	sets, err := querySets(ctx, d.conn, `SELECT `+setColumns+` FROM implicit_sets WHERE id = ?`, id)
	if err != nil {
		return ImplicitSet{}, fmt.Errorf("get implicit set %d: %w", id, err)
	}

	if len(sets) == 0 {
		return ImplicitSet{}, fmt.Errorf("get implicit set %d: %w", id, ErrNotFound)
	}

	return sets[0], nil
}

// FindSets returns the sets that hold one of the identity keys as one of their public user identities.
func (d *DB) FindSets(ctx context.Context, keys []string) (_ []ImplicitSet, err error) {
	defer d.observe(poolWrite, &err)()

	if len(keys) == 0 {
		return []ImplicitSet{}, nil
	}

	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}

	sets, err := querySets(ctx, d.conn, `SELECT `+setColumns+` FROM implicit_sets
		WHERE id IN (SELECT set_id FROM set_identities WHERE key IN (`+placeholders(len(keys))+`)) ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("find implicit sets of %d identities: %w", len(keys), err)
	}

	return sets, nil
}

// ListUnregisteredSets returns the sets kept unregistered, by expiry.
func (d *DB) ListUnregisteredSets(ctx context.Context) (_ []ImplicitSet, err error) {
	defer d.observe(poolWrite, &err)()

	sets, err := querySets(ctx, d.conn,
		`SELECT `+setColumns+` FROM implicit_sets WHERE expires_at IS NOT NULL ORDER BY expires_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list unregistered implicit sets: %w", err)
	}

	return sets, nil
}

// FindUnregisteredSets returns the sets kept unregistered that may serve a public identity: those that hold one of its
// keys, those kept for one of its forms, and those kept for a wildcarded identity, which the caller matches.
func (d *DB) FindUnregisteredSets(ctx context.Context, keys []string) (_ []ImplicitSet, err error) {
	defer d.observe(poolWrite, &err)()

	if len(keys) == 0 {
		return []ImplicitSet{}, nil
	}

	args := make([]any, 0, 2*len(keys))
	for _, k := range keys {
		args = append(args, k)
	}

	args = append(args, args...)

	sets, err := querySets(ctx, d.conn, `SELECT `+setColumns+` FROM implicit_sets WHERE expires_at IS NOT NULL AND (
		id IN (SELECT set_id FROM set_identities WHERE key IN (`+placeholders(len(keys))+`))
		OR unregistered_identity IN (`+placeholders(len(keys))+`) OR wildcard IS NOT NULL) ORDER BY expires_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("find unregistered implicit sets of %d identities: %w", len(keys), err)
	}

	return sets, nil
}

// DeleteSet removes a set no registration refers to.
func (d *DB) DeleteSet(ctx context.Context, id int64) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx,
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

// SaveRegistration stores the registration and its bindings, in place of those it had. A registration without a set
// is of the set that holds its identities, or else of a new set of its Identities, UserData and HSS.
func (d *DB) SaveRegistration(ctx context.Context, r Registration) (_ Registration, err error) {
	defer d.observe(poolWrite, &err)()

	tx, err := d.conn.BeginTx(ctx, nil)
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

// SaveAssignment stores the set that a server assignment answered for, as SaveSet does, and the registration of it by
// a private identity, as SaveRegistration does, at once: in place of the registrations replaced, of the same private
// identity, which it deletes, with their sets when no registration refers to them anymore; and after the sets losing,
// registered, which give up identities of s, stored first. A set is never stored without the registration it is
// stored for.
func (d *DB) SaveAssignment(ctx context.Context, s ImplicitSet, r Registration, replaced []int64, losing []ImplicitSet,
) (_ ImplicitSet, _ Registration, err error) {
	defer d.observe(poolWrite, &err)()

	tx, err := d.conn.BeginTx(ctx, nil)
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

// setOf is the set of a registration without one: the set that holds its identities, or else a new one.
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

// DeleteRegistration removes the registration, and its set when no other registration refers to it.
func (d *DB) DeleteRegistration(ctx context.Context, id int64) (err error) {
	defer d.observe(poolWrite, &err)()

	tx, err := d.conn.BeginTx(ctx, nil)
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
	defer d.observe(poolWrite, &err)()

	regs, err := queryRegistrations(ctx, d.conn,
		`SELECT `+registrationColumns+registrationFrom+` WHERE r.impi = ? ORDER BY r.id`, impi)
	if err != nil {
		return nil, fmt.Errorf("list registrations of %s: %w", impi, err)
	}

	return regs, nil
}

// ListRegistrationsBySet returns the registrations of the set, by every private identity.
func (d *DB) ListRegistrationsBySet(ctx context.Context, setID int64) (_ []Registration, err error) {
	defer d.observe(poolWrite, &err)()

	regs, err := queryRegistrations(ctx, d.conn,
		`SELECT `+registrationColumns+registrationFrom+` WHERE r.set_id = ? ORDER BY r.id`, setID)
	if err != nil {
		return nil, fmt.Errorf("list registrations of set %d: %w", setID, err)
	}

	return regs, nil
}

func (d *DB) ListRegistrationsByIdentity(ctx context.Context, key string) (_ []Registration, err error) {
	defer d.observe(poolWrite, &err)()

	regs, err := queryRegistrations(ctx, d.conn,
		`SELECT `+registrationColumns+registrationFrom+`
		WHERE r.set_id IN (SELECT set_id FROM set_identities WHERE key = ?) ORDER BY r.id`, key)
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
		`SELECT `+registrationColumns+registrationFrom+`
		WHERE r.set_id IN (SELECT set_id FROM set_identities WHERE key IN (`+placeholders(len(keys))+`)) ORDER BY r.id`,
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
		`SELECT `+registrationColumns+registrationFrom+` ORDER BY r.id LIMIT ? OFFSET ?`, perPage, (page-1)*perPage)
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

// loadIdentities returns the identities of the sets, by set.
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
