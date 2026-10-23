package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/mcptt"
	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

type mcpttEnv struct {
	t    *testing.T
	h    http.Handler
	db   *db.DB
	live *mcpttsettings.Live
	path string
}

// newMCPTTEnv is the API of the MCPTT settings of a new database, its seed.
func newMCPTTEnv(t *testing.T) *mcpttEnv {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ims.db")

	d, err := db.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	l, err := mcptt.NewSettings(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}

	h := NewHandler(Config{
		MCPTT:       l,
		MCPTTStatus: &MCPTTStatus{Mode: "ims", SIP: []string{"127.0.0.1:5090"}, MediaControl: "127.0.0.1:21000"},
		Logger:      slog.New(slog.DiscardHandler),
	})

	return &mcpttEnv{t: t, h: h, db: d, live: l, path: path}
}

// do sends a request of body, if any, and returns the status and the result or error of the response.
func (e *mcpttEnv) do(method, path, body string) (int, json.RawMessage) {
	e.t.Helper()

	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}

	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequestWithContext(e.t.Context(), method, path, r))

	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		e.t.Fatalf("%s %s: %d %q: %v", method, path, rec.Code, rec.Body.String(), err)
	}

	if resp.Error != "" {
		b, _ := json.Marshal(resp.Error)
		return rec.Code, b
	}

	return rec.Code, resp.Result
}

// ok sends a request that must succeed with status, and decodes its result into v, if any.
func (e *mcpttEnv) ok(status int, method, path, body string, v any) {
	e.t.Helper()

	code, res := e.do(method, path, body)
	if code != status {
		e.t.Fatalf("%s %s: %d %s, want %d", method, path, code, res, status)
	}

	if v != nil {
		if err := json.Unmarshal(res, v); err != nil {
			e.t.Fatal(err)
		}
	}
}

// fails sends a request that must fail with status and the error message want.
func (e *mcpttEnv) fails(status int, method, path, body, want string) {
	e.t.Helper()

	code, res := e.do(method, path, body)

	var msg string
	if err := json.Unmarshal(res, &msg); err != nil {
		e.t.Fatalf("%s %s: %d %s, not an error", method, path, code, res)
	}

	if code != status || msg != want {
		e.t.Errorf("%s %s: %d %q\nwant %d %q", method, path, code, msg, status, want)
	}
}

const (
	alice = `{"mcptt_id": "sip:alice@mcptt.example.com", "impus": ["sip:alice@ims.example.com"]}`
	bob   = `{"mcptt_id": "sip:bob@mcptt.example.com", "impus": ["sip:bob@ims.example.com", "tel:+15555550102"],
		"max_simultaneous_calls_n6": 2}`
	fire = `{"group_id": "sip:fire@mcptt.example.com", "invite_members": true, "maximum_duration_ms": 600000,
		"hang_timer_ms": 30000, "members": [{"mcptt_id": "sip:alice@mcptt.example.com", "user_priority": 2},
		{"mcptt_id": "sip:bob@MCPTT.example.com", "recvonly": true}]}`
)

// provision provisions alice and bob, members of fire, alice affiliated to it.
func (e *mcpttEnv) provision() {
	e.t.Helper()

	e.ok(http.StatusCreated, "POST", "/api/v1/mcptt/users", alice, nil)
	e.ok(http.StatusCreated, "POST", "/api/v1/mcptt/users", bob, nil)
	e.ok(http.StatusCreated, "POST", "/api/v1/mcptt/groups", fire, nil)
	e.ok(http.StatusOK, "PUT", "/api/v1/mcptt/users/sip:alice@mcptt.example.com",
		`{"impus": ["sip:alice@ims.example.com"], "implicit_affiliations": ["sip:fire@mcptt.example.com"]}`, nil)
}

// A system provisioned in order, as stored: the absent optional fields absent, the identities as their user and
// group have them; and as the database has it.
func TestProvision(t *testing.T) {
	e := newMCPTTEnv(t)

	var sys MCPTTSystem

	e.ok(http.StatusOK, "PUT", "/api/v1/mcptt/system",
		`{"participating_psi": "sip:p@mcptt.example.com", "controlling_psi": "sip:c@mcptt.example.com"}`, &sys)

	if sys.ParticipatingPSI != "sip:p@mcptt.example.com" {
		t.Errorf("system %+v", sys)
	}

	var sc MCPTTServiceConfiguration

	e.ok(http.StatusOK, "PUT", "/api/v1/mcptt/service-configuration",
		`{"domain": "mcptt.example.com", "transmit_time_limit_ms": 20000, "fc_timers_counters": {"t7_floor_idle_ms": 1500, "c7_floor_idle": 0}}`, &sc)

	if *sc.TransmitTimeLimitMS != 20000 || *sc.FCTimersCounters.T7FloorIdleMS != 1500 || *sc.FCTimersCounters.C7FloorIdle != 0 ||
		sc.FCTimersCounters.T1EndOfRTPMediaMS != nil || sc.NumLevelsPriorityHierarchy != nil {
		t.Errorf("service configuration %+v", sc)
	}

	if timers := e.live.Current().ServiceConfiguration().OnNetwork; timers.FCTimersCounters.T7.Milliseconds() != 1500 {
		t.Errorf("snapshot %+v", timers)
	}

	e.provision()

	var u MCPTTUser

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/users/sip%3Aalice%40mcptt.example.com", "", &u)

	want := MCPTTUser{
		MCPTTID: "sip:alice@mcptt.example.com", IMPUs: []string{"sip:alice@ims.example.com"},
		ImplicitAffiliations: []string{"sip:fire@mcptt.example.com"},
	}
	if !reflect.DeepEqual(u, want) {
		t.Errorf("alice %+v, want %+v", u, want)
	}

	var g MCPTTGroup

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/groups/sip:fire@MCPTT.example.com", "", &g)

	if len(g.Members) != 2 || g.Members[1] != (MCPTTMember{MCPTTID: "sip:bob@mcptt.example.com", RecvOnly: true}) ||
		*g.HangTimerMS != 30000 || g.MaximumDurationMS != 600000 || len(g.PreferredVoiceEncodings) != 0 {
		t.Errorf("fire %+v", g)
	}

	var users MCPTTUsers

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/users?per_page=1&page=2", "", &users)

	if users.TotalCount != 2 || len(users.Items) != 1 || users.Items[0].MCPTTID != "sip:bob@mcptt.example.com" ||
		*users.Items[0].MaxSimultaneousCallsN6 != 2 || users.Items[0].ImplicitAffiliations == nil {
		t.Errorf("users %+v", users)
	}

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/users?page=3", "", &users)

	if len(users.Items) != 0 {
		t.Errorf("page 3: %+v", users)
	}

	var groups MCPTTGroups

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/groups", "", &groups)

	if groups.TotalCount != 1 || groups.Items[0].GroupID != "sip:fire@mcptt.example.com" {
		t.Errorf("groups %+v", groups)
	}

	stored, err := e.db.GetMCPTTSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(stored, e.live.Get()) {
		t.Errorf("stored\n%+v\nserved\n%+v", stored, e.live.Get())
	}
}

// A creation answers with the Location of what it created (RFC 9110 §15.3.2).
func TestLocation(t *testing.T) {
	e := newMCPTTEnv(t)

	for _, tc := range []struct{ path, body, want string }{
		{"/api/v1/mcptt/users", alice, "/api/v1/mcptt/users/sip:alice@mcptt.example.com"},
		{"/api/v1/mcptt/groups", `{"group_id": "sip:fire@mcptt.example.com", "maximum_duration_ms": 600000}`, "/api/v1/mcptt/groups/sip:fire@mcptt.example.com"},
	} {
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "POST", tc.path, strings.NewReader(tc.body)))

		if rec.Code != http.StatusCreated || rec.Header().Get("Location") != tc.want {
			t.Errorf("POST %s: %d, Location %q", tc.path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// A member is added after the others, replaced where it is, and removed; the user then, once no group has it.
func TestMembers(t *testing.T) {
	e := newMCPTTEnv(t)
	e.provision()
	e.ok(http.StatusCreated, "POST", "/api/v1/mcptt/users", `{"mcptt_id": "sip:carol@mcptt.example.com", "impus": ["sip:carol@ims.example.com"]}`, nil)

	var g MCPTTGroup

	e.ok(http.StatusCreated, "PUT", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com/members/sip:carol@mcptt.example.com",
		`{"user_priority": 7}`, &g)
	e.ok(http.StatusOK, "PUT", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com/members/sip:alice@mcptt.example.com",
		`{"mcptt_id": "sip:alice@MCPTT.example.com", "affiliation_required": true}`, &g)

	want := []MCPTTMember{
		{MCPTTID: "sip:alice@mcptt.example.com", AffiliationRequired: true},
		{MCPTTID: "sip:bob@mcptt.example.com", RecvOnly: true},
		{MCPTTID: "sip:carol@mcptt.example.com", UserPriority: 7},
	}
	if !reflect.DeepEqual(g.Members, want) {
		t.Errorf("members %+v", g.Members)
	}

	e.fails(http.StatusConflict, "DELETE", "/api/v1/mcptt/users/sip:carol@mcptt.example.com", "",
		"sip:carol@mcptt.example.com is a member of sip:fire@mcptt.example.com")
	e.ok(http.StatusOK, "DELETE", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com/members/sip:carol@mcptt.example.com", "", &g)
	e.ok(http.StatusOK, "DELETE", "/api/v1/mcptt/users/sip:carol@mcptt.example.com", "", nil)
	e.fails(http.StatusNotFound, "GET", "/api/v1/mcptt/users/sip:carol@mcptt.example.com", "", "user not found")

	e.fails(http.StatusConflict, "DELETE", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com", "",
		"sip:fire@mcptt.example.com is an implicit affiliation of sip:alice@mcptt.example.com")
	e.ok(http.StatusOK, "PUT", "/api/v1/mcptt/users/sip:alice@mcptt.example.com", alice, nil)
	e.ok(http.StatusOK, "DELETE", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com", "", nil)
}

// The problems of a refused change are at the fields of the request body: 400 of the body itself, 409 of what it
// conflicts with, 404 of a path of nothing.
func TestRefused(t *testing.T) {
	e := newMCPTTEnv(t)
	e.provision()

	for _, tc := range []struct {
		status             int
		method, path, body string
		want               string
	}{
		{400, "POST", "/api/v1/mcptt/users", `{"mcptt_id": "sip:carol@mcptt.example.com", "impus": []}`, "impus: needs at least one IMPU"},
		{400, "POST", "/api/v1/mcptt/users", `{"mcptt_id": "sip:carol@mcptt.example.com", "impu": []}`, "Invalid request data"},
		{
			409, "POST", "/api/v1/mcptt/users", `{"mcptt_id": "sip:alice@MCPTT.example.com", "impus": ["tel:+1"]}`,
			"mcptt_id: sip:alice@MCPTT.example.com is also the user sip:alice@mcptt.example.com",
		},
		{
			409, "PUT", "/api/v1/mcptt/users/sip:bob@mcptt.example.com", `{"impus": ["sip:mcptt-participating@mcptt.example.com"]}`,
			"impus[0]: sip:mcptt-participating@mcptt.example.com is also the participating PSI",
		},
		{
			400, "PUT", "/api/v1/mcptt/users/sip:bob@mcptt.example.com", `{"mcptt_id": "sip:carol@mcptt.example.com", "impus": ["tel:+1"]}`,
			"mcptt_id: sip:carol@mcptt.example.com is not the MCPTT ID of the path, which a user keeps",
		},
		{
			400, "PUT", "/api/v1/mcptt/users/sip:bob@mcptt.example.com", `{"impus": ["tel:+1"], "max_simultaneous_calls_n6": 0}`,
			"max_simultaneous_calls_n6: 0 is not positive",
		},
		{404, "PUT", "/api/v1/mcptt/users/sip:x@mcptt.example.com", `{"impus": ["tel:+1"]}`, "user not found"},
		{404, "DELETE", "/api/v1/mcptt/users/sip:x@mcptt.example.com", "", "user not found"},
		{
			409, "POST", "/api/v1/mcptt/groups", `{"group_id": "sip:police@mcptt.example.com", "invite_members": true,
				"maximum_duration_ms": 1000, "members": [{"mcptt_id": "sip:x@mcptt.example.com"}]}`,
			"members[0].mcptt_id: sip:x@mcptt.example.com is not a configured user",
		},
		{
			400, "POST", "/api/v1/mcptt/groups", `{"group_id": "sip:police@mcptt.example.com", "maximum_duration_ms": 1000,
				"members": [{"mcptt_id": "sip:alice@mcptt.example.com", "user_priority": 256}]}`,
			"members[0].user_priority: 256 is not within 0 and 255 (TS 24.481 §7.2.4.2)",
		},
		{
			400, "POST", "/api/v1/mcptt/groups", `{"group_id": "sip:police@mcptt.example.com", "invite_members": false, "maximum_duration_ms": 1000}`,
			"invite_members: is false, of a chat group (TS 24.481 §7.2.7): only prearranged groups are supported",
		},
		{400, "POST", "/api/v1/mcptt/users", alice + ` {}`, "Invalid request data"},
		{
			400, "POST", "/api/v1/mcptt/users", `{"mcptt_id": "sip:x@mcptt.example.com", "impus": ["` + strings.Repeat("x", maxBody) + `"]}`,
			"Invalid request data",
		},
		{
			400, "POST", "/api/v1/mcptt/groups", `{"group_id": "sip:police@mcptt.example.com", "invite_members": true, "maximum_duration_ms": 9223372036854775807}`,
			"maximum_duration_ms: 9223372036854775807 is out of range",
		},
		{
			400, "PUT", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com/members/sip:bob@mcptt.example.com", `{"user_priority": -1}`,
			"user_priority: -1 is not within 0 and 255 (TS 24.481 §7.2.4.2)",
		},
		{
			409, "PUT", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com/members/sip:x@mcptt.example.com", `{}`,
			"mcptt_id: sip:x@mcptt.example.com is not a configured user",
		},
		{
			// alice, affiliated to fire, would no longer be a member of it.
			409, "DELETE", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com/members/sip:alice@mcptt.example.com", "",
			"the user sip:alice@mcptt.example.com: implicit_affiliations[0]: sip:fire@mcptt.example.com is a group sip:alice@mcptt.example.com is not a member of",
		},
		{404, "DELETE", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com/members/sip:x@mcptt.example.com", "", "member not found"},
		{404, "GET", "/api/v1/mcptt/groups/sip:x@mcptt.example.com", "", "group not found"},
		{
			400, "PUT", "/api/v1/mcptt/service-configuration", `{"domain": "mcptt.example.com", "fc_timers_counters": {"t1_end_of_rtp_media_ms": 7000}}`,
			"fc_timers_counters.t7_floor_idle_ms: is required: it has no default (TS 24.380 Table 11.1.3-1); " +
				"fc_timers_counters.t1_end_of_rtp_media_ms: 7s is above its maximum of 6s (TS 24.380 Table 11.1.3-1)",
		},
		{
			409, "PUT", "/api/v1/mcptt/system", `{"participating_psi": "sip:fire@mcptt.example.com", "controlling_psi": "sip:c@mcptt.example.com"}`,
			"the group sip:fire@mcptt.example.com: group_id: sip:fire@mcptt.example.com is also the participating PSI",
		},
		{400, "GET", "/api/v1/mcptt/groups?per_page=101", "", "per_page must be an integer between 1 and 100"},
	} {
		e.fails(tc.status, tc.method, tc.path, tc.body, tc.want)
	}
}

// User parts are case-sensitive (RFC 3261 §19.1.4): sip:ALICE is another user, whose IMPU alice's may be too (TS
// 23.280 §8.3.1).
func TestCaseSensitiveUser(t *testing.T) {
	e := newMCPTTEnv(t)
	e.provision()
	e.ok(http.StatusCreated, "POST", "/api/v1/mcptt/users", `{"mcptt_id": "sip:ALICE@mcptt.example.com", "impus": ["sip:alice@ims.example.com"]}`, nil)
}

// A change that cannot be saved answers 500, and does not take effect.
func TestSaveFails(t *testing.T) {
	e := newMCPTTEnv(t)

	if err := e.db.Close(); err != nil {
		t.Fatal(err)
	}

	e.fails(http.StatusInternalServerError, "POST", "/api/v1/mcptt/users", alice, "Failed to create the user")

	if len(e.live.Get().Users) != 0 {
		t.Error("the change took effect")
	}
}

func TestMCPTTStatus(t *testing.T) {
	e := newMCPTTEnv(t)

	var s Status

	e.ok(http.StatusOK, "GET", "/api/v1/status", "", &s)

	if m := s.MCPTT; m == nil || m.Mode != "ims" || len(m.SIP) != 1 || m.MediaControl != "127.0.0.1:21000" || s.Version == "" {
		t.Errorf("status %+v", s)
	}
}

// Without the MCPTT AS, its paths are not found, and the status has none.
func TestMCPTTDisabled(t *testing.T) {
	h := NewHandler(Config{Logger: slog.New(slog.DiscardHandler)})

	for _, path := range []string{"/api/v1/mcptt/users", "/api/v1/mcptt/system"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))

		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "MCPTT is not enabled") {
			t.Errorf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/status", nil))

	if strings.Contains(rec.Body.String(), "mcptt") {
		t.Errorf("status %s", rec.Body.String())
	}
}

// The fields of the paths of the settings.
func TestField(t *testing.T) {
	for in, want := range map[string]string{
		"":                                    "",
		"mcptt-user-id":                       "mcptt_id",
		"implicit-affiliations[2]":            "implicit_affiliations[2]",
		"uri":                                 "group_id",
		"list.entries[3].uri":                 "members[3].mcptt_id",
		"list.entries[3].on-network-recvonly": "members[3].recvonly",
		"on-network-hang-timer":               "hang_timer_ms",
		"on-network-invite-members":           "invite_members",
		"preferred-voice-encodings[1]":        "preferred_voice_encodings[1]",
		"on-network.fc-timers-counters.C7-floor-idle": "fc_timers_counters.c7_floor_idle",
		"on-network.transmit-time.time-limit":         "transmit_time_limit_ms",
		"participating-psi":                           "participating_psi",
	} {
		if got := mcpttField(in); got != want {
			t.Errorf("field(%q) = %q, want %q", in, got, want)
		}
	}
}

// stored checks that the settings the database has are those the API serves.
func (e *mcpttEnv) stored() {
	e.t.Helper()

	stored, err := e.db.GetMCPTTSettings(e.t.Context())
	if err != nil {
		e.t.Fatal(err)
	}

	if !reflect.DeepEqual(normal(stored), normal(e.live.Get())) {
		e.t.Errorf("stored\n%+v\nserved\n%+v", stored, e.live.Get())
	}
}

// normal is s of its empty lists nil, as the database has them.
func normal(s mcpttsettings.Settings) mcpttsettings.Settings {
	s.Users = slices.Clone(s.Users)
	for i := range s.Users {
		u := &s.Users[i]
		if len(u.IMPUs) == 0 {
			u.IMPUs = nil
		}

		if len(u.ImplicitAffiliations) == 0 {
			u.ImplicitAffiliations = nil
		}
	}

	s.Groups = slices.Clone(s.Groups)
	for i := range s.Groups {
		g := &s.Groups[i]
		if len(g.Encodings) == 0 {
			g.Encodings = nil
		}

		if len(g.List.Entries) == 0 {
			g.List.Entries = nil
		}
	}

	if len(s.Users) == 0 {
		s.Users = nil
	}

	if len(s.Groups) == 0 {
		s.Groups = nil
	}

	return s
}

// After every kind of change, the database has the settings the API serves.
func TestStoredAsServed(t *testing.T) {
	e := newMCPTTEnv(t)

	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/mcptt/system", `{"participating_psi": "sip:p@mcptt.example.com", "controlling_psi": "sip:c@mcptt.example.com"}`},
		{"PUT", "/api/v1/mcptt/service-configuration", `{"domain": "mcptt.example.com", "fc_timers_counters": {"t7_floor_idle_ms": 2000}}`},
		{"POST", "/api/v1/mcptt/users", alice},
		{"POST", "/api/v1/mcptt/users", bob},
		{"POST", "/api/v1/mcptt/users", `{"mcptt_id": "sip:carol@mcptt.example.com", "impus": ["sip:carol@ims.example.com"]}`},
		{"POST", "/api/v1/mcptt/groups", fire},
		{"PUT", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com/members/sip:carol@MCPTT.example.com", `{"recvonly": true}`},
		{"PUT", "/api/v1/mcptt/users/sip:carol@mcptt.example.com", `{"impus": ["tel:+15550103"], "implicit_affiliations": ["sip:fire@MCPTT.example.com"]}`},
		{"PUT", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com", `{"maximum_duration_ms": 60000, "preferred_voice_encodings": ["AMR-WB"],
			"members": [{"mcptt_id": "sip:carol@mcptt.example.com"}, {"mcptt_id": "sip:alice@mcptt.example.com", "user_priority": 3}]}`},
		{"DELETE", "/api/v1/mcptt/users/sip:bob@mcptt.example.com", ""},
		{"DELETE", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com/members/sip:alice@mcptt.example.com", ""},
		{"PUT", "/api/v1/mcptt/users/sip:carol@mcptt.example.com", `{"impus": ["tel:+15550103"]}`},
		{"DELETE", "/api/v1/mcptt/groups/sip:fire@mcptt.example.com", ""},
	} {
		if code, res := e.do(c.method, c.path, c.body); code/100 != 2 {
			t.Fatalf("%s %s: %d %s", c.method, c.path, code, res)
		}

		e.stored()
	}
}

// Changes at once, through the API, are each applied and stored.
func TestConcurrentChanges(t *testing.T) {
	e := newMCPTTEnv(t)
	e.provision()

	var wg sync.WaitGroup

	for i := range 20 {
		wg.Go(func() {
			id := fmt.Sprintf("sip:u%d@mcptt.example.com", i)
			body := fmt.Sprintf(`{"mcptt_id": %q, "impus": ["sip:u%d@ims.example.com"]}`, id, i)

			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/mcptt/users", strings.NewReader(body)))

			if rec.Code != http.StatusCreated {
				t.Errorf("POST %s: %d", id, rec.Code)
				return
			}

			rec = httptest.NewRecorder()
			e.h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "PUT",
				"/api/v1/mcptt/groups/sip:fire@mcptt.example.com/members/"+id, strings.NewReader(`{}`)))

			if rec.Code != http.StatusCreated {
				t.Errorf("PUT member %s: %d", id, rec.Code)
			}
		})
	}

	wg.Wait()

	if s := e.live.Get(); len(s.Users) != 22 || len(s.Groups[0].List.Entries) != 22 {
		t.Errorf("%d users, %d members", len(s.Users), len(s.Groups[0].List.Entries))
	}

	e.stored()
}

// A "/" of the user part of an MCPTT ID is percent-encoded in the path, which it is the identity of.
func TestEncodedPath(t *testing.T) {
	e := newMCPTTEnv(t)
	e.ok(http.StatusCreated, "POST", "/api/v1/mcptt/users", `{"mcptt_id": "sip:a/b@mcptt.example.com", "impus": ["sip:ab@ims.example.com"]}`, nil)

	var u MCPTTUser

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/users/sip:a%2Fb@mcptt.example.com", "", &u)

	if u.MCPTTID != "sip:a/b@mcptt.example.com" {
		t.Errorf("user %+v", u)
	}

	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/mcptt/users/sip:a/b@mcptt.example.com", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("unencoded /: %d", rec.Code)
	}
}

// unusedStore is the store of settings no change reaches.
type unusedStore struct{ mcpttsettings.Store }

// The server serves settings.MaxUsers users at most: one more is a conflict.
func TestMaxUsersThroughAPI(t *testing.T) {
	e := newMCPTTEnv(t)

	s := e.live.Get()
	for i := range mcpttsettings.MaxUsers {
		s.Users = append(s.Users, mcpttsettings.User{
			MCPTTUserID: fmt.Sprintf("sip:u%d@mcptt.example.com", i), IMPUs: []string{fmt.Sprintf("sip:u%d@ims.example.com", i)},
		})
	}

	// A refused change is not stored: the store is never called.
	l, err := mcpttsettings.NewLive(unusedStore{}, s)
	if err != nil {
		t.Fatal(err)
	}

	e.live, e.h = l, NewHandler(Config{MCPTT: l, Logger: slog.New(slog.DiscardHandler)})
	e.fails(http.StatusConflict, "POST", "/api/v1/mcptt/users", `{"mcptt_id": "sip:dave@mcptt.example.com", "impus": ["tel:+1"]}`,
		"1001 users: the server serves at most 1000")
}
