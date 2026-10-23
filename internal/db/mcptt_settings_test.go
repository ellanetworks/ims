package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

// The settings of a new database are valid: the PSIs and service configuration of the seed rows, no users or groups.
func TestSeed(t *testing.T) {
	s := getMCPTT(t, openTestDB(t))

	want := settings.Settings{
		System: settings.System{
			ParticipatingPSI: "sip:mcptt-participating@mcptt.example.com",
			ControllingPSI:   "sip:mcptt-controlling@mcptt.example.com",
		},
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain: "mcptt.example.com",
			OnNetwork: settings.OnNetwork{
				FCTimersCounters: settings.FCTimersCounters{T7: ptr(2 * time.Second)},
			},
		},
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("settings %+v, want %+v", s, want)
	}

	if _, err := settings.New(s); err != nil {
		t.Errorf("seed settings invalid: %v", err)
	}
}

// example are settings of every column: optional elements present and absent, explicit zeros.
func mcpttExample() settings.Settings {
	return settings.Settings{
		System: settings.System{ParticipatingPSI: "sip:pf@example.com", ControllingPSI: "sip:cf@example.com"},
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain: "example.com",
			OnNetwork: settings.OnNetwork{
				NumLevelsPriorityHierarchy: ptr(8),
				TransmitTime:               settings.TransmitTime{TimeLimit: ptr(30 * time.Second)},
				FCTimersCounters: settings.FCTimersCounters{
					T1: ptr(4 * time.Second), T3: ptr(time.Duration(0)), T7: ptr(1500 * time.Millisecond), C7: ptr(0),
				},
			},
		},
		Users: []settings.User{
			{
				MCPTTUserID: "sip:a@example.com", IMPUs: []string{"sip:a@ims.example.com", "tel:+15550100"},
				ImplicitAffiliations: []string{"sip:g@example.com", "sip:h@example.com"}, MaxAffiliationsN2: ptr(2),
			},
			{MCPTTUserID: "sip:b@example.com", IMPUs: []string{"sip:b@ims.example.com"}, MaxSimultaneousCallsN6: ptr(2)},
			{MCPTTUserID: "sip:c@example.com", IMPUs: []string{"sip:c@ims.example.com"}, MaxAffiliationsN2: ptr(0)},
		},
		Groups: []settings.Group{
			{
				URI: "sip:g@example.com", InviteMembers: true, MaxParticipants: 10, Encodings: []string{"AMR-WB", "AMR"},
				T4: ptr(time.Duration(0)), TNG3: 10 * time.Minute, MinimumToStart: 1, TNG1: 5 * time.Second,
				TNG1Action: settings.Abandon, MinimumAffiliated: 2, ReleaseOnInitiatorLeave: true,
				List: settings.List{Entries: []settings.Entry{
					{URI: "sip:c@example.com", UserPriority: 3, Required: true},
					{URI: "sip:a@example.com", ReceiveOnly: true, AffiliationRequired: true},
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

	if err := d.UpdateMCPTTSystem(ctx, s.System); err != nil {
		t.Fatal(err)
	}

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
		if err := d.CreateMCPTTGroup(ctx, g); err != nil {
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
	if _, err := settings.New(want); err != nil {
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

	if err := d.CreateMCPTTGroup(ctx, settings.Group{URI: "sips:g@example.com"}); !errors.Is(err, ErrDuplicate) {
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
		d.UpdateMCPTTGroup(ctx, settings.Group{URI: "sip:x@example.com"}),
		d.DeleteMCPTTGroup(ctx, "sip:x@example.com"),
		d.PutMCPTTMember(ctx, "sip:x@example.com", settings.Entry{URI: "sip:a@example.com"}),
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

// A user that is a member and a group that is an implicit affiliation are not deleted; once not, they are. An update
// of a group keeps the affiliations to it.
func TestReferences(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)
	storeMCPTT(t, d, mcpttExample())

	if err := d.DeleteMCPTTUser(ctx, "sip:b@example.com"); err == nil {
		t.Error("member deleted")
	}

	if err := d.DeleteMCPTTGroup(ctx, "sip:h@example.com"); err == nil {
		t.Error("implicit affiliation deleted")
	}

	h := mcpttExample().Groups[1]
	h.List.Entries = append(h.List.Entries, settings.Entry{URI: "sip:c@example.com"})

	if err := d.UpdateMCPTTGroup(ctx, h); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteMCPTTMember(ctx, "sip:g@example.com", "sip:b@example.com"); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteMCPTTUser(ctx, "sip:b@example.com"); err != nil {
		t.Fatal(err)
	}

	a := mcpttExample().Users[0]
	a.ImplicitAffiliations = []string{"sip:g@example.com"}

	if err := d.UpdateMCPTTUser(ctx, a); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteMCPTTGroup(ctx, "sip:h@example.com"); err != nil {
		t.Fatal(err)
	}

	s := getMCPTT(t, d)
	if len(s.Users) != 2 || len(s.Groups) != 1 || len(s.Groups[0].List.Entries) != 2 {
		t.Errorf("settings %+v", s)
	}
}

// A new member goes after the others; a member put again keeps its place.
func TestPutMember(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)
	storeMCPTT(t, d, mcpttExample())

	if err := d.PutMCPTTMember(ctx, "sip:h@example.com", settings.Entry{URI: "sip:c@example.com", UserPriority: 1}); err != nil {
		t.Fatal(err)
	}

	if err := d.PutMCPTTMember(ctx, "sip:h@example.com", settings.Entry{URI: "sip:a@EXAMPLE.com", Required: true}); err != nil {
		t.Fatal(err)
	}

	want := []settings.Entry{{URI: "sip:a@example.com", Required: true}, {URI: "sip:c@example.com", UserPriority: 1}}
	if got := getMCPTT(t, d).Groups[1].List.Entries; !reflect.DeepEqual(got, want) {
		t.Errorf("members %+v, want %+v", got, want)
	}

	if err := d.PutMCPTTMember(ctx, "sip:h@example.com", settings.Entry{URI: "sip:x@example.com"}); !errors.Is(err, ErrNotFound) {
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

	if err := d.UpdateMCPTTGroup(ctx, g); !errors.Is(err, ErrNotFound) {
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

	if _, err := d.conn.ExecContext(t.Context(), `DELETE FROM mcptt_system`); err != nil {
		t.Fatal(err)
	}

	if err := d.UpdateMCPTTSystem(t.Context(), mcpttExample().System); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateSystem: %v", err)
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
