package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

type SecurityAssociationState string

const (
	SecurityAssociationEstablished SecurityAssociationState = "established"
	SecurityAssociationOld         SecurityAssociationState = "old"
)

type SecurityAssociation struct {
	ID           int64
	IMPI         string
	State        SecurityAssociationState
	PCSCFAddress netip.Addr
	UEAddress    netip.Addr
	Instance     string
	RegID        int64
	PCSCFPortC   uint16
	PCSCFPortS   uint16
	UEPortC      uint16
	UEPortS      uint16
	SPIPC        uint32
	SPIPS        uint32
	SPIUC        uint32
	SPIUS        uint32
	Integrity    string
	Encryption   string
	ExpiresAt    time.Time
}

const securityAssociationColumns = `id, impi, state, pcscf_address, ue_address, instance_id, reg_id,
	pcscf_port_c, pcscf_port_s, ue_port_c, ue_port_s, spi_pc, spi_ps, spi_uc, spi_us, alg, ealg, expires_at`

func (d *DB) SaveSecurityAssociation(ctx context.Context, sa SecurityAssociation) (SecurityAssociation, error) {
	args := []any{
		sa.IMPI, sa.State, sa.PCSCFAddress.String(), sa.UEAddress.String(), nullableString(sa.Instance), nullableInt(sa.RegID),
		sa.PCSCFPortC, sa.PCSCFPortS, sa.UEPortC, sa.UEPortS, sa.SPIPC, sa.SPIPS, sa.SPIUC, sa.SPIUS,
		sa.Integrity, sa.Encryption, sa.ExpiresAt.UTC().UnixNano(),
	}

	var (
		saved SecurityAssociation
		err   error
	)

	if sa.ID == 0 {
		saved, err = scanSecurityAssociation(d.conn.QueryRowContext(ctx,
			`INSERT INTO security_associations (impi, state, pcscf_address, ue_address, instance_id, reg_id,
				pcscf_port_c, pcscf_port_s, ue_port_c, ue_port_s, spi_pc, spi_ps, spi_uc, spi_us, alg, ealg, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING `+securityAssociationColumns, args...))
	} else {
		saved, err = scanSecurityAssociation(d.conn.QueryRowContext(ctx,
			`UPDATE security_associations SET impi = ?, state = ?, pcscf_address = ?, ue_address = ?, instance_id = ?, reg_id = ?,
				pcscf_port_c = ?, pcscf_port_s = ?, ue_port_c = ?, ue_port_s = ?, spi_pc = ?, spi_ps = ?, spi_uc = ?, spi_us = ?,
				alg = ?, ealg = ?, expires_at = ?
			WHERE id = ?
			RETURNING `+securityAssociationColumns, append(args, sa.ID)...))
	}

	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}

	if err != nil {
		return SecurityAssociation{}, fmt.Errorf("save security association: %w", err)
	}

	return saved, nil
}

func (d *DB) DeleteSecurityAssociation(ctx context.Context, id int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM security_associations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete security association %d: %w", id, err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete security association %d: %w", id, err)
	}

	return nil
}

func (d *DB) ListSecurityAssociations(ctx context.Context) ([]SecurityAssociation, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+securityAssociationColumns+` FROM security_associations ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list security associations: %w", err)
	}

	defer func() { _ = rows.Close() }()

	out := []SecurityAssociation{}

	for rows.Next() {
		sa, err := scanSecurityAssociation(rows)
		if err != nil {
			return nil, fmt.Errorf("list security associations: %w", err)
		}

		out = append(out, sa)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list security associations: %w", err)
	}

	return out, nil
}

func scanSecurityAssociation(row scanner) (SecurityAssociation, error) {
	var (
		sa                SecurityAssociation
		pcscfAddr, ueAddr string
		instance          sql.NullString
		regID             sql.NullInt64
		expiresAt         int64
	)

	if err := row.Scan(&sa.ID, &sa.IMPI, &sa.State, &pcscfAddr, &ueAddr, &instance, &regID,
		&sa.PCSCFPortC, &sa.PCSCFPortS, &sa.UEPortC, &sa.UEPortS, &sa.SPIPC, &sa.SPIPS, &sa.SPIUC, &sa.SPIUS,
		&sa.Integrity, &sa.Encryption, &expiresAt); err != nil {
		return SecurityAssociation{}, err
	}

	sa.Instance, sa.RegID = instance.String, regID.Int64

	var err error

	if sa.PCSCFAddress, err = netip.ParseAddr(pcscfAddr); err != nil {
		return SecurityAssociation{}, fmt.Errorf("security association %d: %w", sa.ID, err)
	}

	if sa.UEAddress, err = netip.ParseAddr(ueAddr); err != nil {
		return SecurityAssociation{}, fmt.Errorf("security association %d: %w", sa.ID, err)
	}

	sa.ExpiresAt = time.Unix(0, expiresAt).UTC()

	return sa, nil
}
