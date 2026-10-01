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

// ErrIdentityConflict is returned when a public identity is already part of
// another phone's registration.
var ErrIdentityConflict = errors.New("public identity registered by another phone")

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

// PublicIdentity is one IMPU of the implicit registration set. URI is in
// normalized form.
type PublicIdentity struct {
	URI    string
	Barred bool
}

// Registration is a registered phone. IPsec is nil for a plain-SIP phone.
// Path is the REGISTER's Path header value, as received. Identities are in
// profile order: the first one is the default IMPU.
type Registration struct {
	ID           int64
	IMPI         string
	Contact      string
	InstanceID   string
	CallID       string
	CSeq         int64
	UEAddress    netip.Addr
	Path         string
	IPsec        *SecurityAssociations
	RxSessionID  string
	Identities   []PublicIdentity
	RegisteredAt time.Time
	ExpiresAt    time.Time
}

// RegistrationRefresh is what a re-registration changes.
type RegistrationRefresh struct {
	CallID    string
	CSeq      int64
	Path      string
	IPsec     *SecurityAssociations
	ExpiresAt time.Time
}

const registrationColumns = `id, impi, contact, instance_id, call_id, cseq, ue_address, path,
	ue_port_c, ue_port_s, pcscf_port_c, pcscf_port_s, spi_uc, spi_us, spi_pc, spi_ps, alg, ealg,
	rx_session_id, registered_at, expires_at`

// PutRegistration stores an initial registration. It replaces any registration
// of the same IMPI, with its identities and subscriptions.
func (d *DB) PutRegistration(ctx context.Context, r Registration) (int64, error) {
	if !r.UEAddress.IsValid() {
		return 0, errors.New("put registration: UE address is required")
	}

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("put registration: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM registrations WHERE impi = ?`, r.IMPI); err != nil {
		return 0, fmt.Errorf("put registration: %w", err)
	}

	args := []any{r.IMPI, r.Contact, nullableString(r.InstanceID), r.CallID, r.CSeq, r.UEAddress.String(), nullableString(r.Path)}
	args = append(args, securityAssociationArgs(r.IPsec)...)
	args = append(args, nullableString(r.RxSessionID), r.RegisteredAt.UTC().UnixNano(), r.ExpiresAt.UTC().UnixNano())

	res, err := tx.ExecContext(ctx,
		`INSERT INTO registrations (impi, contact, instance_id, call_id, cseq, ue_address, path,
			ue_port_c, ue_port_s, pcscf_port_c, pcscf_port_s, spi_uc, spi_us, spi_pc, spi_ps, alg, ealg,
			rx_session_id, registered_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
	if err != nil {
		return 0, fmt.Errorf("put registration: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("put registration: %w", err)
	}

	for i, identity := range r.Identities {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO registration_identities (registration_id, position, uri, barred) VALUES (?, ?, ?, ?)`,
			id, i, identity.URI, identity.Barred)
		if isConstraint(err, sqlite3.ErrConstraintUnique) {
			return 0, fmt.Errorf("put registration: %s: %w", identity.URI, ErrIdentityConflict)
		}

		if err != nil {
			return 0, fmt.Errorf("put registration: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("put registration: %w", err)
	}

	return id, nil
}

// RefreshRegistration records a re-registration. Re-registration creates new
// SAs and may come through a new Path, so they are replaced too.
func (d *DB) RefreshRegistration(ctx context.Context, impi string, u RegistrationRefresh) error {
	args := []any{u.CallID, u.CSeq, nullableString(u.Path)}
	args = append(args, securityAssociationArgs(u.IPsec)...)
	args = append(args, u.ExpiresAt.UTC().UnixNano(), impi)

	res, err := d.conn.ExecContext(ctx,
		`UPDATE registrations SET call_id = ?, cseq = ?, path = ?,
			ue_port_c = ?, ue_port_s = ?, pcscf_port_c = ?, pcscf_port_s = ?,
			spi_uc = ?, spi_us = ?, spi_pc = ?, spi_ps = ?, alg = ?, ealg = ?, expires_at = ?
		WHERE impi = ?`, args...)
	if err != nil {
		return fmt.Errorf("refresh registration: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("refresh registration: %w", err)
	}

	return nil
}

// SetRegistrationRxSession records the Session-Id of the registration's
// AF_SIGNALLING Rx session. An empty sessionID clears it.
func (d *DB) SetRegistrationRxSession(ctx context.Context, impi, sessionID string) error {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE registrations SET rx_session_id = ? WHERE impi = ?`, nullableString(sessionID), impi)
	if err != nil {
		return fmt.Errorf("set registration Rx session: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("set registration Rx session: %w", err)
	}

	return nil
}

// GetRegistration returns the registration of an IMPI. Expired registrations
// are returned until they are deleted, so the caller checks ExpiresAt.
func (d *DB) GetRegistration(ctx context.Context, impi string) (Registration, error) {
	regs, err := queryRegistrations(ctx, d.conn,
		`SELECT `+registrationColumns+` FROM registrations WHERE impi = ?`, impi)
	if err != nil {
		return Registration{}, fmt.Errorf("get registration: %w", err)
	}

	if len(regs) == 0 {
		return Registration{}, ErrNotFound
	}

	return regs[0], nil
}

// GetRegistrationByIdentity returns the registration holding a normalized
// public identity, barred or not. Expired registrations are returned until
// they are deleted, so the caller checks ExpiresAt.
func (d *DB) GetRegistrationByIdentity(ctx context.Context, uri string) (Registration, error) {
	regs, err := queryRegistrations(ctx, d.conn,
		`SELECT `+registrationColumns+` FROM registrations
		WHERE id = (SELECT registration_id FROM registration_identities WHERE uri = ?)`, uri)
	if err != nil {
		return Registration{}, fmt.Errorf("get registration by identity: %w", err)
	}

	if len(regs) == 0 {
		return Registration{}, ErrNotFound
	}

	return regs[0], nil
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

func (d *DB) DeleteRegistration(ctx context.Context, impi string) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM registrations WHERE impi = ?`, impi)
	if err != nil {
		return fmt.Errorf("delete registration: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete registration: %w", err)
	}

	return nil
}

// DeleteExpiredRegistrations deletes registrations whose expiry is at or before
// now, and returns them so the caller can tear down their SAs and Rx sessions
// and tell the HSS.
func (d *DB) DeleteExpiredRegistrations(ctx context.Context, now time.Time) ([]Registration, error) {
	at := now.UTC().UnixNano()

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("delete expired registrations: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	regs, err := queryRegistrations(ctx, tx,
		`SELECT `+registrationColumns+` FROM registrations WHERE expires_at <= ? ORDER BY id`, at)
	if err != nil {
		return nil, fmt.Errorf("delete expired registrations: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM registrations WHERE expires_at <= ?`, at); err != nil {
		return nil, fmt.Errorf("delete expired registrations: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("delete expired registrations: %w", err)
	}

	return regs, nil
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

	if err := loadIdentities(ctx, q, regs); err != nil {
		return nil, err
	}

	return regs, nil
}

func loadIdentities(ctx context.Context, q querier, regs []Registration) error {
	if len(regs) == 0 {
		return nil
	}

	index := make(map[int64]int, len(regs))
	ids := make([]any, 0, len(regs))

	for i, r := range regs {
		index[r.ID] = i
		ids = append(ids, r.ID)
	}

	rows, err := q.QueryContext(ctx,
		`SELECT registration_id, uri, barred FROM registration_identities
		WHERE registration_id IN (`+placeholders(len(ids))+`) ORDER BY registration_id, position`, ids...)
	if err != nil {
		return err
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			registrationID int64
			identity       PublicIdentity
		)

		if err := rows.Scan(&registrationID, &identity.URI, &identity.Barred); err != nil {
			return err
		}

		r := &regs[index[registrationID]]
		r.Identities = append(r.Identities, identity)
	}

	return rows.Err()
}

func scanRegistration(row scanner) (Registration, error) {
	var (
		r                                        Registration
		instanceID, path, rxSessionID, alg, ealg sql.NullString
		ueAddress                                string
		uePortC, uePortS, pcscfPortC, pcscfPortS sql.Null[uint16]
		spiUC, spiUS, spiPC, spiPS               sql.Null[uint32]
		registeredAt, expiresAt                  int64
	)

	if err := row.Scan(&r.ID, &r.IMPI, &r.Contact, &instanceID, &r.CallID, &r.CSeq, &ueAddress, &path,
		&uePortC, &uePortS, &pcscfPortC, &pcscfPortS, &spiUC, &spiUS, &spiPC, &spiPS, &alg, &ealg,
		&rxSessionID, &registeredAt, &expiresAt); err != nil {
		return Registration{}, err
	}

	addr, err := netip.ParseAddr(ueAddress)
	if err != nil {
		return Registration{}, fmt.Errorf("registration %d: %w", r.ID, err)
	}

	r.UEAddress = addr
	r.InstanceID = instanceID.String
	r.Path = path.String
	r.RxSessionID = rxSessionID.String
	r.RegisteredAt = time.Unix(0, registeredAt).UTC()
	r.ExpiresAt = time.Unix(0, expiresAt).UTC()

	// The table's CHECK constraint keeps the SA columns all set or all NULL.
	if alg.Valid {
		r.IPsec = &SecurityAssociations{
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

	return r, nil
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
