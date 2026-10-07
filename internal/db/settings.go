package db

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/ellanetworks/ims/internal/settings"
)

func (d *DB) GetSettings(ctx context.Context) (settings.Settings, error) {
	var s settings.Settings

	o := &s.Operator

	if err := d.conn.QueryRowContext(ctx, `SELECT mcc, mnc, country_code, national_prefix, international_prefix
		FROM operator WHERE id = 1`).Scan(&o.MCC, &o.MNC, &o.Numbering.CountryCode, &o.Numbering.NationalPrefix,
		&o.Numbering.InternationalPrefix); err != nil {
		return settings.Settings{}, fmt.Errorf("get operator: %w", err)
	}

	var err error

	if s.Peers, err = d.peers(ctx); err != nil {
		return settings.Settings{}, err
	}

	var iface string

	if err := d.conn.QueryRowContext(ctx, `SELECT interface, pcf_uri FROM policy WHERE id = 1`).Scan(&iface,
		&s.Policy.PCFURI); err != nil {
		return settings.Settings{}, fmt.Errorf("get policy: %w", err)
	}

	s.Policy.Interface = settings.PolicyInterface(iface)

	return s, nil
}

// peers are in the order they were created in, which their UUIDv7 IDs sort in.
func (d *DB) peers(ctx context.Context) ([]settings.Peer, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT id, host, realm, address, port, transport, applications
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

		if err := rows.Scan(&p.ID, &p.Host, &p.Realm, &address, &p.Port, &transport, &applications); err != nil {
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

func (d *DB) UpdateOperator(ctx context.Context, o settings.Operator) error {
	if _, err := d.conn.ExecContext(ctx, `UPDATE operator SET mcc = ?, mnc = ?, country_code = ?, national_prefix = ?,
		international_prefix = ? WHERE id = 1`, o.MCC, o.MNC, o.Numbering.CountryCode, o.Numbering.NationalPrefix,
		o.Numbering.InternationalPrefix); err != nil {
		return fmt.Errorf("update operator: %w", err)
	}

	return nil
}

func (d *DB) CreatePeer(ctx context.Context, p settings.Peer) error {
	if _, err := d.conn.ExecContext(ctx, `INSERT INTO diameter_peers (id, host, realm, address, port, transport,
		applications) VALUES (?, ?, ?, ?, ?, ?, ?)`, peerRow(p)...); err != nil {
		return fmt.Errorf("create Diameter peer: %w", err)
	}

	return nil
}

func (d *DB) UpdatePeer(ctx context.Context, p settings.Peer) error {
	row := peerRow(p)

	res, err := d.conn.ExecContext(ctx, `UPDATE diameter_peers SET host = ?, realm = ?, address = ?, port = ?,
		transport = ?, applications = ? WHERE id = ?`, append(row[1:], row[0])...)
	if err != nil {
		return fmt.Errorf("update Diameter peer: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("update Diameter peer: %w", err)
	}

	return nil
}

func (d *DB) DeletePeer(ctx context.Context, id string) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM diameter_peers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete Diameter peer: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete Diameter peer: %w", err)
	}

	return nil
}

func (d *DB) UpdatePolicy(ctx context.Context, p settings.Policy) error {
	if _, err := d.conn.ExecContext(ctx, `UPDATE policy SET interface = ?, pcf_uri = ? WHERE id = 1`, string(p.Interface),
		p.PCFURI); err != nil {
		return fmt.Errorf("update policy: %w", err)
	}

	return nil
}

func peerRow(p settings.Peer) []any {
	apps := make([]string, len(p.Applications))
	for i, a := range p.Applications {
		apps[i] = string(a)
	}

	return []any{p.ID, p.Host, p.Realm, p.Address.String(), p.Port, string(p.Transport), strings.Join(apps, ",")}
}
