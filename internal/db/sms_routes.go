package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ellanetworks/ims/internal/settings"
)

func smsRoutes(ctx context.Context, q querier) ([]settings.SMSRoute, error) {
	var (
		routes []settings.SMSRoute
		index  = map[string]int{}
	)

	err := queryEach(ctx, q, "get SMS routes", `SELECT id, service_centre FROM sms_routes ORDER BY id`,
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

	err = queryEach(ctx, q, "get SMS route peers", `SELECT route_id, peer_id FROM sms_route_peers
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
	ctx, done := d.observe(ctx, "CreateSMSRoute", poolWrite)
	defer done(&err)

	return submitSettings(ctx, d, cmdCreateSMSRoute, r, "create SMS route")
}

func applyCreateSMSRoute(a *applyCtx, r settings.SMSRoute) (struct{}, error) {
	if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO sms_routes (id, service_centre) VALUES (?, ?)`, r.ID,
		r.ServiceCentre); err != nil {
		return struct{}{}, err
	}

	return settingsChanged(a, putSMSRoutePeers(a.ctx, a.tx, r))
}

func (d *DB) UpdateSMSRoute(ctx context.Context, r settings.SMSRoute) (err error) {
	ctx, done := d.observe(ctx, "UpdateSMSRoute", poolWrite)
	defer done(&err)

	return submitSettings(ctx, d, cmdUpdateSMSRoute, r, "update SMS route")
}

func applyUpdateSMSRoute(a *applyCtx, r settings.SMSRoute) (struct{}, error) {
	res, err := a.tx.ExecContext(a.ctx, `UPDATE sms_routes SET service_centre = ? WHERE id = ?`, r.ServiceCentre, r.ID)
	if err != nil {
		return struct{}{}, err
	}

	if err := checkAffected(res); err != nil {
		return struct{}{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM sms_route_peers WHERE route_id = ?`, r.ID); err != nil {
		return struct{}{}, err
	}

	return settingsChanged(a, putSMSRoutePeers(a.ctx, a.tx, r))
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
	ctx, done := d.observe(ctx, "DeleteSMSRoute", poolWrite)
	defer done(&err)

	return submitSettings(ctx, d, cmdDeleteSMSRoute, id, "delete SMS route")
}

func applyDeleteSMSRoute(a *applyCtx, id string) (struct{}, error) {
	return settingsChanged(a, affected(a.tx.ExecContext(a.ctx, `DELETE FROM sms_routes WHERE id = ?`, id)))
}

func init() {
	register(cmdCreateSMSRoute, handle(applyCreateSMSRoute))
	register(cmdUpdateSMSRoute, handle(applyUpdateSMSRoute))
	register(cmdDeleteSMSRoute, handle(applyDeleteSMSRoute))
}
