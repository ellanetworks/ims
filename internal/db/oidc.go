package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type OIDC struct {
	Enabled              bool
	ExternalURL          string
	Issuer               string
	ClientID             string
	ClientSecret         string
	DisplayName          string
	Scopes               []string
	SessionLifetime      time.Duration
	TrustUnverifiedEmail bool
	CACertificate        string
	GroupsClaim          string
	GroupRoles           []GroupRole
}

type GroupRole struct {
	Group string
	Role  Role
}

func (d *DB) GetOIDC(ctx context.Context) (_ OIDC, err error) {
	defer d.observe(poolRead, &err)()

	var (
		o        OIDC
		scopes   string
		lifetime int64
	)

	if err := d.read.QueryRowContext(ctx, `SELECT enabled, external_url, issuer, client_id, client_secret,
		display_name, scopes, session_lifetime, trust_unverified_email, ca_certificate, groups_claim
		FROM oidc WHERE id = 1`).Scan(&o.Enabled, &o.ExternalURL, &o.Issuer, &o.ClientID, &o.ClientSecret,
		&o.DisplayName, &scopes, &lifetime, &o.TrustUnverifiedEmail, &o.CACertificate, &o.GroupsClaim); err != nil {
		return OIDC{}, fmt.Errorf("get OIDC: %w", err)
	}

	o.Scopes = strings.Fields(scopes)
	o.SessionLifetime = time.Duration(lifetime)

	if o.GroupRoles, err = d.groupRoles(ctx); err != nil {
		return OIDC{}, err
	}

	return o, nil
}

func (d *DB) groupRoles(ctx context.Context) ([]GroupRole, error) {
	rows, err := d.read.QueryContext(ctx, `SELECT group_name, role_id FROM oidc_group_roles ORDER BY position`)
	if err != nil {
		return nil, fmt.Errorf("get OIDC group roles: %w", err)
	}

	defer func() { _ = rows.Close() }()

	groupRoles := []GroupRole{}

	for rows.Next() {
		var g GroupRole
		if err := rows.Scan(&g.Group, &g.Role); err != nil {
			return nil, fmt.Errorf("get OIDC group roles: %w", err)
		}

		groupRoles = append(groupRoles, g)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get OIDC group roles: %w", err)
	}

	return groupRoles, nil
}

func (d *DB) UpdateOIDC(ctx context.Context, o OIDC) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "update OIDC", func(tx *sql.Tx) error {
		var issuer string
		if err := tx.QueryRowContext(ctx, `SELECT issuer FROM oidc WHERE id = 1`).Scan(&issuer); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `UPDATE oidc SET enabled = ?, external_url = ?, issuer = ?, client_id = ?,
			client_secret = ?, display_name = ?, scopes = ?, session_lifetime = ?, trust_unverified_email = ?,
			ca_certificate = ?, groups_claim = ? WHERE id = 1`, o.Enabled, o.ExternalURL, o.Issuer, o.ClientID,
			o.ClientSecret, o.DisplayName, strings.Join(o.Scopes, " "), int64(o.SessionLifetime),
			o.TrustUnverifiedEmail, o.CACertificate, o.GroupsClaim); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM oidc_group_roles`); err != nil {
			return err
		}

		for i, g := range o.GroupRoles {
			if _, err := tx.ExecContext(ctx, `INSERT INTO oidc_group_roles (position, group_name, role_id)
				VALUES (?, ?, ?)`, i+1, g.Group, g.Role); err != nil {
				return err
			}
		}

		if !o.Enabled || o.Issuer != issuer {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE method = ?`, SessionOIDC); err != nil {
				return err
			}
		}

		return nil
	})
}

const oidcCookieKeyLength = 32

func (d *DB) OIDCCookieKey(ctx context.Context) (_ []byte, err error) {
	defer d.observe(poolWrite, &err)()

	return d.singletonSecret(ctx, "get OIDC cookie key", "oidc_cookie_key", "key", oidcCookieKeyLength)
}
