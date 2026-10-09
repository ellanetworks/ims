package db

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/ellanetworks/ims/internal/settings"
)

func (d *DB) GetSettings(ctx context.Context) (_ settings.Settings, err error) {
	defer d.observe(poolWrite, &err)()

	var s settings.Settings

	o := &s.Operator

	if err := d.conn.QueryRowContext(ctx, `SELECT mcc, mnc, country_code, national_prefix, international_prefix
		FROM operator WHERE id = 1`).Scan(&o.MCC, &o.MNC, &o.Numbering.CountryCode, &o.Numbering.NationalPrefix,
		&o.Numbering.InternationalPrefix); err != nil {
		return settings.Settings{}, fmt.Errorf("get operator: %w", err)
	}

	if s.Peers, err = d.peers(ctx); err != nil {
		return settings.Settings{}, err
	}

	if s.Routes, err = d.routes(ctx); err != nil {
		return settings.Settings{}, err
	}

	var iface string

	if err := d.conn.QueryRowContext(ctx, `SELECT interface, pcf_uri FROM policy WHERE id = 1`).Scan(&iface,
		&s.Policy.PCFURI); err != nil {
		return settings.Settings{}, fmt.Errorf("get policy: %w", err)
	}

	s.Policy.Interface = settings.PolicyInterface(iface)

	if err := d.conn.QueryRowContext(ctx, `SELECT retention_days FROM call_record_settings WHERE id = 1`).Scan(
		&s.CallRecords.RetentionDays); err != nil {
		return settings.Settings{}, fmt.Errorf("get call record settings: %w", err)
	}

	return s, nil
}

// peers are in the order they were created in, which their UUIDv7 IDs sort in.
func (d *DB) peers(ctx context.Context) ([]settings.Peer, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT id, host, address, port, transport, applications, priority
		FROM diameter_peers ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("get Diameter peers: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var peers []settings.Peer

	for rows.Next() {
		var (
			p                  settings.Peer
			address, transport string
			applications       string
		)

		if err := rows.Scan(&p.ID, &p.Host, &address, &p.Port, &transport, &applications, &p.Priority); err != nil {
			return nil, fmt.Errorf("get Diameter peers: %w", err)
		}

		if p.Address, err = netip.ParseAddr(address); err != nil {
			return nil, fmt.Errorf("get Diameter peer %s: %w", p.ID, err)
		}

		p.Transport = settings.Transport(transport)

		for a := range strings.SplitSeq(applications, ",") {
			p.Applications = append(p.Applications, settings.Application(a))
		}

		peers = append(peers, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get Diameter peers: %w", err)
	}

	return peers, nil
}

// routes are in the order of settings.Applications.
func (d *DB) routes(ctx context.Context) ([]settings.Route, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT application, realm FROM diameter_routes`)
	if err != nil {
		return nil, fmt.Errorf("get Diameter routes: %w", err)
	}

	defer func() { _ = rows.Close() }()

	realms := make(map[settings.Application]string)

	for rows.Next() {
		var app, realm string

		if err := rows.Scan(&app, &realm); err != nil {
			return nil, fmt.Errorf("get Diameter routes: %w", err)
		}

		realms[settings.Application(app)] = realm
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get Diameter routes: %w", err)
	}

	routes := make([]settings.Route, 0, len(settings.Applications))

	for _, app := range settings.Applications {
		realm, ok := realms[app]
		if !ok {
			return nil, fmt.Errorf("get Diameter routes: no route for %s", app)
		}

		routes = append(routes, settings.Route{Application: app, Realm: realm})
	}

	return routes, nil
}

func (d *DB) UpdateOperator(ctx context.Context, o settings.Operator) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `UPDATE operator SET mcc = ?, mnc = ?, country_code = ?, national_prefix = ?,
		international_prefix = ? WHERE id = 1`, o.MCC, o.MNC, o.Numbering.CountryCode, o.Numbering.NationalPrefix,
		o.Numbering.InternationalPrefix); err != nil {
		return fmt.Errorf("update operator: %w", err)
	}

	return nil
}

func (d *DB) CreatePeer(ctx context.Context, p settings.Peer) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `INSERT INTO diameter_peers (id, host, address, port, transport,
		applications, priority) VALUES (?, ?, ?, ?, ?, ?, ?)`, peerRow(p)...); err != nil {
		return fmt.Errorf("create Diameter peer: %w", err)
	}

	return nil
}

func (d *DB) UpdatePeer(ctx context.Context, p settings.Peer) (err error) {
	defer d.observe(poolWrite, &err)()

	row := peerRow(p)

	res, err := d.conn.ExecContext(ctx, `UPDATE diameter_peers SET host = ?, address = ?, port = ?, transport = ?,
		applications = ?, priority = ? WHERE id = ?`, append(row[1:], row[0])...)
	if err != nil {
		return fmt.Errorf("update Diameter peer: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("update Diameter peer: %w", err)
	}

	return nil
}

func (d *DB) DeletePeer(ctx context.Context, id string) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM diameter_peers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete Diameter peer: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete Diameter peer: %w", err)
	}

	return nil
}

func (d *DB) UpdateRoute(ctx context.Context, r settings.Route) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `UPDATE diameter_routes SET realm = ? WHERE application = ?`, r.Realm,
		string(r.Application))
	if err != nil {
		return fmt.Errorf("update Diameter route: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("update Diameter route: %w", err)
	}

	return nil
}

func (d *DB) UpdatePolicy(ctx context.Context, p settings.Policy) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `UPDATE policy SET interface = ?, pcf_uri = ? WHERE id = 1`, string(p.Interface),
		p.PCFURI); err != nil {
		return fmt.Errorf("update policy: %w", err)
	}

	return nil
}

func (d *DB) UpdateCallRecords(ctx context.Context, c settings.CallRecords) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `UPDATE call_record_settings SET retention_days = ? WHERE id = 1`,
		c.RetentionDays); err != nil {
		return fmt.Errorf("update call record settings: %w", err)
	}

	return nil
}

func peerRow(p settings.Peer) []any {
	apps := make([]string, len(p.Applications))
	for i, a := range p.Applications {
		apps[i] = string(a)
	}

	return []any{p.ID, p.Host, p.Address.String(), p.Port, string(p.Transport), strings.Join(apps, ","), p.Priority}
}
