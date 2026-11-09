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

func trunks(ctx context.Context, q querier) ([]settings.Trunk, error) {
	var (
		trunks []settings.Trunk
		index  = map[string]int{}
	)

	err := queryEach(ctx, q, "get SIP trunks", `SELECT id, name, domain, trusted, keepalive_interval_s, enabled,
		tls_required FROM sip_trunks ORDER BY id`, func(rows *sql.Rows) error {
		var (
			t         settings.Trunk
			keepalive int64
		)

		if err := rows.Scan(&t.ID, &t.Name, &t.Domain, &t.Trusted, &keepalive, &t.Enabled, &t.TLS.Required); err != nil {
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

	err = queryEach(ctx, q, "get SIP trunk addresses", `SELECT trunk_id, host, port, transport FROM sip_trunk_addresses
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

	err = queryEach(ctx, q, "get SIP trunk inbound CIDRs", `SELECT trunk_id, cidr FROM sip_trunk_inbound_cidrs
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

	if err := trunkTLS(ctx, q, trunks, index); err != nil {
		return nil, err
	}

	return trunks, nil
}

func trunkTLS(ctx context.Context, q querier, trunks []settings.Trunk, index map[string]int) error {
	err := queryEach(ctx, q, "get SIP trunk TLS CA certificates", `SELECT trunk_id, der FROM sip_trunk_tls_ca_certificates
		ORDER BY trunk_id, position`, func(rows *sql.Rows) error {
		var (
			id  string
			der []byte
		)

		if err := rows.Scan(&id, &der); err != nil {
			return err
		}

		i, ok := index[id]
		if !ok {
			return fmt.Errorf("a TLS CA certificate of no SIP trunk: %s", id)
		}

		trunks[i].TLS.CACertificates = append(trunks[i].TLS.CACertificates, der)

		return nil
	})
	if err != nil {
		return err
	}

	err = queryEach(ctx, q, "get SIP trunk TLS pins", `SELECT trunk_id, sha256 FROM sip_trunk_tls_pins
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
			return fmt.Errorf("a TLS pin of no SIP trunk: %s", id)
		}

		if len(b) != len(fp) {
			return fmt.Errorf("SIP trunk %s: a TLS pin of %d bytes", id, len(b))
		}

		copy(fp[:], b)
		trunks[i].TLS.Pins = append(trunks[i].TLS.Pins, fp)

		return nil
	})
	if err != nil {
		return err
	}

	return queryEach(ctx, q, "get SIP trunk TLS identities", `SELECT trunk_id, identity FROM sip_trunk_tls_identities
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

func sipRoutes(ctx context.Context, q querier) ([]settings.SIPRoute, error) {
	var (
		routes []settings.SIPRoute
		index  = map[string]int{}
	)

	err := queryEach(ctx, q, "get SIP routes", `SELECT id, prefix FROM sip_routes ORDER BY id`, func(rows *sql.Rows) error {
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

	err = queryEach(ctx, q, "get SIP route trunks", `SELECT route_id, trunk_id FROM sip_route_trunks
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

func queryEach(ctx context.Context, q querier, what, query string, scan func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, query)
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

	return submitSettings(ctx, d, cmdCreateTrunk, t, "create SIP trunk")
}

func applyCreateTrunk(a *applyCtx, t settings.Trunk) (struct{}, error) {
	if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO sip_trunks (id, name, domain, trusted, keepalive_interval_s,
		enabled, tls_required) VALUES (?, ?, ?, ?, ?, ?, ?)`, t.ID, t.Name, t.Domain, t.Trusted,
		int64(t.KeepaliveInterval/time.Second), t.Enabled, t.TLS.Required); err != nil {
		return struct{}{}, err
	}

	return settingsChanged(a, putTrunkLists(a.ctx, a.tx, t))
}

func (d *DB) UpdateTrunk(ctx context.Context, t settings.Trunk) (err error) {
	defer d.observe(poolWrite, &err)()

	return submitSettings(ctx, d, cmdUpdateTrunk, t, "update SIP trunk")
}

func applyUpdateTrunk(a *applyCtx, t settings.Trunk) (struct{}, error) {
	res, err := a.tx.ExecContext(a.ctx, `UPDATE sip_trunks SET name = ?, domain = ?, trusted = ?,
		keepalive_interval_s = ?, enabled = ?, tls_required = ? WHERE id = ?`, t.Name, t.Domain, t.Trusted,
		int64(t.KeepaliveInterval/time.Second), t.Enabled, t.TLS.Required, t.ID)
	if err != nil {
		return struct{}{}, err
	}

	if err := checkAffected(res); err != nil {
		return struct{}{}, err
	}

	for _, table := range trunkListTables {
		if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM `+table+` WHERE trunk_id = ?`, t.ID); err != nil {
			return struct{}{}, err
		}
	}

	return settingsChanged(a, putTrunkLists(a.ctx, a.tx, t))
}

var trunkListTables = []string{
	"sip_trunk_addresses", "sip_trunk_inbound_cidrs", "sip_trunk_tls_ca_certificates", "sip_trunk_tls_pins",
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

	for i, der := range t.TLS.CACertificates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sip_trunk_tls_ca_certificates (trunk_id, position, der) VALUES (?, ?, ?)`,
			t.ID, i, der); err != nil {
			return err
		}
	}

	for i, fp := range t.TLS.Pins {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sip_trunk_tls_pins (trunk_id, position, sha256) VALUES (?, ?, ?)`,
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

	return submitSettings(ctx, d, cmdDeleteTrunk, id, "delete SIP trunk")
}

func applyDeleteTrunk(a *applyCtx, id string) (struct{}, error) {
	return settingsChanged(a, affected(a.tx.ExecContext(a.ctx, `DELETE FROM sip_trunks WHERE id = ?`, id)))
}

func (d *DB) CreateSIPRoute(ctx context.Context, r settings.SIPRoute) (err error) {
	defer d.observe(poolWrite, &err)()

	return submitSettings(ctx, d, cmdCreateSIPRoute, r, "create SIP route")
}

func applyCreateSIPRoute(a *applyCtx, r settings.SIPRoute) (struct{}, error) {
	if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO sip_routes (id, prefix) VALUES (?, ?)`, r.ID,
		r.Prefix); err != nil {
		return struct{}{}, err
	}

	return settingsChanged(a, putRouteTrunks(a.ctx, a.tx, r))
}

func (d *DB) UpdateSIPRoute(ctx context.Context, r settings.SIPRoute) (err error) {
	defer d.observe(poolWrite, &err)()

	return submitSettings(ctx, d, cmdUpdateSIPRoute, r, "update SIP route")
}

func applyUpdateSIPRoute(a *applyCtx, r settings.SIPRoute) (struct{}, error) {
	res, err := a.tx.ExecContext(a.ctx, `UPDATE sip_routes SET prefix = ? WHERE id = ?`, r.Prefix, r.ID)
	if err != nil {
		return struct{}{}, err
	}

	if err := checkAffected(res); err != nil {
		return struct{}{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM sip_route_trunks WHERE route_id = ?`, r.ID); err != nil {
		return struct{}{}, err
	}

	return settingsChanged(a, putRouteTrunks(a.ctx, a.tx, r))
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

	return submitSettings(ctx, d, cmdDeleteSIPRoute, id, "delete SIP route")
}

func applyDeleteSIPRoute(a *applyCtx, id string) (struct{}, error) {
	return settingsChanged(a, affected(a.tx.ExecContext(a.ctx, `DELETE FROM sip_routes WHERE id = ?`, id)))
}

func init() {
	register(cmdCreateTrunk, handle(applyCreateTrunk))
	register(cmdUpdateTrunk, handle(applyUpdateTrunk))
	register(cmdDeleteTrunk, handle(applyDeleteTrunk))
	register(cmdCreateSIPRoute, handle(applyCreateSIPRoute))
	register(cmdUpdateSIPRoute, handle(applyUpdateSIPRoute))
	register(cmdDeleteSIPRoute, handle(applyDeleteSIPRoute))
}
