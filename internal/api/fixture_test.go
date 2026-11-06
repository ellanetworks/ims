package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/db"
	"golang.org/x/crypto/bcrypt"
)

var testNow = time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

type testAPI struct {
	handler http.Handler
	store   *db.DB
	token   string
	now     time.Time
	logs    bytes.Buffer
}

const (
	testAdmin    = "admin@example.com"
	testPassword = "correct horse"
)

var testJWTSecret = []byte("0123456789abcdef0123456789abcdef")

func testConfig(t *testing.T, store *db.DB) api.Config {
	t.Helper()

	return api.Config{
		Auth:          store,
		OIDC:          testOIDC(t, store),
		JWTSecret:     testJWTSecret,
		OIDCCookieKey: []byte("fedcba9876543210fedcba9876543210"),
		BcryptCost:    bcrypt.MinCost,
		Now:           func() time.Time { return testNow },
		Logger:        slog.New(slog.DiscardHandler),
	}
}

func openStore(t *testing.T) *db.DB {
	t.Helper()

	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	return store
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()

	a := newUninitializedAPI(t)
	a.token = initialize(t, a.handler)

	return a
}

func newUninitializedAPI(t *testing.T) *testAPI {
	t.Helper()

	store := openStore(t)
	a := &testAPI{store: store, now: testNow}

	cfg := testConfig(t, store)
	cfg.Now = func() time.Time { return a.now }
	cfg.Logger = slog.New(slog.NewJSONHandler(&a.logs, nil))

	a.handler = api.NewHandler(cfg)

	return a
}

func initialize(t *testing.T, h http.Handler) string {
	t.Helper()

	rec := serve(h, request(http.MethodPost, "/api/v1/init",
		`{"email": "`+testAdmin+`", "password": "`+testPassword+`"}`, ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/init = %d %s", rec.Code, rec.Body)
	}

	var resp struct {
		Result api.LoginResponse `json:"result"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	return resp.Result.Token
}

func request(method, path, body, token string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func (a *testAPI) do(t *testing.T, method, path, body string) (int, json.RawMessage, string) {
	t.Helper()

	return a.doAs(t, a.token, method, path, body)
}

func (a *testAPI) doAs(t *testing.T, token, method, path, body string) (int, json.RawMessage, string) {
	t.Helper()

	rec := serve(a.handler, request(method, path, body, token))

	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s %s: invalid JSON %q", method, path, rec.Body.String())
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}

	return rec.Code, resp.Result, resp.Error
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()

	var v T

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}

	return v
}

func (a *testAPI) auditEvents(t *testing.T, action string) []string {
	t.Helper()

	var details []string

	for line := range strings.Lines(a.logs.String()) {
		var e struct{ Msg, Action, Actor, Details string }
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}

		if e.Msg == "audit event" && e.Action == action {
			details = append(details, e.Actor+": "+e.Details)
		}
	}

	return details
}
