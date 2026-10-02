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

type PCSCFRegistration struct {
	ID             int64
	IMPI           string
	FlowToken      string
	Transport      string
	Protected      bool
	UEAddress      netip.AddrPort
	PCSCFAddress   netip.Addr
	Contacts       []string
	AssociatedURIs []string
	Sets           map[string][]string
	ServiceRoute   []string
	ExpiresAt      time.Time
}

const pcscfRegistrationColumns = `id, impi, flow_token, transport, protected, ue_address, ue_port, pcscf_address,
	contacts, associated_uris, sets, service_route, expires_at`

func (d *DB) SavePCSCFRegistration(ctx context.Context, r PCSCFRegistration) (PCSCFRegistration, error) {
	if !r.UEAddress.IsValid() || !r.PCSCFAddress.IsValid() {
		return PCSCFRegistration{}, errors.New("save P-CSCF registration: invalid address")
	}

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

	if r.Sets == nil {
		r.Sets = map[string][]string{}
	}

	sets, err := json.Marshal(r.Sets)
	if err != nil {
		return PCSCFRegistration{}, fmt.Errorf("save P-CSCF registration: %w", err)
	}

	saved, err := scanPCSCFRegistration(d.conn.QueryRowContext(ctx,
		`INSERT INTO pcscf_registrations (impi, flow_token, transport, protected, ue_address, ue_port, pcscf_address,
			contacts, associated_uris, sets, service_route, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (impi, ue_address) DO UPDATE SET flow_token = excluded.flow_token, transport = excluded.transport,
			protected = excluded.protected,
			ue_port = excluded.ue_port, pcscf_address = excluded.pcscf_address, contacts = excluded.contacts,
			associated_uris = excluded.associated_uris, sets = excluded.sets, service_route = excluded.service_route,
			expires_at = excluded.expires_at
		RETURNING `+pcscfRegistrationColumns,
		r.IMPI, r.FlowToken, r.Transport, r.Protected, r.UEAddress.Addr().String(), r.UEAddress.Port(), r.PCSCFAddress.String(),
		contacts, associated, sets, route, r.ExpiresAt.UTC().UnixNano()))
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
	regs, bad, err := d.listPCSCFRegistrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list P-CSCF registrations: %w", err)
	}

	for _, id := range bad {
		if _, err := d.conn.ExecContext(ctx, `DELETE FROM pcscf_registrations WHERE id = ?`, id); err != nil {
			return nil, fmt.Errorf("list P-CSCF registrations: delete unparsable row %d: %w", id, err)
		}
	}

	return regs, nil
}

func (d *DB) listPCSCFRegistrations(ctx context.Context) ([]PCSCFRegistration, []int64, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+pcscfRegistrationColumns+` FROM pcscf_registrations ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}

	defer func() { _ = rows.Close() }()

	var (
		regs = []PCSCFRegistration{}
		bad  []int64
	)

	for rows.Next() {
		raw, err := scanRawPCSCFRegistration(rows)
		if err != nil {
			return nil, nil, err
		}

		r, err := raw.parse()
		if err != nil {
			bad = append(bad, raw.ID)
			continue
		}

		regs = append(regs, r)
	}

	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	return regs, bad, nil
}

type rawPCSCFRegistration struct {
	PCSCFRegistration
	ue, pcscf                   string
	port                        uint16
	contacts, associated, route []byte
	sets                        []byte
	expiresAt                   int64
}

func scanRawPCSCFRegistration(row scanner) (rawPCSCFRegistration, error) {
	var raw rawPCSCFRegistration

	err := row.Scan(&raw.ID, &raw.IMPI, &raw.FlowToken, &raw.Transport, &raw.Protected, &raw.ue, &raw.port, &raw.pcscf,
		&raw.contacts, &raw.associated, &raw.sets, &raw.route, &raw.expiresAt)

	return raw, err
}

func (raw rawPCSCFRegistration) parse() (PCSCFRegistration, error) {
	r := raw.PCSCFRegistration

	ueAddr, err := netip.ParseAddr(raw.ue)
	if err != nil {
		return PCSCFRegistration{}, err
	}

	if r.PCSCFAddress, err = netip.ParseAddr(raw.pcscf); err != nil {
		return PCSCFRegistration{}, err
	}

	r.UEAddress = netip.AddrPortFrom(ueAddr, raw.port)
	r.ExpiresAt = time.Unix(0, raw.expiresAt).UTC()

	for _, f := range []struct {
		b []byte
		v *[]string
	}{{raw.contacts, &r.Contacts}, {raw.associated, &r.AssociatedURIs}, {raw.route, &r.ServiceRoute}} {
		if err := json.Unmarshal(f.b, f.v); err != nil {
			return PCSCFRegistration{}, err
		}
	}

	if err := json.Unmarshal(raw.sets, &r.Sets); err != nil {
		return PCSCFRegistration{}, err
	}

	return r, nil
}

func scanPCSCFRegistration(row scanner) (PCSCFRegistration, error) {
	raw, err := scanRawPCSCFRegistration(row)
	if err != nil {
		return PCSCFRegistration{}, err
	}

	return raw.parse()
}

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
