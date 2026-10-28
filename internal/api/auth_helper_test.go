package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// testHandler is the API of cfg, on a database of users of its own unless cfg has one, which sends the requests
// without credentials as its first user, an admin.
func testHandler(t *testing.T, cfg Config) http.Handler {
	t.Helper()

	if cfg.Auth == nil {
		cfg.Auth = openAuthStore(t)
	}

	if cfg.JWTSecret == nil {
		cfg.JWTSecret = []byte("0123456789abcdef0123456789abcdef")
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	cfg.BcryptCost = bcrypt.MinCost
	h := NewHandler(cfg)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/init",
		strings.NewReader(`{"email": "admin@example.com", "password": "correct horse"}`)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/init = %d %s", rec.Code, rec.Body)
	}

	var resp struct {
		Result LoginResponse `json:"result"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			r.Header.Set("Authorization", "Bearer "+resp.Result.Token)
		}

		h.ServeHTTP(w, r)
	})
}

func openAuthStore(t *testing.T) *db.DB {
	t.Helper()

	d, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	return d
}
