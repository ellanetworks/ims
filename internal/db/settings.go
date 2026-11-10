package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/ellanetworks/ims/internal/settings"
)

func (d *DB) GetSettings(ctx context.Context) (_ settings.Settings, err error) {
	defer d.observe(poolRead, &err)()

	tx, err := d.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return settings.Settings{}, fmt.Errorf("get settings: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	return getSettings(ctx, tx)
}

func getSettings(ctx context.Context, q querier) (_ settings.Settings, err error) {
	var s settings.Settings

	o := &s.Operator

	if err := q.QueryRowContext(ctx, `SELECT mcc, mnc, domain, country_code, national_prefix, international_prefix,
		min_national_digits FROM operator WHERE id = 1`).Scan(&o.MCC, &o.MNC, &o.Domain, &o.Numbering.CountryCode,
		&o.Numbering.NationalPrefix, &o.Numbering.InternationalPrefix, &o.Numbering.MinNationalDigits); err != nil {
		return settings.Settings{}, fmt.Errorf("get operator: %w", err)
	}

	if s.Peers, err = peers(ctx, q); err != nil {
		return settings.Settings{}, err
	}

	if s.Routes, err = routes(ctx, q); err != nil {
		return settings.Settings{}, err
	}

	if s.SMSRoutes, err = smsRoutes(ctx, q); err != nil {
		return settings.Settings{}, err
	}

	if s.Trunks, err = trunks(ctx, q); err != nil {
		return settings.Settings{}, err
	}

	if s.SIPRoutes, err = sipRoutes(ctx, q); err != nil {
		return settings.Settings{}, err
	}

	var iface string

	if err := q.QueryRowContext(ctx, `SELECT interface, pcf_uri FROM policy WHERE id = 1`).Scan(&iface,
		&s.Policy.PCFURI); err != nil {
		return settings.Settings{}, fmt.Errorf("get policy: %w", err)
	}

	s.Policy.Interface = settings.PolicyInterface(iface)

	if err := q.QueryRowContext(ctx, `SELECT retention_days FROM call_record_settings WHERE id = 1`).Scan(
		&s.CallRecords.RetentionDays); err != nil {
		return settings.Settings{}, fmt.Errorf("get call record settings: %w", err)
	}

	if err := q.QueryRowContext(ctx, `SELECT number FROM ip_sm_gw WHERE id = 1`).Scan(&s.IPSMGW.Number); err != nil {
		return settings.Settings{}, fmt.Errorf("get IP-SM-GW settings: %w", err)
	}

	return s, nil
}

func peers(ctx context.Context, q querier) ([]settings.Peer, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, host, address, port, transport, applications, priority
		FROM diameter_peers ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("get Diameter peers: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var peers []settings.Peer

	for rows.Next() {
		var (
			p            settings.Peer
			transport    string
			applications string
		)

		if err := rows.Scan(&p.ID, &p.Host, &p.Address, &p.Port, &transport, &applications, &p.Priority); err != nil {
			return nil, fmt.Errorf("get Diameter peers: %w", err)
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

func routes(ctx context.Context, q querier) ([]settings.Route, error) {
	rows, err := q.QueryContext(ctx, `SELECT application, realm FROM diameter_routes`)
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

	routes := make([]settings.Route, 0, len(settings.RoutedApplications))

	for _, app := range settings.RoutedApplications {
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

	return submitSettings(ctx, d, cmdUpdateOperator, o, "update operator")
}

func applyUpdateOperator(a *applyCtx, o settings.Operator) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `UPDATE operator SET mcc = ?, mnc = ?, domain = ?, country_code = ?,
		national_prefix = ?, international_prefix = ?, min_national_digits = ? WHERE id = 1`, o.MCC, o.MNC, o.Domain,
		o.Numbering.CountryCode, o.Numbering.NationalPrefix, o.Numbering.InternationalPrefix,
		o.Numbering.MinNationalDigits)

	return settingsChanged(a, err)
}

func (d *DB) CreatePeer(ctx context.Context, p settings.Peer) (err error) {
	defer d.observe(poolWrite, &err)()

	return submitSettings(ctx, d, cmdCreatePeer, p, "create Diameter peer")
}

func applyCreatePeer(a *applyCtx, p settings.Peer) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `INSERT INTO diameter_peers (id, host, address, port, transport,
		applications, priority) VALUES (?, ?, ?, ?, ?, ?, ?)`, peerRow(p)...)

	return settingsChanged(a, err)
}

func (d *DB) UpdatePeer(ctx context.Context, p settings.Peer) (err error) {
	defer d.observe(poolWrite, &err)()

	return submitSettings(ctx, d, cmdUpdatePeer, p, "update Diameter peer")
}

func applyUpdatePeer(a *applyCtx, p settings.Peer) (struct{}, error) {
	row := peerRow(p)

	return settingsChanged(a, affected(a.tx.ExecContext(a.ctx, `UPDATE diameter_peers SET host = ?, address = ?, port = ?,
		transport = ?, applications = ?, priority = ? WHERE id = ?`, append(row[1:], row[0])...)))
}

func (d *DB) DeletePeer(ctx context.Context, id string) (err error) {
	defer d.observe(poolWrite, &err)()

	return submitSettings(ctx, d, cmdDeletePeer, id, "delete Diameter peer")
}

func applyDeletePeer(a *applyCtx, id string) (struct{}, error) {
	return settingsChanged(a, affected(a.tx.ExecContext(a.ctx, `DELETE FROM diameter_peers WHERE id = ?`, id)))
}

func (d *DB) UpdateRoute(ctx context.Context, r settings.Route) (err error) {
	defer d.observe(poolWrite, &err)()

	return submitSettings(ctx, d, cmdUpdateRoute, r, "update Diameter route")
}

func applyUpdateRoute(a *applyCtx, r settings.Route) (struct{}, error) {
	return settingsChanged(a, affected(a.tx.ExecContext(a.ctx, `UPDATE diameter_routes SET realm = ? WHERE application = ?`,
		r.Realm, string(r.Application))))
}

func (d *DB) UpdatePolicy(ctx context.Context, p settings.Policy) (err error) {
	defer d.observe(poolWrite, &err)()

	return submitSettings(ctx, d, cmdUpdatePolicy, p, "update policy")
}

func applyUpdatePolicy(a *applyCtx, p settings.Policy) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `UPDATE policy SET interface = ?, pcf_uri = ? WHERE id = 1`,
		string(p.Interface), p.PCFURI)

	return settingsChanged(a, err)
}

func (d *DB) UpdateCallRecords(ctx context.Context, c settings.CallRecords) (err error) {
	defer d.observe(poolWrite, &err)()

	return submitSettings(ctx, d, cmdUpdateCallRecords, c, "update call record settings")
}

func applyUpdateCallRecords(a *applyCtx, c settings.CallRecords) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `UPDATE call_record_settings SET retention_days = ? WHERE id = 1`,
		c.RetentionDays)

	return settingsChanged(a, err)
}

func peerRow(p settings.Peer) []any {
	apps := make([]string, len(p.Applications))
	for i, a := range p.Applications {
		apps[i] = string(a)
	}

	return []any{
		p.ID, p.Host, p.Address, p.Port, string(p.Transport), strings.Join(apps, ","), p.Priority,
	}
}

func (d *DB) UpdateIPSMGW(ctx context.Context, g settings.IPSMGW) (err error) {
	defer d.observe(poolWrite, &err)()

	return submitSettings(ctx, d, cmdUpdateIPSMGW, g, "update IP-SM-GW settings")
}

func applyUpdateIPSMGW(a *applyCtx, g settings.IPSMGW) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `UPDATE ip_sm_gw SET number = ? WHERE id = 1`, g.Number)

	return settingsChanged(a, err)
}

func submitSettings(ctx context.Context, d *DB, t CommandType, payload any, what string) error {
	if _, err := submit[struct{}](ctx, d, t, payload); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	return nil
}

func settingsChanged(a *applyCtx, err error) (struct{}, error) {
	if err != nil {
		return struct{}{}, err
	}

	if err := validateSettings(a); err != nil {
		return struct{}{}, err
	}

	a.events.Settings = true

	return struct{}{}, nil
}

func validateSettings(a *applyCtx) error {
	s, err := getSettings(a.ctx, a.tx)
	if err != nil {
		return err
	}

	if err := s.Validate(); err != nil {
		return knownSettingsError(err)
	}

	labels, err := memberLabels(a.ctx, a.tx)
	if err != nil {
		return err
	}

	for _, label := range labels {
		if err := s.Operator.HostConflict(s.Peers, label); err != nil {
			return knownSettingsError(err)
		}
	}

	return nil
}

func knownSettingsError(err error) error {
	if errors.Is(err, settings.ErrInvalid) || errors.Is(err, settings.ErrConflict) {
		return err
	}

	return fmt.Errorf("%w: %w", settings.ErrInvalid, err)
}

func memberLabels(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT diameter_label FROM cluster_members ORDER BY node_id`)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var labels []string

	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}

		labels = append(labels, label)
	}

	return labels, rows.Err()
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}

	return checkAffected(res)
}

func init() {
	register(cmdUpdateOperator, handle(applyUpdateOperator))
	register(cmdCreatePeer, handle(applyCreatePeer))
	register(cmdUpdatePeer, handle(applyUpdatePeer))
	register(cmdDeletePeer, handle(applyDeletePeer))
	register(cmdUpdateRoute, handle(applyUpdateRoute))
	register(cmdUpdatePolicy, handle(applyUpdatePolicy))
	register(cmdUpdateCallRecords, handle(applyUpdateCallRecords))
	register(cmdUpdateIPSMGW, handle(applyUpdateIPSMGW))
}
