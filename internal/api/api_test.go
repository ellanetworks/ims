package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ellanetworks/ims/version"
)

func TestGetStatus(t *testing.T) {
	h := NewHandler(Config{Logger: slog.New(slog.DiscardHandler)})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Result Status `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if want := (Status{Version: version.Get().Version, Revision: version.Get().Revision}); resp.Result != want || want.Version != "v0.0.1" {
		t.Fatalf("status = %+v, want %+v at v0.0.1", resp.Result, want)
	}
}
