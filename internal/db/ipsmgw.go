package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// IPSMGWRegistration is a registration of the IP-SM-GW: of the third-party REGISTERs of an IMPU (TS 24.341
// §5.3.3.2), and whether a contact of the IMPU can receive short messages over IP. Its URIs are as received.
type IPSMGWRegistration struct {
	// IMPUKey is the canonical form of IMPU, which the registration is found by.
	IMPUKey string
	IMPU    string
	// IMPUs are those of the implicit registration set of IMPU.
	IMPUs []string
	// MSISDN and IMSI are the user's identities, of the third-party REGISTER, each "" if unknown.
	MSISDN string
	IMSI   string
	// SCSCF is the Contact of the third-party REGISTER, and AS its Request-URI: the S-CSCF the reg event
	// subscription goes to, and the URI of the IP-SM-GW it comes from.
	SCSCF     string
	AS        string
	ExpiresAt time.Time
	Available bool
}

// IPSMGWHSSRegistration is a user the HSS has the IP-SM-GW registered for (TS 29.328 §6.1.2.1 step 4b), by the
// identity it was registered with, the MSISDN, else the IMPU, and the number it was given.
type IPSMGWHSSRegistration struct {
	UserKey string
	MSISDN  string
	IMPU    string
	Number  string
}

func (d *DB) IPSMGWRegistrations(ctx context.Context) (_ []IPSMGWRegistration, err error) {
	defer d.observe(poolWrite, &err)()

	rows, err := d.conn.QueryContext(ctx, `SELECT impu_key, impu, impus, msisdn, imsi, scscf, as_uri, expires_at,
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

// PutIPSMGWRegistration stores r, replacing the registration of its IMPU.
func (d *DB) PutIPSMGWRegistration(ctx context.Context, r IPSMGWRegistration) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `INSERT OR REPLACE INTO ip_sm_gw_registrations (impu_key, impu, impus, msisdn,
		imsi, scscf, as_uri, expires_at, available) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.IMPUKey, r.IMPU,
		strings.Join(r.IMPUs, "\n"), r.MSISDN, r.IMSI, r.SCSCF, r.AS, r.ExpiresAt.UTC().UnixNano(), r.Available); err != nil {
		return fmt.Errorf("put IP-SM-GW registration: %w", err)
	}

	return nil
}

func (d *DB) DeleteIPSMGWRegistration(ctx context.Context, impuKey string) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `DELETE FROM ip_sm_gw_registrations WHERE impu_key = ?`, impuKey); err != nil {
		return fmt.Errorf("delete IP-SM-GW registration: %w", err)
	}

	return nil
}

func (d *DB) IPSMGWHSSRegistrations(ctx context.Context) (_ []IPSMGWHSSRegistration, err error) {
	defer d.observe(poolWrite, &err)()

	rows, err := d.conn.QueryContext(ctx, `SELECT user_key, msisdn, impu, number FROM ip_sm_gw_hss_registrations
		ORDER BY user_key`)
	if err != nil {
		return nil, fmt.Errorf("get IP-SM-GW HSS registrations: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []IPSMGWHSSRegistration

	for rows.Next() {
		var r IPSMGWHSSRegistration

		if err := rows.Scan(&r.UserKey, &r.MSISDN, &r.IMPU, &r.Number); err != nil {
			return nil, fmt.Errorf("get IP-SM-GW HSS registrations: %w", err)
		}

		out = append(out, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get IP-SM-GW HSS registrations: %w", err)
	}

	return out, nil
}

// PutIPSMGWHSSRegistration stores r, replacing the one of its user.
func (d *DB) PutIPSMGWHSSRegistration(ctx context.Context, r IPSMGWHSSRegistration) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `INSERT OR REPLACE INTO ip_sm_gw_hss_registrations (user_key, msisdn, impu,
		number) VALUES (?, ?, ?, ?)`, r.UserKey, r.MSISDN, r.IMPU, r.Number); err != nil {
		return fmt.Errorf("put IP-SM-GW HSS registration: %w", err)
	}

	return nil
}

func (d *DB) DeleteIPSMGWHSSRegistration(ctx context.Context, userKey string) (err error) {
	defer d.observe(poolWrite, &err)()

	if _, err := d.conn.ExecContext(ctx, `DELETE FROM ip_sm_gw_hss_registrations WHERE user_key = ?`, userKey); err != nil {
		return fmt.Errorf("delete IP-SM-GW HSS registration: %w", err)
	}

	return nil
}
