package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
)

// SaveRegistrations saves the bindings of records, replacing those of their clients, and removes those of the
// clients of deleted, in one transaction.
func (d *DB) SaveMCPTTRegistrations(ctx context.Context, records []registrar.Record, deleted []string) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "save MCPTT registrations", func(tx *sql.Tx) error {
		for _, client := range deleted {
			if _, err := tx.ExecContext(ctx, `DELETE FROM mcptt_registrations WHERE client_id = ?`, client); err != nil {
				return err
			}
		}

		for _, r := range records {
			impus := make([]string, len(r.IMPUs))
			for i, u := range r.IMPUs {
				impus[i] = u.String()
			}

			set, err := json.Marshal(impus)
			if err != nil {
				return err
			}

			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO mcptt_registrations (client_id, mcptt_id, impu, impus,
				expires_at, since, scscf, as_uri, token, instance, reg_id, contact, call_id, cseq, icid, orig_ioi,
				charging_function_addresses) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.ClientID,
				r.MCPTTID.String(), r.IMPU.String(), string(set), r.Expires.UnixMilli(), r.Since.UnixMilli(), uriString(r.SCSCF),
				uriString(r.AS), r.Token, r.Instance, r.RegID, uriString(r.Contact), r.CallID, r.CSeq,
				r.Charging.ICID, r.Charging.OrigIOI, r.Charging.FunctionAddresses); err != nil {
				return err
			}
		}

		return nil
	})
}

// Registrations are the bindings saved. A row whose URIs do not parse, which only a change outside the server or a
// release of the sip package that parses less makes, is removed.
func (d *DB) MCPTTRegistrations(ctx context.Context) (_ []registrar.Record, err error) {
	defer d.observe(poolWrite, &err)()

	out, bad, err := d.mcpttRegistrations(ctx)
	if err != nil || len(bad) == 0 {
		return out, err
	}

	return out, d.SaveMCPTTRegistrations(ctx, nil, bad)
}

func (d *DB) mcpttRegistrations(ctx context.Context) ([]registrar.Record, []string, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT client_id, mcptt_id, impu, impus, expires_at, since, scscf, as_uri, token,
		instance, reg_id, contact, call_id, cseq, icid, orig_ioi, charging_function_addresses FROM mcptt_registrations
		ORDER BY client_id`)
	if err != nil {
		return nil, nil, fmt.Errorf("get MCPTT registrations: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var (
		out []registrar.Record
		bad []string
	)

	for rows.Next() {
		var (
			r                                 registrar.Record
			id, impu, set, scscf, as, contact string
			expires, since                    int64
		)

		if err := rows.Scan(&r.ClientID, &id, &impu, &set, &expires, &since, &scscf, &as, &r.Token, &r.Instance, &r.RegID,
			&contact, &r.CallID, &r.CSeq, &r.Charging.ICID, &r.Charging.OrigIOI, &r.Charging.FunctionAddresses); err != nil {
			return nil, nil, fmt.Errorf("get MCPTT registrations: %w", err)
		}

		r.Expires, r.Since = time.UnixMilli(expires), time.UnixMilli(since)

		if !parseMCPTTRecord(&r, []string{id, impu, scscf, as, contact}, set) {
			bad = append(bad, r.ClientID)
			continue
		}

		out = append(out, r)
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("get MCPTT registrations: %w", err)
	}

	return out, bad, nil
}

// parseRecord parses the URIs of r, of uris and the JSON set of its IMPUs, reporting whether they all parse.
func parseMCPTTRecord(r *registrar.Record, uris []string, set string) bool {
	var impus []string
	if err := json.Unmarshal([]byte(set), &impus); err != nil {
		return false
	}

	var err error

	for i, to := range []*sip.URI{&r.MCPTTID, &r.IMPU, &r.SCSCF, &r.AS, &r.Contact} {
		if *to, err = parseURI(uris[i]); err != nil {
			return false
		}
	}

	for _, s := range impus {
		u, err := sip.ParseURI(s)
		if err != nil {
			return false
		}

		r.IMPUs = append(r.IMPUs, u)
	}

	return true
}

// ClearState removes the registrations and affiliations saved: of another mode, the clients register again.
func (d *DB) ClearMCPTTState(ctx context.Context) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "clear MCPTT state", func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM mcptt_registrations`, `DELETE FROM mcptt_affiliation_clients`} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}

		return nil
	})
}

// SaveAffiliations replaces the affiliations of the clients of records, and removes those of a record of no groups
// and no entity-tag, in one transaction.
func (d *DB) SaveMCPTTAffiliations(ctx context.Context, records []affiliation.Record) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "save MCPTT affiliations", func(tx *sql.Tx) error {
		for _, r := range records {
			uk := settings.Key(r.MCPTTID)

			if _, err := tx.ExecContext(ctx, `DELETE FROM mcptt_affiliation_clients WHERE user_key = ? AND client_id = ?`,
				uk, r.ClientID); err != nil {
				return err
			}

			if len(r.Groups) == 0 && r.ETag == "" {
				continue
			}

			if _, err := tx.ExecContext(ctx, `INSERT INTO mcptt_affiliation_clients (user_key, client_id, mcptt_id, etag)
				VALUES (?, ?, ?, ?)`, uk, r.ClientID, r.MCPTTID.String(), r.ETag); err != nil {
				return err
			}

			for i, g := range r.Groups {
				var pid string
				if i < len(r.PIDs) {
					pid = r.PIDs[i]
				}

				if _, err := tx.ExecContext(ctx, `INSERT INTO mcptt_affiliations (user_key, client_id, position, group_uri,
					status, expires_at, pid) VALUES (?, ?, ?, ?, ?, ?, ?)`, uk, r.ClientID, i, g.ID.String(),
					string(g.Status), g.Expires.UnixMilli(), pid); err != nil {
					return err
				}
			}
		}

		return nil
	})
}

// Affiliations are the affiliations saved, of each client its groups in order. A client of an MCPTT ID or group that
// does not parse is removed.
func (d *DB) MCPTTAffiliations(ctx context.Context) (_ []affiliation.Record, err error) {
	defer d.observe(poolWrite, &err)()

	out, bad, err := d.mcpttAffiliations(ctx)
	if err != nil || len(bad) == 0 {
		return out, err
	}

	return out, d.inTx(ctx, "remove MCPTT affiliations", func(tx *sql.Tx) error {
		for _, k := range bad {
			if _, err := tx.ExecContext(ctx, `DELETE FROM mcptt_affiliation_clients WHERE user_key = ? AND client_id = ?`,
				k[0], k[1]); err != nil {
				return err
			}
		}

		return nil
	})
}

func (d *DB) mcpttAffiliations(ctx context.Context) ([]affiliation.Record, [][2]string, error) {
	tx, err := d.conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, fmt.Errorf("get MCPTT affiliations: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var (
		out   []affiliation.Record
		bad   [][2]string
		index = map[[2]string]int{}
	)

	err = queryRows(ctx, tx, "get MCPTT affiliations", `SELECT user_key, client_id, mcptt_id, etag FROM mcptt_affiliation_clients
		ORDER BY user_key, client_id`, func(rows *sql.Rows) error {
		var (
			uk, id string
			r      affiliation.Record
		)

		if err := rows.Scan(&uk, &r.ClientID, &id, &r.ETag); err != nil {
			return err
		}

		u, err := sip.ParseURI(id)
		if err != nil {
			bad = append(bad, [2]string{uk, r.ClientID})
			return nil
		}

		r.MCPTTID = u
		index[[2]string{uk, r.ClientID}] = len(out)
		out = append(out, r)

		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	err = queryRows(ctx, tx, "get MCPTT affiliations", `SELECT user_key, client_id, group_uri, status, expires_at, pid
		FROM mcptt_affiliations ORDER BY user_key, client_id, position`, func(rows *sql.Rows) error {
		var (
			uk, client, group, status, pid string
			expires                        int64
		)

		if err := rows.Scan(&uk, &client, &group, &status, &expires, &pid); err != nil {
			return err
		}

		i, ok := index[[2]string{uk, client}]
		if !ok {
			return nil
		}

		g, err := sip.ParseURI(group)
		if err != nil {
			bad = append(bad, [2]string{uk, client})
			return nil
		}

		r := &out[i]
		r.Groups = append(r.Groups, affiliation.Group{ID: g, Status: affiliation.Status(status), Expires: time.UnixMilli(expires)})
		r.PIDs = append(r.PIDs, pid)

		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	// A client of a group that does not parse is removed whole, not restored of its other groups.
	out = slices.DeleteFunc(out, func(r affiliation.Record) bool {
		return slices.Contains(bad, [2]string{settings.Key(r.MCPTTID), r.ClientID})
	})

	return out, bad, nil
}

// uriString is the text of u, "" of the zero URI.
func uriString(u sip.URI) string {
	if u.Scheme == "" {
		return ""
	}

	return u.String()
}

// parseURI is the URI of s, the zero URI of "".
func parseURI(s string) (sip.URI, error) {
	if s == "" {
		return sip.URI{}, nil
	}

	return sip.ParseURI(s)
}
