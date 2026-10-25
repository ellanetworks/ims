package db

import (
	"reflect"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	"github.com/ellanetworks/ims/internal/mcptt/registrar"
	"github.com/ellanetworks/ims/sip"
)

func mustURI(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

// What is saved of a binding is what is read, to the millisecond; a client saved again is replaced, one deleted
// removed.
func TestRegistrations(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)

	expires := time.UnixMilli(time.Now().Add(time.Hour).UnixMilli())
	since := expires.Add(-2 * time.Hour)
	full := registrar.Record{
		Binding: registrar.Binding{
			MCPTTID: mustURI(t, "sip:a@example.com"), ClientID: "c1", IMPU: mustURI(t, "sip:a@ims.example.com"),
			IMPUs: []sip.URI{mustURI(t, "sip:a@ims.example.com"), mustURI(t, "tel:+15550100")}, Expires: expires, Since: since,
			SCSCF: mustURI(t, "sip:scscf.ims.example.com:6060;transport=tcp"), Token: "t1",
			Charging: registrar.Charging{ICID: "4a2f", OrigIOI: "ims.example.com", FunctionAddresses: "ccf=192.0.2.1"},
		},
		AS: mustURI(t, "sip:as.mcptt.example.com"), Instance: "urn:uuid:00000000-0000-1000-8000-000000000001", RegID: "1",
		Contact: mustURI(t, "sip:ue@192.0.2.9:5060"), CallID: "x@ue", CSeq: 7,
	}
	bare := registrar.Record{Binding: registrar.Binding{
		MCPTTID: mustURI(t, "sip:b@example.com"), ClientID: "c2", IMPU: mustURI(t, "sip:b@ims.example.com"),
		IMPUs: []sip.URI{mustURI(t, "sip:b@ims.example.com")}, Expires: expires, Since: since,
	}}

	if err := d.SaveMCPTTRegistrations(ctx, []registrar.Record{full, bare}, nil); err != nil {
		t.Fatal(err)
	}

	got, err := d.MCPTTRegistrations(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, []registrar.Record{full, bare}) {
		t.Errorf("registrations\n%+v\nwant\n%+v", got, []registrar.Record{full, bare})
	}

	bare.Token = "t2"
	if err := d.SaveMCPTTRegistrations(ctx, []registrar.Record{bare}, []string{"c1"}); err != nil {
		t.Fatal(err)
	}

	if got, err := d.MCPTTRegistrations(ctx); err != nil || !reflect.DeepEqual(got, []registrar.Record{bare}) {
		t.Errorf("after a replace and a delete: %+v, %v", got, err)
	}
}

// What is saved of the affiliations of a client is what is read, its groups in order; a record of no groups and no
// entity-tag removes them.
func TestAffiliations(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)

	expires := time.UnixMilli(time.Now().Add(affiliation.MaxExpires).UnixMilli())
	a := affiliation.Record{
		MCPTTID: mustURI(t, "sip:a@example.com"), ClientID: "c1", ETag: "e1",
		Groups: []affiliation.Group{
			{ID: mustURI(t, "sip:g2@example.com"), Status: affiliation.Affiliated, Expires: expires},
			{ID: mustURI(t, "sip:g1@example.com"), Status: affiliation.Affiliating, Expires: expires},
		},
		PIDs: []string{"p1", ""},
	}
	b := affiliation.Record{MCPTTID: mustURI(t, "sip:b@example.com"), ClientID: "c1", ETag: "e2"}

	if err := d.SaveMCPTTAffiliations(ctx, []affiliation.Record{a, b}); err != nil {
		t.Fatal(err)
	}

	got, err := d.MCPTTAffiliations(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, []affiliation.Record{a, b}) {
		t.Errorf("affiliations\n%+v\nwant\n%+v", got, []affiliation.Record{a, b})
	}

	a.Groups, a.PIDs = a.Groups[1:], a.PIDs[1:]

	if err := d.SaveMCPTTAffiliations(ctx, []affiliation.Record{a, {MCPTTID: b.MCPTTID, ClientID: "c1"}}); err != nil {
		t.Fatal(err)
	}

	if got, err := d.MCPTTAffiliations(ctx); err != nil || !reflect.DeepEqual(got, []affiliation.Record{a}) {
		t.Errorf("after a replace and a removal: %+v, %v", got, err)
	}
}

// A row whose URIs do not parse is left out, and removed: the others are restored.
func TestUnparsableState(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)

	good := registrar.Record{Binding: registrar.Binding{
		MCPTTID: mustURI(t, "sip:a@example.com"), ClientID: "c1", IMPU: mustURI(t, "sip:a@ims.example.com"),
		IMPUs: []sip.URI{mustURI(t, "sip:a@ims.example.com")}, Expires: time.UnixMilli(time.Now().Add(time.Hour).UnixMilli()),
	}}

	if err := d.SaveMCPTTRegistrations(ctx, []registrar.Record{good}, nil); err != nil {
		t.Fatal(err)
	}

	if err := d.SaveMCPTTAffiliations(ctx, []affiliation.Record{{MCPTTID: good.MCPTTID, ClientID: "c1", ETag: "e"}}); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{
		`INSERT INTO mcptt_registrations SELECT 'c2', mcptt_id, 'not a URI', impus, expires_at, since, scscf, as_uri, token, instance,
			reg_id, contact, call_id, cseq, icid, orig_ioi, charging_function_addresses FROM mcptt_registrations`,
		`INSERT INTO mcptt_affiliation_clients VALUES ('k', 'c2', 'not a URI', '')`,
	} {
		if _, err := d.conn.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	for range 2 {
		regs, err := d.MCPTTRegistrations(ctx)
		if err != nil || len(regs) != 1 || regs[0].ClientID != "c1" {
			t.Fatalf("registrations %+v, %v", regs, err)
		}

		affs, err := d.MCPTTAffiliations(ctx)
		if err != nil || len(affs) != 1 || affs[0].ClientID != "c1" {
			t.Fatalf("affiliations %+v, %v", affs, err)
		}
	}

	var n int
	if err := d.conn.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM mcptt_registrations) + (SELECT COUNT(*) FROM mcptt_affiliation_clients)`).Scan(&n); err != nil || n != 2 {
		t.Errorf("%d rows left, %v", n, err)
	}
}
