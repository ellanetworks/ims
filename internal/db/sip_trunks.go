package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/netip"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
)

func (d *DB) trunks(ctx context.Context) ([]settings.Trunk, error) {
	var (
		trunks []settings.Trunk
		index  = map[string]int{}
	)

	err := d.queryEach(ctx, "get SIP trunks", `SELECT id, name, domain, trusted, keepalive_interval_s, enabled,
		tls_required, tls_require_client_certificate FROM sip_trunks ORDER BY id`, func(rows *sql.Rows) error {
		var (
			t         settings.Trunk
			keepalive int64
		)

		if err := rows.Scan(&t.ID, &t.Name, &t.Domain, &t.Trusted, &keepalive, &t.Enabled, &t.TLS.Required,
			&t.TLS.RequireClientCertificate); err != nil {
			return err
		}

		t.KeepaliveInterval = time.Duration(keepalive) * time.Second
		index[t.ID] = len(trunks)
		trunks = append(trunks, t)

		return nil
	})
	if err != nil {
		return nil, err
	}

	err = d.queryEach(ctx, "get SIP trunk addresses", `SELECT trunk_id, host, port, transport FROM sip_trunk_addresses
		ORDER BY trunk_id, position`, func(rows *sql.Rows) error {
		var (
			id        string
			a         settings.TrunkAddress
			port      sql.NullInt64
			transport sql.NullString
		)

		if err := rows.Scan(&id, &a.Host, &port, &transport); err != nil {
			return err
		}

		i, ok := index[id]
		if !ok {
			return fmt.Errorf("an address of no SIP trunk: %s", id)
		}

		a.Port = int(port.Int64)
		a.Transport = settings.SIPTransport(transport.String)
		trunks[i].Addresses = append(trunks[i].Addresses, a)

		return nil
	})
	if err != nil {
		return nil, err
	}

	err = d.queryEach(ctx, "get SIP trunk inbound CIDRs", `SELECT trunk_id, cidr FROM sip_trunk_inbound_cidrs
		ORDER BY trunk_id, position`, func(rows *sql.Rows) error {
		var id, cidr string

		if err := rows.Scan(&id, &cidr); err != nil {
			return err
		}

		i, ok := index[id]
		if !ok {
			return fmt.Errorf("an inbound CIDR of no SIP trunk: %s", id)
		}

		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			return fmt.Errorf("SIP trunk %s: %w", id, err)
		}

		trunks[i].InboundCIDRs = append(trunks[i].InboundCIDRs, p)

		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := d.trunkTLS(ctx, trunks, index); err != nil {
		return nil, err
	}

	return trunks, nil
}

func (d *DB) trunkTLS(ctx context.Context, trunks []settings.Trunk, index map[string]int) error {
	err := d.queryEach(ctx, "get SIP trunk TLS certificates", `SELECT trunk_id, kind, der FROM sip_trunk_tls_certificates
		ORDER BY trunk_id, kind, position`, func(rows *sql.Rows) error {
		var (
			id, kind string
			der      []byte
		)

		if err := rows.Scan(&id, &kind, &der); err != nil {
			return err
		}

		i, ok := index[id]
		if !ok {
			return fmt.Errorf("a TLS certificate of no SIP trunk: %s", id)
		}

		switch kind {
		case "ca":
			trunks[i].TLS.CACertificates = append(trunks[i].TLS.CACertificates, der)
		case "pinned":
			trunks[i].TLS.PinnedCertificates = append(trunks[i].TLS.PinnedCertificates, der)
		default:
			return fmt.Errorf("SIP trunk %s: a TLS certificate of kind %q", id, kind)
		}

		return nil
	})
	if err != nil {
		return err
	}

	err = d.queryEach(ctx, "get SIP trunk TLS fingerprints", `SELECT trunk_id, sha256 FROM sip_trunk_tls_fingerprints
		ORDER BY trunk_id, position`, func(rows *sql.Rows) error {
		var (
			id string
			b  []byte
			fp [sha256.Size]byte
		)

		if err := rows.Scan(&id, &b); err != nil {
			return err
		}

		i, ok := index[id]
		if !ok {
			return fmt.Errorf("a TLS fingerprint of no SIP trunk: %s", id)
		}

		if len(b) != len(fp) {
			return fmt.Errorf("SIP trunk %s: a TLS fingerprint of %d bytes", id, len(b))
		}

		copy(fp[:], b)
		trunks[i].TLS.PinnedFingerprints = append(trunks[i].TLS.PinnedFingerprints, fp)

		return nil
	})
	if err != nil {
		return err
	}

	return d.queryEach(ctx, "get SIP trunk TLS identities", `SELECT trunk_id, identity FROM sip_trunk_tls_identities
		ORDER BY trunk_id, position`, func(rows *sql.Rows) error {
		var id, identity string

		if err := rows.Scan(&id, &identity); err != nil {
			return err
		}

		i, ok := index[id]
		if !ok {
			return fmt.Errorf("a TLS identity of no SIP trunk: %s", id)
		}

		trunks[i].TLS.Identities = append(trunks[i].TLS.Identities, identity)

		return nil
	})
}

func (d *DB) sipRoutes(ctx context.Context) ([]settings.SIPRoute, error) {
	var (
		routes []settings.SIPRoute
		index  = map[string]int{}
	)

	err := d.queryEach(ctx, "get SIP routes", `SELECT id, prefix FROM sip_routes ORDER BY id`, func(rows *sql.Rows) error {
		var r settings.SIPRoute

		if err := rows.Scan(&r.ID, &r.Prefix); err != nil {
			return err
		}

		index[r.ID] = len(routes)
		routes = append(routes, r)

		return nil
	})
	if err != nil {
		return nil, err
	}

	err = d.queryEach(ctx, "get SIP route trunks", `SELECT route_id, trunk_id FROM sip_route_trunks
		ORDER BY route_id, position`, func(rows *sql.Rows) error {
		var routeID, trunkID string

		if err := rows.Scan(&routeID, &trunkID); err != nil {
			return err
		}

		i, ok := index[routeID]
		if !ok {
			return fmt.Errorf("a trunk of no SIP route: %s", routeID)
		}

		routes[i].Trunks = append(routes[i].Trunks, trunkID)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return routes, nil
}

func (d *DB) queryEach(ctx context.Context, what, query string, scan func(*sql.Rows) error) error {
	rows, err := d.conn.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		if err := scan(rows); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	return nil
}

func (d *DB) CreateTrunk(ctx context.Context, t settings.Trunk) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "create SIP trunk", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sip_trunks (id, name, domain, trusted, keepalive_interval_s, enabled,
			tls_required, tls_require_client_certificate) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, t.ID, t.Name, t.Domain, t.Trusted,
			int64(t.KeepaliveInterval/time.Second), t.Enabled, t.TLS.Required, t.TLS.RequireClientCertificate); err != nil {
			return err
		}

		return putTrunkLists(ctx, tx, t)
	})
}

func (d *DB) UpdateTrunk(ctx context.Context, t settings.Trunk) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "update SIP trunk", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sip_trunks SET name = ?, domain = ?, trusted = ?, keepalive_interval_s = ?,
			enabled = ?, tls_required = ?, tls_require_client_certificate = ? WHERE id = ?`, t.Name, t.Domain, t.Trusted,
			int64(t.KeepaliveInterval/time.Second), t.Enabled, t.TLS.Required, t.TLS.RequireClientCertificate, t.ID)
		if err != nil {
			return err
		}

		if err := checkAffected(res); err != nil {
			return err
		}

		for _, table := range trunkListTables {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE trunk_id = ?`, t.ID); err != nil {
				return err
			}
		}

		return putTrunkLists(ctx, tx, t)
	})
}

var trunkListTables = []string{
	"sip_trunk_addresses", "sip_trunk_inbound_cidrs", "sip_trunk_tls_certificates", "sip_trunk_tls_fingerprints",
	"sip_trunk_tls_identities",
}

func putTrunkLists(ctx context.Context, tx *sql.Tx, t settings.Trunk) error {
	for i, a := range t.Addresses {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sip_trunk_addresses (trunk_id, position, host, port, transport)
			VALUES (?, ?, ?, ?, ?)`, t.ID, i, a.Host, nullableInt(int64(a.Port)), nullableString(string(a.Transport))); err != nil {
			return err
		}
	}

	for i, p := range t.InboundCIDRs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sip_trunk_inbound_cidrs (trunk_id, position, cidr) VALUES (?, ?, ?)`,
			t.ID, i, p.String()); err != nil {
			return err
		}
	}

	for kind, ders := range map[string][][]byte{"ca": t.TLS.CACertificates, "pinned": t.TLS.PinnedCertificates} {
		for i, der := range ders {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sip_trunk_tls_certificates (trunk_id, kind, position, der)
				VALUES (?, ?, ?, ?)`, t.ID, kind, i, der); err != nil {
				return err
			}
		}
	}

	for i, fp := range t.TLS.PinnedFingerprints {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sip_trunk_tls_fingerprints (trunk_id, position, sha256) VALUES (?, ?, ?)`,
			t.ID, i, fp[:]); err != nil {
			return err
		}
	}

	for i, id := range t.TLS.Identities {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sip_trunk_tls_identities (trunk_id, position, identity) VALUES (?, ?, ?)`,
			t.ID, i, id); err != nil {
			return err
		}
	}

	return nil
}

func (d *DB) DeleteTrunk(ctx context.Context, id string) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM sip_trunks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete SIP trunk: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete SIP trunk: %w", err)
	}

	return nil
}

func (d *DB) CreateSIPRoute(ctx context.Context, r settings.SIPRoute) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "create SIP route", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sip_routes (id, prefix) VALUES (?, ?)`, r.ID, r.Prefix); err != nil {
			return err
		}

		return putRouteTrunks(ctx, tx, r)
	})
}

func (d *DB) UpdateSIPRoute(ctx context.Context, r settings.SIPRoute) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "update SIP route", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sip_routes SET prefix = ? WHERE id = ?`, r.Prefix, r.ID)
		if err != nil {
			return err
		}

		if err := checkAffected(res); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM sip_route_trunks WHERE route_id = ?`, r.ID); err != nil {
			return err
		}

		return putRouteTrunks(ctx, tx, r)
	})
}

func putRouteTrunks(ctx context.Context, tx *sql.Tx, r settings.SIPRoute) error {
	for i, id := range r.Trunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sip_route_trunks (route_id, position, trunk_id) VALUES (?, ?, ?)`,
			r.ID, i, id); err != nil {
			return err
		}
	}

	return nil
}

func (d *DB) DeleteSIPRoute(ctx context.Context, id string) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM sip_routes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete SIP route: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete SIP route: %w", err)
	}

	return nil
}
