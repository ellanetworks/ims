package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ellanetworks/ims/internal/settings"
)

func (d *DB) smsRoutes(ctx context.Context) ([]settings.SMSRoute, error) {
	var (
		routes []settings.SMSRoute
		index  = map[string]int{}
	)

	err := d.queryEach(ctx, "get SMS routes", `SELECT id, service_centre FROM sms_routes ORDER BY id`,
		func(rows *sql.Rows) error {
			var r settings.SMSRoute

			if err := rows.Scan(&r.ID, &r.ServiceCentre); err != nil {
				return err
			}

			index[r.ID] = len(routes)
			routes = append(routes, r)

			return nil
		})
	if err != nil {
		return nil, err
	}

	err = d.queryEach(ctx, "get SMS route peers", `SELECT route_id, peer_id FROM sms_route_peers
		ORDER BY route_id, peer_id`, func(rows *sql.Rows) error {
		var routeID, peerID string

		if err := rows.Scan(&routeID, &peerID); err != nil {
			return err
		}

		i, ok := index[routeID]
		if !ok {
			return fmt.Errorf("a peer of no SMS route: %s", routeID)
		}

		routes[i].Peers = append(routes[i].Peers, peerID)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return routes, nil
}

func (d *DB) CreateSMSRoute(ctx context.Context, r settings.SMSRoute) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "create SMS route", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sms_routes (id, service_centre) VALUES (?, ?)`, r.ID,
			r.ServiceCentre); err != nil {
			return err
		}

		return putSMSRoutePeers(ctx, tx, r)
	})
}

func (d *DB) UpdateSMSRoute(ctx context.Context, r settings.SMSRoute) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "update SMS route", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sms_routes SET service_centre = ? WHERE id = ?`, r.ServiceCentre, r.ID)
		if err != nil {
			return err
		}

		if err := checkAffected(res); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM sms_route_peers WHERE route_id = ?`, r.ID); err != nil {
			return err
		}

		return putSMSRoutePeers(ctx, tx, r)
	})
}

func putSMSRoutePeers(ctx context.Context, tx *sql.Tx, r settings.SMSRoute) error {
	for _, id := range r.Peers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sms_route_peers (route_id, peer_id) VALUES (?, ?)`, r.ID,
			id); err != nil {
			return err
		}
	}

	return nil
}

func (d *DB) DeleteSMSRoute(ctx context.Context, id string) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM sms_routes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete SMS route: %w", err)
	}

	if err := checkAffected(res); err != nil {
		return fmt.Errorf("delete SMS route: %w", err)
	}

	return nil
}
