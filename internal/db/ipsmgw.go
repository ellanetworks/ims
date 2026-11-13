package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TS 24.341 clause 5.3.3.2
type IPSMGWRegistration struct {
	IMPUKey   string
	IMPU      string
	IMPUs     []string
	MSISDN    string
	IMSI      string
	SCSCF     string
	AS        string
	ExpiresAt time.Time
	Available bool
}

// TS 29.328 clause 6.1.2.1
// TS 29.328 clause 6.5
type IPSMGWHSSRegistration struct {
	UserKey string
	MSISDN  string
	IMPU    string
	Number  string
	HSS     HSS
}

func (d *DB) IPSMGWRegistrations(ctx context.Context) (_ []IPSMGWRegistration, err error) {
	ctx, done := d.observe(ctx, "IPSMGWRegistrations", poolLocal)
	defer done(&err)

	rows, err := d.local.QueryContext(ctx, `SELECT impu_key, impu, impus, msisdn, imsi, scscf, as_uri, expires_at,
		available FROM ip_sm_gw_registrations ORDER BY impu_key`)
	if err != nil {
		return nil, fmt.Errorf("get IP-SM-GW registrations: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []IPSMGWRegistration

	for rows.Next() {
		var (
			r       IPSMGWRegistration
			impus   string
			expires int64
		)

		if err := rows.Scan(&r.IMPUKey, &r.IMPU, &impus, &r.MSISDN, &r.IMSI, &r.SCSCF, &r.AS, &expires,
			&r.Available); err != nil {
			return nil, fmt.Errorf("get IP-SM-GW registrations: %w", err)
		}

		r.ExpiresAt = time.Unix(0, expires).UTC()

		if impus != "" {
			r.IMPUs = strings.Split(impus, "\n")
		}

		out = append(out, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get IP-SM-GW registrations: %w", err)
	}

	return out, nil
}

func (d *DB) PutIPSMGWRegistration(ctx context.Context, r IPSMGWRegistration) (err error) {
	ctx, done := d.observe(ctx, "PutIPSMGWRegistration", poolLocal)
	defer done(&err)

	if _, err := d.local.ExecContext(ctx, `INSERT OR REPLACE INTO ip_sm_gw_registrations (impu_key, impu, impus, msisdn,
		imsi, scscf, as_uri, expires_at, available) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.IMPUKey, r.IMPU,
		strings.Join(r.IMPUs, "\n"), r.MSISDN, r.IMSI, r.SCSCF, r.AS, r.ExpiresAt.UTC().UnixNano(), r.Available); err != nil {
		return fmt.Errorf("put IP-SM-GW registration: %w", err)
	}

	return nil
}

func (d *DB) DeleteIPSMGWRegistration(ctx context.Context, impuKey string) (err error) {
	ctx, done := d.observe(ctx, "DeleteIPSMGWRegistration", poolLocal)
	defer done(&err)

	if _, err := d.local.ExecContext(ctx, `DELETE FROM ip_sm_gw_registrations WHERE impu_key = ?`, impuKey); err != nil {
		return fmt.Errorf("delete IP-SM-GW registration: %w", err)
	}

	return nil
}

func (d *DB) IPSMGWHSSRegistrations(ctx context.Context) (_ []IPSMGWHSSRegistration, err error) {
	ctx, done := d.observe(ctx, "IPSMGWHSSRegistrations", poolLocal)
	defer done(&err)

	rows, err := d.local.QueryContext(ctx, `SELECT user_key, msisdn, impu, number, hss_host, hss_realm
		FROM ip_sm_gw_hss_registrations ORDER BY user_key`)
	if err != nil {
		return nil, fmt.Errorf("get IP-SM-GW HSS registrations: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []IPSMGWHSSRegistration

	for rows.Next() {
		var r IPSMGWHSSRegistration

		if err := rows.Scan(&r.UserKey, &r.MSISDN, &r.IMPU, &r.Number, &r.HSS.Host, &r.HSS.Realm); err != nil {
			return nil, fmt.Errorf("get IP-SM-GW HSS registrations: %w", err)
		}

		out = append(out, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get IP-SM-GW HSS registrations: %w", err)
	}

	return out, nil
}

func (d *DB) PutIPSMGWHSSRegistration(ctx context.Context, r IPSMGWHSSRegistration) (err error) {
	ctx, done := d.observe(ctx, "PutIPSMGWHSSRegistration", poolLocal)
	defer done(&err)

	if _, err := d.local.ExecContext(ctx, `INSERT OR REPLACE INTO ip_sm_gw_hss_registrations (user_key, msisdn, impu,
		number, hss_host, hss_realm) VALUES (?, ?, ?, ?, ?, ?)`, r.UserKey, r.MSISDN, r.IMPU, r.Number, r.HSS.Host,
		r.HSS.Realm); err != nil {
		return fmt.Errorf("put IP-SM-GW HSS registration: %w", err)
	}

	return nil
}

func (d *DB) DeleteIPSMGWHSSRegistration(ctx context.Context, userKey string) (err error) {
	ctx, done := d.observe(ctx, "DeleteIPSMGWHSSRegistration", poolLocal)
	defer done(&err)

	if _, err := d.local.ExecContext(ctx, `DELETE FROM ip_sm_gw_hss_registrations WHERE user_key = ?`, userKey); err != nil {
		return fmt.Errorf("delete IP-SM-GW HSS registration: %w", err)
	}

	return nil
}
