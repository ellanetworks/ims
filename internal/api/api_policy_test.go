package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakePolicy PolicyStatus

func (f fakePolicy) PolicyStatus() PolicyStatus { return PolicyStatus(f) }

func getPolicyStatus(t *testing.T, cfg Config) string {
	t.Helper()

	cfg.Logger = slog.New(slog.DiscardHandler)

	rec := httptest.NewRecorder()
	NewHandler(cfg).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/policy", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	return string(resp.Result)
}

func TestGetPolicyStatus(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	for name, tc := range map[string]struct {
		policy Policy
		want   string
	}{
		"none": {nil, `{"interface":"none"}`},
		"rx":   {fakePolicy{Interface: "rx", Endpoint: "rx:pcrf.epc"}, `{"interface":"rx","endpoint":"rx:pcrf.epc"}`},
		"n5 before any request": {
			fakePolicy{Interface: "n5", Endpoint: "http://10.0.0.13:7777", Notify: "10.0.0.5:7778"},
			`{"interface":"n5","endpoint":"http://10.0.0.13:7777","notify":"10.0.0.5:7778"}`,
		},
		"n5": {
			fakePolicy{
				Interface: "n5", Endpoint: "http://10.0.0.13:7777", Notify: "10.0.0.5:7778",
				Last: &PolicyResult{At: at, Reachable: true, Result: "404 APPLICATION_SESSION_CONTEXT_NOT_FOUND"},
			},
			`{"interface":"n5","endpoint":"http://10.0.0.13:7777","notify":"10.0.0.5:7778",` +
				`"last":{"at":"2026-10-05T12:00:00.000Z","reachable":true,"result":"404 APPLICATION_SESSION_CONTEXT_NOT_FOUND"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := getPolicyStatus(t, Config{Policy: tc.policy}); got != tc.want {
				t.Fatalf("policy = %s, want %s", got, tc.want)
			}
		})
	}
}
