package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/mattn/go-sqlite3"
)

type IntegrityAlgorithm string

const (
	IntegrityHMACMD596  IntegrityAlgorithm = "hmac-md5-96"
	IntegrityHMACSHA196 IntegrityAlgorithm = "hmac-sha-1-96"
)

type EncryptionAlgorithm string

const (
	EncryptionNull   EncryptionAlgorithm = "null"
	EncryptionAESCBC EncryptionAlgorithm = "aes-cbc"
)

var ErrIdentityConflict = errors.New("public identity in another registration set of the private identity")

// SecurityAssociations identifies the four IPsec SAs of a registration
// (TS 33.203 §7). The keys stay in the kernel and aren't stored.
type SecurityAssociations struct {
	UEPortC    uint16
	UEPortS    uint16
	PCSCFPortC uint16
	PCSCFPortS uint16
	SPIUC      uint32
	SPIUS      uint32
	SPIPC      uint32
	SPIPS      uint32
	Integrity  IntegrityAlgorithm
	Encryption EncryptionAlgorithm
}

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

// Contact is a registered contact. The S-CSCF owns its URI, Params and
// Path. The P-CSCF owns UEAddress, IPsec and RxSessionID: SaveRegistration
// sets them only on a new contact, and SetContactFlow and
// SetContactRxSession change them.
type Contact struct {
	ID          int64
	IMPI        string
	URI         string
	Params      string
	Path        string
	UEAddress   netip.Addr
	IPsec       *SecurityAssociations
	RxSessionID string
}

type Binding struct {
	Contact   Contact
	CallID    string
	CSeq      int64
	ExpiresAt time.Time
}

const (
	registrationColumns = `id, impi, impu, user_data`

	contactColumns = `c.id, c.impi, c.uri, c.params, c.path, c.ue_address,
	c.ue_port_c, c.ue_port_s, c.pcscf_port_c, c.pcscf_port_s, c.spi_uc, c.spi_us, c.spi_pc, c.spi_ps, c.alg, c.ealg,
	c.rx_session_id`

	returnedContactColumns = `id, impi, uri, params, path, ue_address,
	ue_port_c, ue_port_s, pcscf_port_c, pcscf_port_s, spi_uc, spi_us, spi_pc, spi_ps, alg, ealg, rx_session_id`
)

func (d *DB) SaveRegistration(ctx context.Context, r Registration) (Registration, error) {
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

	if _, err := tx.ExecContext(ctx, `DELETE FROM bindings WHERE registration_id = ?`, r.ID); err != nil {
		return Registration{}, fmt.Errorf("save registration: %w", err)
	}

	r.Bindings = append([]Binding(nil), r.Bindings...)

	for i := range r.Bindings {
		b := &r.Bindings[i]
		b.Contact.IMPI = r.IMPI

		if b.Contact, err = saveContact(ctx, tx, b.Contact); err != nil {
			return Registration{}, fmt.Errorf("save registration: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO bindings (registration_id, contact_id, call_id, cseq, expires_at) VALUES (?, ?, ?, ?, ?)`,
			r.ID, b.Contact.ID, b.CallID, b.CSeq, b.ExpiresAt.UTC().UnixNano()); err != nil {
			return Registration{}, fmt.Errorf("save registration: %w", err)
		}
	}

	if err := deleteUnboundContacts(ctx, tx, r.IMPI); err != nil {
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

// saveContact inserts a contact, or updates the S-CSCF's columns of an
// existing one: the P-CSCF's may have changed since the S-CSCF read them. It
// returns the contact as stored.
func saveContact(ctx context.Context, tx *sql.Tx, c Contact) (Contact, error) {
	args := []any{c.IMPI, c.URI, c.Params, nullableString(c.Path), addressArg(c.UEAddress)}
	args = append(args, securityAssociationArgs(c.IPsec)...)
	args = append(args, nullableString(c.RxSessionID))

	return scanContact(tx.QueryRowContext(ctx,
		`INSERT INTO contacts (impi, uri, params, path, ue_address,
			ue_port_c, ue_port_s, pcscf_port_c, pcscf_port_s, spi_uc, spi_us, spi_pc, spi_ps, alg, ealg, rx_session_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (impi, uri) DO UPDATE SET params = excluded.params, path = excluded.path
		RETURNING `+returnedContactColumns, args...))
}

func addressArg(a netip.Addr) any {
	if !a.IsValid() {
		return nil
	}

	return a.String()
}

func deleteUnboundContacts(ctx context.Context, tx *sql.Tx, impi string) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM contacts WHERE impi = ? AND id NOT IN (SELECT contact_id FROM bindings)`, impi)

	return err
}

func (d *DB) DeleteRegistration(ctx context.Context, id int64) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete registration: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var impi string
	if err := tx.QueryRowContext(ctx, `DELETE FROM registrations WHERE id = ? RETURNING impi`, id).Scan(&impi); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}

		return fmt.Errorf("delete registration %d: %w", id, err)
	}

	if err := deleteUnboundContacts(ctx, tx, impi); err != nil {
		return fmt.Errorf("delete registration %d: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete registration %d: %w", id, err)
	}

	return nil
}

func (d *DB) ListRegistrationsByIMPI(ctx context.Context, impi string) ([]Registration, error) {
	regs, err := queryRegistrations(ctx, d.conn,
		`SELECT `+registrationColumns+` FROM registrations WHERE impi = ? ORDER BY id`, impi)
	if err != nil {
		return nil, fmt.Errorf("list registrations of %s: %w", impi, err)
	}

	return regs, nil
}

func (d *DB) ListRegistrationsByIdentity(ctx context.Context, key string) ([]Registration, error) {
	regs, err := queryRegistrations(ctx, d.conn,
		`SELECT `+registrationColumns+` FROM registrations
		WHERE id IN (SELECT registration_id FROM registration_identities WHERE key = ?) ORDER BY id`, key)
	if err != nil {
		return nil, fmt.Errorf("list registrations of %s: %w", key, err)
	}

	return regs, nil
}

func (d *DB) ListRegistrations(ctx context.Context, page, perPage int) ([]Registration, int, error) {
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

func (d *DB) ListExpiredIMPIs(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT DISTINCT r.impi FROM bindings b JOIN registrations r ON r.id = b.registration_id
		WHERE b.expires_at <= ? ORDER BY r.impi`, now.UTC().UnixNano())
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

// SetContactFlow records the address the UE registered the contact from and
// the IPsec SAs protecting it, nil for none.
func (d *DB) SetContactFlow(ctx context.Context, contactID int64, ueAddress netip.Addr, sa *SecurityAssociations) error {
	args := []any{addressArg(ueAddress)}
	args = append(args, securityAssociationArgs(sa)...)
	args = append(args, contactID)

	res, err := d.conn.ExecContext(ctx,
		`UPDATE contacts SET ue_address = ?,
			ue_port_c = ?, ue_port_s = ?, pcscf_port_c = ?, pcscf_port_s = ?,
			spi_uc = ?, spi_us = ?, spi_pc = ?, spi_ps = ?, alg = ?, ealg = ?
		WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("set contact flow: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("set contact flow: %w", err)
	}

	return nil
}

func (d *DB) SetContactRxSession(ctx context.Context, contactID int64, sessionID string) error {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE contacts SET rx_session_id = ? WHERE id = ?`, nullableString(sessionID), contactID)
	if err != nil {
		return fmt.Errorf("set contact Rx session: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("set contact Rx session: %w", err)
	}

	return nil
}

func queryRegistrations(ctx context.Context, q querier, query string, args ...any) ([]Registration, error) {
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

func loadIdentities(ctx context.Context, q querier, regs []Registration, index map[int64]int, ids []any) error {
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

func loadBindings(ctx context.Context, q querier, regs []Registration, index map[int64]int, ids []any) error {
	rows, err := q.QueryContext(ctx,
		`SELECT b.registration_id, b.call_id, b.cseq, b.expires_at, `+contactColumns+`
		FROM bindings b JOIN contacts c ON c.id = b.contact_id
		WHERE b.registration_id IN (`+placeholders(len(ids))+`) ORDER BY b.registration_id, c.id`, ids...)
	if err != nil {
		return err
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			registrationID, expiresAt int64
			b                         Binding
		)

		if b.Contact, err = scanContact(rows, &registrationID, &b.CallID, &b.CSeq, &expiresAt); err != nil {
			return err
		}

		b.ExpiresAt = time.Unix(0, expiresAt).UTC()

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

func scanContact(row scanner, leading ...any) (Contact, error) {
	var (
		c                                        Contact
		path, rxSessionID, alg, ealg, ueAddress  sql.NullString
		uePortC, uePortS, pcscfPortC, pcscfPortS sql.Null[uint16]
		spiUC, spiUS, spiPC, spiPS               sql.Null[uint32]
	)

	dest := append(leading, &c.ID, &c.IMPI, &c.URI, &c.Params, &path, &ueAddress,
		&uePortC, &uePortS, &pcscfPortC, &pcscfPortS, &spiUC, &spiUS, &spiPC, &spiPS, &alg, &ealg, &rxSessionID)

	if err := row.Scan(dest...); err != nil {
		return Contact{}, err
	}

	if ueAddress.Valid {
		addr, err := netip.ParseAddr(ueAddress.String)
		if err != nil {
			return Contact{}, fmt.Errorf("contact %d: %w", c.ID, err)
		}

		c.UEAddress = addr
	}

	c.Path = path.String
	c.RxSessionID = rxSessionID.String

	// The table's CHECK constraint keeps the SA columns all set or all NULL.
	if alg.Valid {
		c.IPsec = &SecurityAssociations{
			UEPortC:    uePortC.V,
			UEPortS:    uePortS.V,
			PCSCFPortC: pcscfPortC.V,
			PCSCFPortS: pcscfPortS.V,
			SPIUC:      spiUC.V,
			SPIUS:      spiUS.V,
			SPIPC:      spiPC.V,
			SPIPS:      spiPS.V,
			Integrity:  IntegrityAlgorithm(alg.String),
			Encryption: EncryptionAlgorithm(ealg.String),
		}
	}

	return c, nil
}

// securityAssociationArgs returns the values of ue_port_c through ealg.
func securityAssociationArgs(sa *SecurityAssociations) []any {
	if sa == nil {
		return make([]any, 10)
	}

	return []any{
		sa.UEPortC, sa.UEPortS, sa.PCSCFPortC, sa.PCSCFPortS,
		sa.SPIUC, sa.SPIUS, sa.SPIPC, sa.SPIPS, sa.Integrity, sa.Encryption,
	}
}
