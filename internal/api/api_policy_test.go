package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
)

type fakePolicy PolicyStatus

func (f fakePolicy) PolicyStatus() PolicyStatus { return PolicyStatus(f) }

func getPolicy(t *testing.T, cfg Config) string {
	t.Helper()

	code, body := serve(t, cfg, http.MethodGet, "/api/v1/policy", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	return string(body)
}

func TestGetPolicy(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	n5 := settings.NewLive(&memStore{}, defaultSettings(), nil)
	if err := n5.UpdatePolicy(t.Context(), settings.Policy{Interface: settings.PolicyN5, PCFURI: "http://10.0.0.13:7777"}); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		settings *settings.Live
		policy   Policy
		want     string
	}{
		"none": {newFakeSettings(), nil, `{"interface":"none","status":{"interface":"none"}}`},
		"n5 before any request": {
			n5,
			fakePolicy{Interface: "n5", Endpoint: "http://10.0.0.13:7777", Notify: "10.0.0.5:7778"},
			`{"interface":"n5","n5":{"pcf_uri":"http://10.0.0.13:7777"},` +
				`"status":{"interface":"n5","endpoint":"http://10.0.0.13:7777","notify":"10.0.0.5:7778"}}`,
		},
		"n5": {
			n5,
			fakePolicy{
				Interface: "n5", Endpoint: "http://10.0.0.13:7777", Notify: "10.0.0.5:7778",
				Last: &PolicyResult{At: at, Reachable: true, Result: "404 APPLICATION_SESSION_CONTEXT_NOT_FOUND"},
			},
			`{"interface":"n5","n5":{"pcf_uri":"http://10.0.0.13:7777"},` +
				`"status":{"interface":"n5","endpoint":"http://10.0.0.13:7777","notify":"10.0.0.5:7778",` +
				`"last":{"at":"2026-10-05T12:00:00.000Z","reachable":true,"result":"404 APPLICATION_SESSION_CONTEXT_NOT_FOUND"}}}`,
		},
		"n5 not running": {
			n5, nil, `{"interface":"n5","n5":{"pcf_uri":"http://10.0.0.13:7777"},"status":{"interface":"none"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := getPolicy(t, Config{Settings: tc.settings, Policy: tc.policy}); got != `{"result":`+tc.want+`}` {
				t.Fatalf("policy = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestUpdatePolicy(t *testing.T) {
	s := newFakeSettings()
	cfg := Config{Settings: s}

	code, body := serve(t, cfg, http.MethodPut, "/api/v1/policy", `{"interface": "n5", "n5": {"pcf_uri": "https://pcf.example.org:7777"}}`)
	if code != http.StatusOK {
		t.Fatalf("PUT n5 = %d: %s", code, body)
	}

	if got := s.Get().Policy; got != (settings.Policy{Interface: settings.PolicyN5, PCFURI: "https://pcf.example.org:7777"}) {
		t.Fatalf("policy = %+v", got)
	}

	createPeer(t, cfg, pcrfPeer)

	if code, body := serve(t, cfg, http.MethodPut, "/api/v1/policy", `{"interface": "rx"}`); code != http.StatusOK {
		t.Fatalf("PUT rx = %d: %s", code, body)
	}

	if got := s.Get().Policy; got != (settings.Policy{Interface: settings.PolicyRx}) {
		t.Fatalf("policy = %+v, want rx", got)
	}

	id := s.Get().Peers[0].ID

	code, body = serve(t, cfg, http.MethodDelete, "/api/v1/diameter/peers/"+id, "")
	if code != http.StatusConflict || decodeError(t, body) != "rx requires a Diameter peer serving rx" {
		t.Fatalf("DELETE the rx peer = %d %s, want 409", code, body)
	}
}

func TestUpdatePolicyRejected(t *testing.T) {
	check := func(s settings.Settings) error {
		if s.Policy.HTTPS() {
			return settings.Invalidf("n5 over https requires n5.tls in the configuration file")
		}

		return nil
	}

	tests := []struct {
		name, body string
		code       int
		want       string
	}{
		{"malformed", `{"interface":`, 400, "Invalid request data"},
		{"unknown interface", `{"interface": "gx"}`, 400, "interface must be none, rx or n5"},
		{"no interface", `{}`, 400, "interface must be none, rx or n5"},
		{"n5 without pcf_uri", `{"interface": "n5"}`, 400, "n5.pcf_uri is required"},
		{"n5 with a bad pcf_uri", `{"interface": "n5", "n5": {"pcf_uri": "ftp://pcf"}}`, 400, "n5.pcf_uri must be http[s]://host[:port][/prefix]"},
		{"n5 on rx", `{"interface": "rx", "n5": {"pcf_uri": "http://pcf"}}`, 400, "n5 is only for the n5 interface"},
		{"against the configuration file", `{"interface": "n5", "n5": {"pcf_uri": "https://pcf"}}`, 400, "n5 over https requires n5.tls in the configuration file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := settings.NewLive(&memStore{}, defaultSettings(), check)

			code, body := serve(t, Config{Settings: s}, http.MethodPut, "/api/v1/policy", tt.body)
			if code != tt.code || decodeError(t, body) != tt.want {
				t.Fatalf("PUT = %d %s, want %d %q", code, body, tt.code, tt.want)
			}

			if s.Get().Policy.Interface != settings.PolicyNone {
				t.Fatalf("policy changed to %+v", s.Get().Policy)
			}
		})
	}
}
