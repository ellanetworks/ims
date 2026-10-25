package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
)

func ptr[T any](v T) *T { return &v }

func getMCPTT(t *testing.T, d *DB) settings.Settings {
	t.Helper()

	s, err := d.GetMCPTTSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	return s
}

// The settings of a new database are valid, of the PSIs and the domain of the organisation of the home domain: the
// service configuration of the seed row, its domain empty, no users or groups.
func TestSeed(t *testing.T) {
	s := getMCPTT(t, openTestDB(t))

	if !reflect.DeepEqual(s, settings.Settings{}) {
		t.Errorf("settings %+v", s)
	}

	s.ServiceConfiguration.Domain = "ims.mnc001.mcc001.3gppnetwork.org"

	if _, err := settings.New(withPSIs(s)); err != nil {
		t.Errorf("seed settings invalid: %v", err)
	}
}

// withPSIs is s of PSIs, which the database does not store: they are of the home domain.
func withPSIs(s settings.Settings) settings.Settings {
	s.System = settings.System{
		ParticipatingPSI: "sip:mcptt-participating@mcptt.ims.mnc001.mcc001.3gppnetwork.org",
		ControllingPSI:   "sip:mcptt-controlling@mcptt.ims.mnc001.mcc001.3gppnetwork.org",
	}

	return s
}

// example are settings of every column: optional elements present and absent, explicit zeros. What the server fixes
// to its defaults is not stored.
func mcpttExample() settings.Settings {
	return settings.Settings{
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain:    "example.com",
			OnNetwork: settings.OnNetwork{TransmitTime: settings.TransmitTime{TimeLimit: ptr(30 * time.Second)}},
		},
		Users: []settings.User{
			{
				MCPTTUserID: "sip:a@example.com", Alias: "Alice", IMPUs: []string{"sip:a@ims.example.com", "tel:+15550100"},
				ImplicitAffiliations: []string{"sip:g@example.com", "sip:h@example.com"},
			},
			{MCPTTUserID: "sip:b@example.com", IMPUs: []string{"sip:b@ims.example.com"}},
			{MCPTTUserID: "sip:c@example.com", IMPUs: []string{"sip:c@ims.example.com"}},
		},
		Groups: []settings.Group{
			{
				URI: "sip:g@example.com", DisplayName: "Fire Dept.", InviteMembers: true, T4: ptr(time.Duration(0)),
				TNG3: 10 * time.Minute, ReleaseOnInitiatorLeave: true,
				List: settings.List{Entries: []settings.Entry{
					{URI: "sip:c@example.com"},
					{URI: "sip:a@example.com", ReceiveOnly: true},
					{URI: "sip:b@example.com"},
				}},
			},
			{
				URI: "sip:h@example.com", InviteMembers: true, TNG3: time.Minute,
				List: settings.List{Entries: []settings.Entry{{URI: "sip:a@example.com"}}},
			},
		},
	}
}

// store writes s to d in the order the API would: the users without their implicit affiliations, the groups, then
// the affiliations.
func storeMCPTT(t *testing.T, d *DB, s settings.Settings) {
	t.Helper()

	ctx := t.Context()

	if err := d.UpdateMCPTTServiceConfiguration(ctx, s.ServiceConfiguration); err != nil {
		t.Fatal(err)
	}

	for _, u := range s.Users {
		u.ImplicitAffiliations = nil
		if err := d.CreateMCPTTUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	for _, g := range s.Groups {
		if err := d.CreateMCPTTGroup(ctx, g, nil); err != nil {
			t.Fatal(err)
		}
	}

	for _, u := range s.Users {
		if err := d.UpdateMCPTTUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
}

// What is written is what is read, the NULLs of the absent elements included, and after a reopen.
func TestRoundTrip(t *testing.T) {
	want := mcpttExample()
	if _, err := settings.New(withPSIs(want)); err != nil {
		t.Fatalf("example invalid: %v", err)
	}

	path := filepath.Join(t.TempDir(), "ims.db")

	d, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	storeMCPTT(t, d, want)

	if got := getMCPTT(t, d); !reflect.DeepEqual(got, want) {
		t.Errorf("settings\n%+v\nwant\n%+v", got, want)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	if got := getMCPTT(t, d); !reflect.DeepEqual(got, want) {
		t.Errorf("settings after a reopen\n%+v\nwant\n%+v", got, want)
	}

	// And back to absent.
	if err := d.UpdateMCPTTServiceConfiguration(t.Context(), settings.ServiceConfiguration{Domain: "example.org"}); err != nil {
		t.Fatal(err)
	}

	if got := getMCPTT(t, d).ServiceConfiguration; !reflect.DeepEqual(got, settings.ServiceConfiguration{Domain: "example.org"}) {
		t.Errorf("service configuration %+v", got)
	}
}

// A user and a group are found by their identity, as settings.Key has it: an update may write it otherwise, and a
// creation of another of the same identity is a duplicate.
func TestIdentities(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)
	storeMCPTT(t, d, mcpttExample())

	if err := d.CreateMCPTTUser(ctx, settings.User{MCPTTUserID: "sip:a@EXAMPLE.com"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate user: %v", err)
	}

	if err := d.CreateMCPTTGroup(ctx, settings.Group{URI: "sips:g@example.com"}, nil); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate group: %v", err)
	}

	a := settings.User{
		MCPTTUserID: "sip:a@EXAMPLE.com", IMPUs: []string{"tel:+15550101"},
		ImplicitAffiliations: []string{"sip:h@example.com"},
	}
	if err := d.UpdateMCPTTUser(ctx, a); err != nil {
		t.Fatal(err)
	}

	s := getMCPTT(t, d)
	if !reflect.DeepEqual(s.Users[0], a) {
		t.Errorf("user %+v, want %+v", s.Users[0], a)
	}

	// The members show the MCPTT ID as the user has it.
	if e := s.Groups[0].List.Entries[1]; e.URI != "sip:a@EXAMPLE.com" {
		t.Errorf("member %+v", e)
	}

	for _, err := range []error{
		d.UpdateMCPTTUser(ctx, settings.User{MCPTTUserID: "sip:x@example.com"}),
		d.DeleteMCPTTUser(ctx, "sip:x@example.com"),
		d.UpdateMCPTTGroup(ctx, settings.Group{URI: "sip:x@example.com"}, nil),
		d.DeleteMCPTTGroup(ctx, "sip:x@example.com"),
		d.PutMCPTTMember(ctx, "sip:x@example.com", settings.Entry{URI: "sip:a@example.com"}, nil),
		d.DeleteMCPTTMember(ctx, "sip:g@example.com", "sip:x@example.com"),
		d.DeleteMCPTTMember(ctx, "sip:x@example.com", "sip:a@example.com"),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("of an unknown identity: %v", err)
		}
	}

	if err := d.CreateMCPTTUser(ctx, settings.User{MCPTTUserID: "mailto:a@example.com"}); err == nil {
		t.Error("user of a mailto URI created")
	}
}

// A user deleted leaves its groups; a group deleted, or a member removed, goes from the implicit affiliations of its
// users; an update of a group keeps the affiliations of the members it keeps, wherever they move.
func TestCascades(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)
	storeMCPTT(t, d, mcpttExample())

	g := mcpttExample().Groups[0]
	slices.Reverse(g.List.Entries)

	if err := d.UpdateMCPTTGroup(ctx, g, nil); err != nil {
		t.Fatal(err)
	}

	if a := getMCPTT(t, d).Users[0].ImplicitAffiliations; !reflect.DeepEqual(a, []string{"sip:g@example.com", "sip:h@example.com"}) {
		t.Errorf("a's affiliations after a reorder %v", a)
	}

	if err := d.DeleteMCPTTUser(ctx, "sip:b@example.com"); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteMCPTTMember(ctx, "sip:h@example.com", "sip:a@example.com"); err != nil {
		t.Fatal(err)
	}

	s := getMCPTT(t, d)
	if a := s.Users[0].ImplicitAffiliations; !reflect.DeepEqual(a, []string{"sip:g@example.com"}) {
		t.Errorf("a's affiliations after its removal from h %v", a)
	}

	if e := s.Groups[0].List.Entries; len(e) != 2 || e[0].URI != "sip:a@example.com" {
		t.Errorf("g's members after b's deletion %+v", e)
	}

	if err := d.DeleteMCPTTGroup(ctx, "sip:g@example.com"); err != nil {
		t.Fatal(err)
	}

	if s := getMCPTT(t, d); len(s.Users[0].ImplicitAffiliations) != 0 || len(s.Groups) != 1 {
		t.Errorf("settings after g's deletion %+v", s)
	}
}

// An implicit affiliation is of a member of the group; one written with a change of a group or member is in it.
func TestImplicitAffiliations(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)
	storeMCPTT(t, d, mcpttExample())

	b := mcpttExample().Users[1]
	b.ImplicitAffiliations = []string{"sip:h@example.com"}

	if err := d.UpdateMCPTTUser(ctx, b); err == nil {
		t.Error("an implicit affiliation of a group b is not a member of")
	}

	if err := d.PutMCPTTMember(ctx, "sip:h@example.com", settings.Entry{URI: "sip:b@example.com"}, []settings.User{b}); err != nil {
		t.Fatal(err)
	}

	c := mcpttExample().Users[2]
	c.ImplicitAffiliations = []string{"sip:k@example.com"}
	k := settings.Group{
		URI: "sip:k@example.com", InviteMembers: true, TNG3: time.Minute,
		List: settings.List{Entries: []settings.Entry{{URI: "sip:c@example.com"}}},
	}

	if err := d.CreateMCPTTGroup(ctx, k, []settings.User{c}); err != nil {
		t.Fatal(err)
	}

	s := getMCPTT(t, d)
	if a := s.Users[1].ImplicitAffiliations; !reflect.DeepEqual(a, b.ImplicitAffiliations) {
		t.Errorf("b's affiliations %v", a)
	}

	if a := s.Users[2].ImplicitAffiliations; !reflect.DeepEqual(a, c.ImplicitAffiliations) {
		t.Errorf("c's affiliations %v", a)
	}
}

// A new member goes after the others; a member put again keeps its place.
func TestPutMember(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)
	storeMCPTT(t, d, mcpttExample())

	if err := d.PutMCPTTMember(ctx, "sip:h@example.com", settings.Entry{URI: "sip:c@example.com"}, nil); err != nil {
		t.Fatal(err)
	}

	if err := d.PutMCPTTMember(ctx, "sip:h@example.com", settings.Entry{URI: "sip:a@EXAMPLE.com", ReceiveOnly: true}, nil); err != nil {
		t.Fatal(err)
	}

	want := []settings.Entry{{URI: "sip:a@example.com", ReceiveOnly: true}, {URI: "sip:c@example.com"}}
	if got := getMCPTT(t, d).Groups[1].List.Entries; !reflect.DeepEqual(got, want) {
		t.Errorf("members %+v, want %+v", got, want)
	}

	if err := d.PutMCPTTMember(ctx, "sip:h@example.com", settings.Entry{URI: "sip:x@example.com"}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("member not a user: %v", err)
	}
}

// A change that fails changes nothing.
func TestAtomic(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)
	storeMCPTT(t, d, mcpttExample())

	before := getMCPTT(t, d)

	g := mcpttExample().Groups[0]
	g.Encodings = []string{"AMR"}
	g.List.Entries = append(g.List.Entries, settings.Entry{URI: "sip:x@example.com"})

	if err := d.UpdateMCPTTGroup(ctx, g, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a member not a user: %v", err)
	}

	u := settings.User{
		MCPTTUserID: "sip:d@example.com", IMPUs: []string{"sip:d@ims.example.com"},
		ImplicitAffiliations: []string{"sip:x@example.com"},
	}
	if err := d.CreateMCPTTUser(ctx, u); !errors.Is(err, ErrNotFound) {
		t.Fatalf("creation of an affiliation not a group: %v", err)
	}

	if after := getMCPTT(t, d); !reflect.DeepEqual(after, before) {
		t.Errorf("settings changed:\n%+v\nwas\n%+v", after, before)
	}
}

// The context of a call is that of its statements.
func TestCanceled(t *testing.T) {
	d := openTestDB(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := d.GetMCPTTSettings(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("GetSettings: %v", err)
	}
}

// A row of a user deleted outside the server, with the foreign keys off as the sqlite3 shell has them, is no other
// user's: the settings are refused, and so is the database at its next opening.
func TestOrphans(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "ims.db")

	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	for _, u := range mcpttExample().Users {
		u.ImplicitAffiliations = nil
		if err := d.CreateMCPTTUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	shell, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = shell.Close() }()

	if _, err := shell.ExecContext(ctx, `DELETE FROM mcptt_users WHERE mcptt_id = 'sip:b@example.com'`); err != nil {
		t.Fatal(err)
	}

	if _, err := d.GetMCPTTSettings(ctx); !errors.Is(err, errOrphan) {
		t.Errorf("GetSettings: %v", err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "a row of mcptt_user_impus references no row of mcptt_users") {
		t.Errorf("Open: %v", err)
	}
}

// An update of a row deleted outside the server fails.
func TestSingleRowGone(t *testing.T) {
	d := openTestDB(t)

	if _, err := d.conn.ExecContext(t.Context(), `DELETE FROM mcptt_service_configuration`); err != nil {
		t.Fatal(err)
	}

	if err := d.UpdateMCPTTServiceConfiguration(t.Context(), mcpttExample().ServiceConfiguration); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateServiceConfiguration: %v", err)
	}
}

// A path of the characters of a URI's query, fragment and escapes is a file of that name.
func TestPathOfURICharacters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a?b#c%d e.db")

	d, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("no database at %s: %v", path, err)
	}
}

// The keys stored are recomputed at the opening: one of another form, of an earlier sip package, is found again.
func TestRekey(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "ims.db")

	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	storeMCPTT(t, d, mcpttExample())

	if _, err := d.conn.ExecContext(ctx, `UPDATE mcptt_users SET key = 'stale' WHERE mcptt_id = 'sip:b@example.com'`); err != nil {
		t.Fatal(err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	if d, err = Open(ctx, path); err != nil {
		t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	if err := d.DeleteMCPTTMember(ctx, "sip:g@example.com", "sip:b@example.com"); err != nil {
		t.Errorf("member of a key recomputed: %v", err)
	}
}
