package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// PCSCFRegistration is a UE's registration as the P-CSCF sees it
// (TS 24.229 §5.2.2): one per private identity and UE address.
type PCSCFRegistration struct {
	ID             int64
	IMPI           string
	FlowToken      string
	Transport      string
	UEAddress      netip.AddrPort
	PCSCFAddress   netip.Addr
	Contacts       []string
	AssociatedURIs []string
	ServiceRoute   []string
	ExpiresAt      time.Time
}

const pcscfRegistrationColumns = `id, impi, flow_token, transport, ue_address, ue_port, pcscf_address,
	contacts, associated_uris, service_route, expires_at`

// SavePCSCFRegistration inserts or replaces the registration of its private
// identity and UE address.
func (d *DB) SavePCSCFRegistration(ctx context.Context, r PCSCFRegistration) (PCSCFRegistration, error) {
	contacts, err := json.Marshal(r.Contacts)
	if err != nil {
		return PCSCFRegistration{}, fmt.Errorf("save P-CSCF registration: %w", err)
	}

	associated, err := json.Marshal(r.AssociatedURIs)
	if err != nil {
		return PCSCFRegistration{}, fmt.Errorf("save P-CSCF registration: %w", err)
	}

	route, err := json.Marshal(r.ServiceRoute)
	if err != nil {
		return PCSCFRegistration{}, fmt.Errorf("save P-CSCF registration: %w", err)
	}

	saved, err := scanPCSCFRegistration(d.conn.QueryRowContext(ctx,
		`INSERT INTO pcscf_registrations (impi, flow_token, transport, ue_address, ue_port, pcscf_address,
			contacts, associated_uris, service_route, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (impi, ue_address) DO UPDATE SET flow_token = excluded.flow_token, transport = excluded.transport,
			ue_port = excluded.ue_port, pcscf_address = excluded.pcscf_address, contacts = excluded.contacts,
			associated_uris = excluded.associated_uris, service_route = excluded.service_route,
			expires_at = excluded.expires_at
		RETURNING `+pcscfRegistrationColumns,
		r.IMPI, r.FlowToken, r.Transport, r.UEAddress.Addr().String(), r.UEAddress.Port(), r.PCSCFAddress.String(),
		contacts, associated, route, r.ExpiresAt.UTC().UnixNano()))
	if err != nil {
		return PCSCFRegistration{}, fmt.Errorf("save P-CSCF registration: %w", err)
	}

	return saved, nil
}

func (d *DB) DeletePCSCFRegistration(ctx context.Context, id int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM pcscf_registrations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete P-CSCF registration: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete P-CSCF registration: %w", err)
	}

	return nil
}

func (d *DB) ListPCSCFRegistrations(ctx context.Context) ([]PCSCFRegistration, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+pcscfRegistrationColumns+` FROM pcscf_registrations ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list P-CSCF registrations: %w", err)
	}

	defer func() { _ = rows.Close() }()

	regs := []PCSCFRegistration{}

	for rows.Next() {
		r, err := scanPCSCFRegistration(rows)
		if err != nil {
			return nil, fmt.Errorf("list P-CSCF registrations: %w", err)
		}

		regs = append(regs, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list P-CSCF registrations: %w", err)
	}

	return regs, nil
}

func scanPCSCFRegistration(row scanner) (PCSCFRegistration, error) {
	var (
		r                           PCSCFRegistration
		ue, pcscf                   string
		port                        uint16
		contacts, associated, route []byte
		expiresAt                   int64
	)

	if err := row.Scan(&r.ID, &r.IMPI, &r.FlowToken, &r.Transport, &ue, &port, &pcscf,
		&contacts, &associated, &route, &expiresAt); err != nil {
		return PCSCFRegistration{}, err
	}

	ueAddr, err := netip.ParseAddr(ue)
	if err != nil {
		return PCSCFRegistration{}, err
	}

	if r.PCSCFAddress, err = netip.ParseAddr(pcscf); err != nil {
		return PCSCFRegistration{}, err
	}

	r.UEAddress = netip.AddrPortFrom(ueAddr, port)
	r.ExpiresAt = time.Unix(0, expiresAt).UTC()

	for _, f := range []struct {
		b []byte
		v *[]string
	}{{contacts, &r.Contacts}, {associated, &r.AssociatedURIs}, {route, &r.ServiceRoute}} {
		if err := json.Unmarshal(f.b, f.v); err != nil {
			return PCSCFRegistration{}, err
		}
	}

	return r, nil
}

// PCSCFSubscription is the P-CSCF's own subscription to the reg event
// package of a private identity (TS 24.229 §5.2.3). Dialog is empty until
// the dialog exists.
type PCSCFSubscription struct {
	ID        int64
	IMPI      string
	IMPU      string
	CallID    string
	LocalTag  string
	Dialog    []byte
	Version   int64
	ExpiresAt time.Time
}

const pcscfSubscriptionColumns = `id, impi, impu, call_id, local_tag, dialog, version, expires_at`

// SavePCSCFSubscription inserts or replaces the subscription of its private
// identity.
func (d *DB) SavePCSCFSubscription(ctx context.Context, s PCSCFSubscription) (PCSCFSubscription, error) {
	saved, err := scanPCSCFSubscription(d.conn.QueryRowContext(ctx,
		`INSERT INTO pcscf_subscriptions (impi, impu, call_id, local_tag, dialog, version, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (impi) DO UPDATE SET impu = excluded.impu, call_id = excluded.call_id,
			local_tag = excluded.local_tag, dialog = excluded.dialog, version = excluded.version,
			expires_at = excluded.expires_at
		RETURNING `+pcscfSubscriptionColumns,
		s.IMPI, s.IMPU, s.CallID, s.LocalTag, s.Dialog, s.Version, s.ExpiresAt.UTC().UnixNano()))
	if err != nil {
		return PCSCFSubscription{}, fmt.Errorf("save P-CSCF subscription: %w", err)
	}

	return saved, nil
}

func (d *DB) DeletePCSCFSubscription(ctx context.Context, impi string) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM pcscf_subscriptions WHERE impi = ?`, impi)
	if err != nil {
		return fmt.Errorf("delete P-CSCF subscription: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete P-CSCF subscription: %w", err)
	}

	return nil
}

func (d *DB) ListPCSCFSubscriptions(ctx context.Context) ([]PCSCFSubscription, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+pcscfSubscriptionColumns+` FROM pcscf_subscriptions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list P-CSCF subscriptions: %w", err)
	}

	defer func() { _ = rows.Close() }()

	subs := []PCSCFSubscription{}

	for rows.Next() {
		s, err := scanPCSCFSubscription(rows)
		if err != nil {
			return nil, fmt.Errorf("list P-CSCF subscriptions: %w", err)
		}

		subs = append(subs, s)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list P-CSCF subscriptions: %w", err)
	}

	return subs, nil
}

func scanPCSCFSubscription(row scanner) (PCSCFSubscription, error) {
	var (
		s         PCSCFSubscription
		expiresAt int64
	)

	err := row.Scan(&s.ID, &s.IMPI, &s.IMPU, &s.CallID, &s.LocalTag, &s.Dialog, &s.Version, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PCSCFSubscription{}, ErrNotFound
	}

	if err != nil {
		return PCSCFSubscription{}, err
	}

	s.ExpiresAt = time.Unix(0, expiresAt).UTC()

	return s, nil
}
