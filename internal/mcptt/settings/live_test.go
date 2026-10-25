package settings_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
)

// recorder is a store that records its calls, and fails them with err.
type recorder struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (r *recorder) record(format string, a ...any) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, fmt.Sprintf(format, a...))

	return r.err
}

func (r *recorder) UpdateSystem(_ context.Context, s settings.System) error {
	return r.record("update system %s", s.ParticipatingPSI)
}

func (r *recorder) UpdateServiceConfiguration(_ context.Context, c settings.ServiceConfiguration) error {
	return r.record("update service configuration %s", c.Domain)
}

func (r *recorder) UpdateGroup(_ context.Context, g settings.Group) error {
	return r.record("update group %s", g.URI)
}

func (r *recorder) CreateUser(_ context.Context, u settings.User) error {
	return r.record("create user %s %v", u.MCPTTUserID, u.ImplicitAffiliations)
}

func (r *recorder) UpdateUser(_ context.Context, u settings.User) error {
	return r.record("update user %s %v", u.MCPTTUserID, u.ImplicitAffiliations)
}

func (r *recorder) DeleteUser(_ context.Context, id string) error {
	return r.record("delete user %s", id)
}

func (r *recorder) CreateGroup(_ context.Context, g settings.Group) error {
	return r.record("create group %s", g.URI)
}

func (r *recorder) DeleteGroup(_ context.Context, uri string) error {
	return r.record("delete group %s", uri)
}

func (r *recorder) PutMember(_ context.Context, group string, e settings.Entry) error {
	return r.record("put member %s %s", group, e.URI)
}

func (r *recorder) DeleteMember(_ context.Context, group, id string) error {
	return r.record("delete member %s %s", group, id)
}

// example are alice, bob and carol, members of fire, affiliated to it on registration: alice required, bob of a tel
// IMPU too, carol receive-only.
func example(t *testing.T) settings.Settings {
	t.Helper()

	const fire = "sip:fire@mcptt.example.com"

	return settings.Settings{
		System: settings.System{
			ParticipatingPSI: "sip:mcptt-participating@mcptt.example.com", ControllingPSI: "sip:mcptt-controlling@mcptt.example.com",
		},
		ServiceConfiguration: settings.ServiceConfiguration{
			Domain:    "mcptt.example.com",
			OnNetwork: settings.OnNetwork{FCTimersCounters: settings.FCTimersCounters{T7: new(2 * time.Second)}},
		},
		Users: []settings.User{
			{MCPTTUserID: "sip:alice@mcptt.example.com", IMPUs: []string{"sip:alice@ims.example.com"}, ImplicitAffiliations: []string{fire}},
			{
				MCPTTUserID: "sip:bob@mcptt.example.com", IMPUs: []string{"sip:bob@ims.example.com", "tel:+15555550102"},
				ImplicitAffiliations: []string{fire}, MaxAffiliationsN2: new(4), MaxSimultaneousCallsN6: new(2),
			},
			{MCPTTUserID: "sip:carol@mcptt.example.com", IMPUs: []string{"sip:carol@ims.example.com"}, ImplicitAffiliations: []string{fire}},
		},
		Groups: []settings.Group{{
			URI: fire, InviteMembers: true, MaxParticipants: 100, T4: new(30 * time.Second), TNG3: 10 * time.Minute,
			TNG1: 5 * time.Second, TNG1Action: settings.Proceed,
			List: settings.List{Entries: []settings.Entry{
				{URI: "sip:alice@mcptt.example.com", UserPriority: 2, Required: true},
				{URI: "sip:bob@mcptt.example.com", UserPriority: 1},
				{URI: "sip:carol@mcptt.example.com", ReceiveOnly: true},
			}},
		}},
	}
}

func newLive(t *testing.T) (*settings.Live, *recorder) {
	t.Helper()

	r := &recorder{}

	l, err := settings.NewLive(r, example(t))
	if err != nil {
		t.Fatal(err)
	}

	return l, r
}

func mustURI(t *testing.T, s string) sip.URI {
	t.Helper()

	u, err := sip.ParseURI(s)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

// err2 and err3 are the errors of calls of two and three results.
func err2[T any](_ T, err error) error { return err }

func err3[T, U any](_ T, _ U, err error) error { return err }

// changeError is err, a ChangeError of kind and of the problems want, each as its Error.
func changeError(t *testing.T, err, kind error, want ...string) {
	t.Helper()

	var ce *settings.ChangeError
	if !errors.As(err, &ce) || !errors.Is(err, kind) {
		t.Fatalf("error %v, want a ChangeError of %v", err, kind)
	}

	got := make([]string, len(ce.Problems))
	for i := range ce.Problems {
		got[i] = ce.Problems[i].Error()
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("problems\n%q\nwant\n%q", got, want)
	}
}

func TestNewLiveInvalid(t *testing.T) {
	s := example(t)
	s.System.ParticipatingPSI = ""

	if _, err := settings.NewLive(&recorder{}, s); err == nil {
		t.Error("invalid settings served")
	}
}

// A change is for the calls after it: a call holding the snapshot before keeps it. Get has the settings as written,
// Current their snapshot, the defaults applied.
func TestChange(t *testing.T) {
	ctx := t.Context()
	l, r := newLive(t)
	held := l.Current()

	dave := settings.User{
		MCPTTUserID: "sip:dave@mcptt.example.com", IMPUs: []string{"sip:dave@ims.example.com"},
		ImplicitAffiliations: []string{"sip:fire@MCPTT.example.com"},
	}

	// An affiliation to a group of which dave is not a member yet.
	changeError(t, err2(l.CreateUser(ctx, dave)), settings.ErrConflict,
		"implicit-affiliations[0]: sip:fire@mcptt.example.com is a group sip:dave@mcptt.example.com is not a member of")

	dave.ImplicitAffiliations = nil
	if err := err2(l.CreateUser(ctx, dave)); err != nil {
		t.Fatal(err)
	}

	if err := err3(l.PutMember(ctx, "sip:fire@MCPTT.example.com", settings.Entry{URI: "sip:dave@MCPTT.example.com"})); err != nil {
		t.Fatal(err)
	}

	dave.MCPTTUserID = "sip:dave@MCPTT.example.com"
	dave.ImplicitAffiliations = []string{"sip:fire@MCPTT.example.com"}

	if err := err2(l.UpdateUser(ctx, dave)); err != nil {
		t.Fatal(err)
	}

	// The store and Get have the identities as their user and group have them.
	want := []string{
		"create user sip:dave@mcptt.example.com []",
		"put member sip:fire@mcptt.example.com sip:dave@mcptt.example.com",
		"update user sip:dave@mcptt.example.com [sip:fire@mcptt.example.com]",
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("store calls\n%q\nwant\n%q", r.calls, want)
	}

	s := l.Get()
	if u := s.Users[3]; u.MCPTTUserID != "sip:dave@mcptt.example.com" || u.ImplicitAffiliations[0] != "sip:fire@mcptt.example.com" {
		t.Errorf("dave %+v", u)
	}

	if e := s.Groups[0].List.Entries[3]; e.URI != "sip:dave@mcptt.example.com" {
		t.Errorf("dave's entry %+v", e)
	}

	// As written: the defaults are the snapshot's.
	if s.Users[3].MaxAffiliationsN2 != nil {
		t.Errorf("N2 %v", *s.Users[3].MaxAffiliationsN2)
	}

	davesID, fire := mustURI(t, "sip:dave@mcptt.example.com"), mustURI(t, "sip:fire@mcptt.example.com")

	if held.User(davesID) != nil || held.Group(fire).Entry(davesID) != nil {
		t.Error("the held snapshot changed")
	}

	if u := l.Current().User(davesID); u == nil || len(u.ImplicitGroups()) != 1 || l.Current().Group(fire).Entry(davesID) == nil {
		t.Errorf("current snapshot: dave %+v", u)
	}
}

// A change with an error of its own is invalid; one that only conflicts with the rest, a conflict; the errors of
// the documents it conflicts with name them.
func TestRefused(t *testing.T) {
	ctx := t.Context()
	l, r := newLive(t)
	before := l.Current()

	changeError(t, err2(l.CreateUser(ctx, settings.User{MCPTTUserID: "sip:fire@mcptt.example.com"})), settings.ErrInvalid,
		"impus: needs at least one IMPU",
		"the group sip:fire@mcptt.example.com: uri: sip:fire@mcptt.example.com is also the user sip:fire@mcptt.example.com")

	changeError(t, err2(l.CreateUser(ctx, settings.User{MCPTTUserID: "sip:alice@mcptt.example.com", IMPUs: []string{"tel:+1"}})),
		settings.ErrConflict, "mcptt-user-id: sip:alice@mcptt.example.com is also the user sip:alice@mcptt.example.com")

	// An IMPU of a group's URI: found at the group, validated after the users.
	changeError(t, err2(l.CreateUser(ctx, settings.User{MCPTTUserID: "sip:dave@mcptt.example.com", IMPUs: []string{"sip:fire@mcptt.example.com"}})),
		settings.ErrConflict, "the group sip:fire@mcptt.example.com: uri: sip:fire@mcptt.example.com is also an IMPU of sip:dave@mcptt.example.com")

	changeError(t, l.UpdateServiceConfiguration(ctx, settings.ServiceConfiguration{Domain: "-"}), settings.ErrInvalid,
		`domain: "-" is not a domain name`, "on-network.fc-timers-counters.T7-floor-idle: is required: it has no default (TS 24.380 Table 11.1.3-1)")

	changeError(t, err3(l.PutMember(ctx, "sip:fire@mcptt.example.com", settings.Entry{URI: "sip:x@mcptt.example.com", UserPriority: 256})),
		settings.ErrInvalid, "user-priority: 256 is not within 0 and 255 (TS 24.481 §7.2.4.2)",
		"uri: sip:x@mcptt.example.com is not a configured user")

	// carol is affiliated to fire, of which she would no longer be a member.
	changeError(t, err2(l.DeleteMember(ctx, "sip:fire@mcptt.example.com", "sip:carol@mcptt.example.com")), settings.ErrConflict,
		"the user sip:carol@mcptt.example.com: implicit-affiliations[0]: sip:fire@mcptt.example.com is a group sip:carol@mcptt.example.com is not a member of")

	changeError(t, l.DeleteUser(ctx, "sip:bob@mcptt.example.com"), settings.ErrConflict,
		"sip:bob@mcptt.example.com is a member of sip:fire@mcptt.example.com")

	changeError(t, l.DeleteGroup(ctx, "sip:fire@mcptt.example.com"), settings.ErrConflict,
		"sip:fire@mcptt.example.com is an implicit affiliation of sip:alice@mcptt.example.com, sip:bob@mcptt.example.com, sip:carol@mcptt.example.com")

	for _, err := range []error{
		err2(l.UpdateUser(ctx, settings.User{MCPTTUserID: "sip:x@mcptt.example.com"})),
		l.DeleteUser(ctx, "sip:x@mcptt.example.com"),
		err2(l.UpdateGroup(ctx, settings.Group{URI: "sip:x@mcptt.example.com"})),
		l.DeleteGroup(ctx, "not a URI"),
		err3(l.PutMember(ctx, "sip:x@mcptt.example.com", settings.Entry{URI: "sip:alice@mcptt.example.com"})),
		err2(l.DeleteMember(ctx, "sip:fire@mcptt.example.com", "sip:x@mcptt.example.com")),
	} {
		if !errors.Is(err, settings.ErrNotFound) {
			t.Errorf("of nothing to change: %v", err)
		}
	}

	if len(r.calls) != 0 || l.Current() != before {
		t.Errorf("refused changes made: %q", r.calls)
	}
}

// A change the store fails to save does not take effect.
func TestStoreFails(t *testing.T) {
	l, r := newLive(t)
	before := l.Current()
	r.err = errors.New("disk full")

	err := l.UpdateSystem(t.Context(), settings.System{ParticipatingPSI: "sip:p@mcptt.example.com", ControllingPSI: "sip:c@mcptt.example.com"})
	if err == nil || !strings.Contains(err.Error(), "disk full") || errors.Is(err, settings.ErrInvalid) {
		t.Errorf("error %v", err)
	}

	if l.Current() != before {
		t.Error("the change took effect")
	}
}

// A member put again keeps its place; deleted members and users go, and the groups once no user is affiliated.
func TestMembers(t *testing.T) {
	ctx := t.Context()
	l, r := newLive(t)

	if err := err3(l.PutMember(ctx, "sip:fire@mcptt.example.com", settings.Entry{URI: "sip:alice@mcptt.example.com", UserPriority: 5})); err != nil {
		t.Fatal(err)
	}

	if e := l.Get().Groups[0].List.Entries; len(e) != 3 || e[0].UserPriority != 5 || e[0].Required {
		t.Errorf("entries %+v", e)
	}

	carol := example(t).Users[2]
	carol.ImplicitAffiliations = nil

	steps := []func() error{
		func() error { return err2(l.UpdateUser(ctx, carol)) },
		func() error {
			return err2(l.DeleteMember(ctx, "sip:fire@mcptt.example.com", "sip:carol@MCPTT.example.com"))
		},
		func() error { return l.DeleteUser(ctx, "sip:carol@mcptt.example.com") },
	}

	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}

	if s := l.Get(); len(s.Users) != 2 || len(s.Groups[0].List.Entries) != 2 {
		t.Errorf("settings %+v", s)
	}

	want := "delete member sip:fire@mcptt.example.com sip:carol@mcptt.example.com"
	if r.calls[2] != want {
		t.Errorf("store call %q, want %q", r.calls[2], want)
	}
}

// Changes and reads at once (go test -race).
func TestConcurrent(t *testing.T) {
	ctx := t.Context()
	l, _ := newLive(t)

	var wg sync.WaitGroup

	for i := range 8 {
		wg.Go(func() {
			u := settings.User{
				MCPTTUserID: fmt.Sprintf("sip:u%d@mcptt.example.com", i),
				IMPUs:       []string{fmt.Sprintf("sip:u%d@ims.example.com", i)},
			}
			if err := err2(l.CreateUser(ctx, u)); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			_ = l.Current().ServerTimers(l.Current().Group(mustURI(t, "sip:fire@mcptt.example.com")))
			_ = len(l.Get().Users)
		})
	}

	wg.Wait()

	if n := len(l.Get().Users); n != 11 {
		t.Errorf("%d users", n)
	}

	if l.Current().ServiceConfiguration().OnNetwork.FCTimersCounters.T7 == nil || *l.Current().ServiceConfiguration().OnNetwork.FCTimersCounters.T7 != 2*time.Second {
		t.Error("T7")
	}
}

// The server serves MaxUsers users at most.
func TestMaxUsers(t *testing.T) {
	s := example(t)
	for i := len(s.Users); i < settings.MaxUsers; i++ {
		s.Users = append(s.Users, settings.User{
			MCPTTUserID: fmt.Sprintf("sip:u%d@mcptt.example.com", i), IMPUs: []string{fmt.Sprintf("sip:u%d@ims.example.com", i)},
		})
	}

	l, err := settings.NewLive(&recorder{}, s)
	if err != nil {
		t.Fatal(err)
	}

	changeError(t, err2(l.CreateUser(t.Context(), settings.User{MCPTTUserID: "sip:x@mcptt.example.com", IMPUs: []string{"tel:+1"}})),
		settings.ErrConflict, "1001 users: the server serves at most 1000")
}

// A removal of a member, of no body, that makes its group invalid is a conflict with the group.
func TestDeleteMemberConflict(t *testing.T) {
	ctx := t.Context()
	l, _ := newLive(t)

	fire := example(t).Groups[0]
	fire.MinimumToStart = 2

	if err := err2(l.UpdateGroup(ctx, fire)); err != nil {
		t.Fatal(err)
	}

	changeError(t, err2(l.DeleteMember(ctx, "sip:fire@mcptt.example.com", "sip:bob@mcptt.example.com")), settings.ErrConflict,
		"the group sip:fire@mcptt.example.com: on-network-minimum-number-to-start: 2 is not within 0 and the 1 members to invite",
		"the user sip:bob@mcptt.example.com: implicit-affiliations[0]: sip:fire@mcptt.example.com is a group sip:bob@mcptt.example.com is not a member of")
}
