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

// PCSCFRegistration is a registration through the P-CSCF of a UE, identified by its private identity and
// address, and by its Instance and RegID when it is one of the UE's registration flows (RFC 5626).
type PCSCFRegistration struct {
	ID             int64
	IMPI           string
	Instance       string
	RegID          int64
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
	Policy         PolicySession
	SignallingLost bool
}

// PolicySession is the P-CSCF's session for IMS signalling with a policy function (TS 29.214 §4.4.5,
// TS 29.514 §4.2.6.7): the endpoint it was opened with, its session ID and the backend's reference to it.
type PolicySession struct {
	Endpoint string
	ID       string
	Ref      string
}

const pcscfRegistrationColumns = `id, impi, instance_id, reg_id, flow_token, transport, protected, ue_address, ue_port, pcscf_address,
	contacts, associated_uris, sets, service_route, expires_at, policy_endpoint, policy_session_id, policy_ref,
	signalling_lost`

func (d *DB) SavePCSCFRegistration(ctx context.Context, r PCSCFRegistration) (_ PCSCFRegistration, err error) {
	defer d.observe(poolWrite, &err)()

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

	conflict := `ON CONFLICT (impi, ue_address) WHERE reg_id IS NULL`
	if r.RegID != 0 {
		conflict = `ON CONFLICT (impi, ue_address, instance_id, reg_id) WHERE reg_id IS NOT NULL`
	}

	saved, err := scanPCSCFRegistration(d.conn.QueryRowContext(ctx,
		`INSERT INTO pcscf_registrations (impi, instance_id, reg_id, flow_token, transport, protected, ue_address, ue_port,
			pcscf_address, contacts, associated_uris, sets, service_route, expires_at, policy_endpoint, policy_session_id,
			policy_ref, signalling_lost)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`+conflict+` DO UPDATE SET flow_token = excluded.flow_token, transport = excluded.transport,
			protected = excluded.protected,
			ue_port = excluded.ue_port, pcscf_address = excluded.pcscf_address, contacts = excluded.contacts,
			associated_uris = excluded.associated_uris, sets = excluded.sets, service_route = excluded.service_route,
			expires_at = excluded.expires_at, policy_endpoint = excluded.policy_endpoint,
			policy_session_id = excluded.policy_session_id, policy_ref = excluded.policy_ref,
			signalling_lost = excluded.signalling_lost
		RETURNING `+pcscfRegistrationColumns,
		r.IMPI, nullableString(r.Instance), nullableInt(r.RegID), r.FlowToken, r.Transport, r.Protected, r.UEAddress.Addr().String(), r.UEAddress.Port(), r.PCSCFAddress.String(),
		contacts, associated, sets, route, r.ExpiresAt.UTC().UnixNano(), nullableString(r.Policy.Endpoint),
		nullableString(r.Policy.ID), nullableString(r.Policy.Ref),
		r.SignallingLost))
	if err != nil {
		return PCSCFRegistration{}, fmt.Errorf("save P-CSCF registration: %w", err)
	}

	return saved, nil
}

func (d *DB) DeletePCSCFRegistration(ctx context.Context, id int64) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM pcscf_registrations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete P-CSCF registration: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete P-CSCF registration: %w", err)
	}

	return nil
}

func (d *DB) ListPCSCFRegistrations(ctx context.Context) (_ []PCSCFRegistration, err error) {
	defer d.observe(poolWrite, &err)()

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
	policy                      [3]sql.NullString
	instance                    sql.NullString
	regID                       sql.NullInt64
}

func scanRawPCSCFRegistration(row scanner) (rawPCSCFRegistration, error) {
	var raw rawPCSCFRegistration

	err := row.Scan(&raw.ID, &raw.IMPI, &raw.instance, &raw.regID, &raw.FlowToken, &raw.Transport, &raw.Protected, &raw.ue, &raw.port, &raw.pcscf,
		&raw.contacts, &raw.associated, &raw.sets, &raw.route, &raw.expiresAt, &raw.policy[0], &raw.policy[1], &raw.policy[2],
		&raw.SignallingLost)

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

	r.Instance, r.RegID = raw.instance.String, raw.regID.Int64
	r.UEAddress = netip.AddrPortFrom(ueAddr, raw.port)
	r.ExpiresAt = time.Unix(0, raw.expiresAt).UTC()
	r.Policy = PolicySession{Endpoint: raw.policy[0].String, ID: raw.policy[1].String, Ref: raw.policy[2].String}

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

func (d *DB) SavePCSCFSubscription(ctx context.Context, s PCSCFSubscription) (_ PCSCFSubscription, err error) {
	defer d.observe(poolWrite, &err)()

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

func (d *DB) DeletePCSCFSubscription(ctx context.Context, impi string) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM pcscf_subscriptions WHERE impi = ?`, impi)
	if err != nil {
		return fmt.Errorf("delete P-CSCF subscription: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete P-CSCF subscription: %w", err)
	}

	return nil
}

func (d *DB) ListPCSCFSubscriptions(ctx context.Context) (_ []PCSCFSubscription, err error) {
	defer d.observe(poolWrite, &err)()

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
