package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/settings"
)

// memStore is a settings.Store in memory, which fails to save with err.
type memStore struct {
	err error
}

func (m *memStore) UpdateOperator(context.Context, settings.Operator) error { return m.err }

func (m *memStore) CreatePeer(context.Context, settings.Peer) error { return m.err }

func (m *memStore) UpdatePeer(context.Context, settings.Peer) error { return m.err }

func (m *memStore) DeletePeer(context.Context, string) error { return m.err }

func (m *memStore) UpdateRoute(context.Context, settings.Route) error { return m.err }

func (m *memStore) UpdatePolicy(context.Context, settings.Policy) error { return m.err }

func (m *memStore) UpdateCallRecords(context.Context, settings.CallRecords) error { return m.err }

func defaultSettings() settings.Settings {
	return settings.Settings{
		Operator: settings.Operator{
			MCC: "001", MNC: "01",
			Numbering: settings.Numbering{CountryCode: "1", NationalPrefix: "1", InternationalPrefix: "011"},
		},
		Routes:      []settings.Route{{Application: settings.ApplicationCx}, {Application: settings.ApplicationRx}},
		Policy:      settings.Policy{Interface: settings.PolicyNone},
		CallRecords: settings.CallRecords{RetentionDays: 90},
	}
}

func newFakeSettings() *settings.Live {
	return settings.NewLive(&memStore{}, defaultSettings(), nil)
}

const validOperator = `{"mcc": "208", "mnc": "10", "numbering": {"country_code": "33", "national_prefix": "0", "international_prefix": "00"}}`

func serve(t *testing.T, cfg Config, method, path, body string) (int, []byte) {
	t.Helper()

	cfg.Logger = slog.New(slog.DiscardHandler)

	rec := httptest.NewRecorder()
	NewHandler(cfg).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body)))

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("%s %s: Content-Type = %q, want application/json", method, path, ct)
	}

	return rec.Code, rec.Body.Bytes()
}

func decodeResult[T any](t *testing.T, body []byte) T {
	t.Helper()

	var resp struct {
		Result T `json:"result"`
	}

	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}

	return resp.Result
}

func decodeError(t *testing.T, body []byte) string {
	t.Helper()

	var resp ErrorResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}

	return resp.Error
}

func TestGetOperator(t *testing.T) {
	code, body := serve(t, Config{Settings: newFakeSettings()}, http.MethodGet, "/api/v1/operator", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	want := Operator{MCC: "001", MNC: "01", Numbering: Numbering{CountryCode: "1", NationalPrefix: "1", InternationalPrefix: "011"}}
	if got := decodeResult[Operator](t, body); got != want {
		t.Fatalf("operator = %+v, want %+v", got, want)
	}
}

func TestUpdateOperator(t *testing.T) {
	s := newFakeSettings()

	code, body := serve(t, Config{Settings: s}, http.MethodPut, "/api/v1/operator", validOperator)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, body)
	}

	want := Operator{MCC: "208", MNC: "10", Numbering: Numbering{CountryCode: "33", NationalPrefix: "0", InternationalPrefix: "00"}}
	if got := decodeResult[Operator](t, body); got != want {
		t.Fatalf("response = %+v, want %+v", got, want)
	}

	if got := operatorResponse(s.Get().Operator); got != want {
		t.Fatalf("stored = %+v, want %+v", got, want)
	}
}

func TestUpdateOperatorWithoutPrefixes(t *testing.T) {
	code, body := serve(t, Config{Settings: newFakeSettings()}, http.MethodPut, "/api/v1/operator",
		`{"mcc": "208", "mnc": "10", "numbering": {"country_code": "33", "national_prefix": "", "international_prefix": ""}}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, body)
	}
}

func TestUpdateOperatorRejected(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"malformed", `{"mcc":`, "Invalid request data"},
		{"unknown field", `{"mcc": "001", "mnc": "01", "home_domain": "example.org", "numbering": {"country_code": "1"}}`, "Invalid request data"},
		{"short mcc", strings.Replace(validOperator, `"208"`, `"20"`, 1), "mcc must be 3 digits"},
		{"long mnc", strings.Replace(validOperator, `"10"`, `"1000"`, 1), "mnc must be 2 or 3 digits"},
		{"no numbering", `{"mcc": "208", "mnc": "10"}`, "numbering.country_code must be 1 to 3 digits, not starting with 0"},
		{"long prefix", strings.Replace(validOperator, `"national_prefix": "0"`, `"national_prefix": "00000"`, 1), "numbering.national_prefix must be up to 4 digits"},
		{"same prefixes", strings.Replace(validOperator, `"national_prefix": "0"`, `"national_prefix": "00"`, 1), "numbering.national_prefix and numbering.international_prefix must differ"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeSettings()
			before := s.Get()

			code, body := serve(t, Config{Settings: s}, http.MethodPut, "/api/v1/operator", tt.body)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}

			if got := decodeError(t, body); got != tt.want {
				t.Fatalf("error = %q, want %q", got, tt.want)
			}

			if s.Get().Operator != before.Operator {
				t.Fatalf("settings changed to %+v", s.Get())
			}
		})
	}
}

func TestUpdateOperatorStoreFailure(t *testing.T) {
	s := settings.NewLive(&memStore{err: errors.New("disk full")}, defaultSettings(), nil)

	code, body := serve(t, Config{Settings: s}, http.MethodPut, "/api/v1/operator", validOperator)
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}

	if got := decodeError(t, body); got != "Failed to update operator" {
		t.Fatalf("error = %q", got)
	}
}
