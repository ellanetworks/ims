package db

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

var keptTables = []string{
	"operator", "diameter_peers", "diameter_routes", "policy", "call_record_settings", "call_records",
	"sip_trunks", "sip_trunk_addresses", "sip_trunk_inbound_cidrs", "sip_trunk_tls_ca_certificates", "sip_trunk_tls_pins",
	"sip_trunk_tls_identities", "sip_routes", "sip_route_trunks",
	"mcptt_service_configuration", "mcptt_users", "mcptt_user_impus", "mcptt_groups", "mcptt_group_members",
	"mcptt_user_implicit_affiliations", "ip_sm_gw", "sms_routes", "sms_route_peers",
	"users", "user_identities", "api_tokens", "oidc", "oidc_group_roles",
}

func TestEveryTableIsKeptOrExcludedFromBackups(t *testing.T) {
	ctx := context.Background()

	d, err := Open(ctx, filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	rows, err := d.conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}

		kept, excluded := slices.Contains(keptTables, name), slices.Contains(excludedTables, name)
		if kept == excluded {
			t.Errorf("table %s: kept %t, excluded %t; want exactly one", name, kept, excluded)
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
