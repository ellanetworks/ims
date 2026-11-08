package backup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/backup"
	"github.com/ellanetworks/ims/internal/db"
)

func TestBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store, err := db.Open(ctx, filepath.Join(dir, "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	jwt, err := store.JWTSecret(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cookieKey, err := store.OIDCCookieKey(ctx)
	if err != nil {
		t.Fatal(err)
	}

	userID, err := store.CreateUser(ctx, db.NewUser{Email: "admin@example.com", Role: db.RoleAdmin, PasswordHash: "hash"}, 10)
	if err != nil {
		t.Fatal(err)
	}

	created := time.Now()

	if _, err := store.CreateAPIToken(ctx, db.NewAPIToken{
		TokenID: "abc", Name: "cron", TokenHash: bytes.Repeat([]byte{1}, 32), UserID: userID, CreatedAt: created,
	}); err != nil {
		t.Fatal(err)
	}

	sessionHash := bytes.Repeat([]byte{0xab}, 32)

	if _, err := store.CreateSession(ctx, db.NewSession{
		UserID: userID, TokenHash: sessionHash, Method: db.SessionPassword, CreatedAt: created,
		ExpiresAt: created.Add(time.Hour),
	}, 10); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	b := &backup.Backups{DB: store, Dir: dir, Now: func() time.Time { return now }}

	var out bytes.Buffer
	if err := b.Backup(ctx, &out); err != nil {
		t.Fatal(err)
	}

	files := untar(t, &out)

	if len(files) != 3 {
		t.Fatalf("files = %v, want manifest.json, SHA256SUMS and ims.db", keys(files))
	}

	var m backup.Manifest
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}

	if want := (backup.Manifest{Format: 1, Schema: db.SchemaVersion(), CreatedAt: now}); m != want {
		t.Fatalf("manifest = %+v, want %+v", m, want)
	}

	for _, name := range []string{"ims.db", "manifest.json"} {
		sum := sha256.Sum256(files[name])
		if !strings.Contains(string(files["SHA256SUMS"]), hex.EncodeToString(sum[:])+"  "+name+"\n") {
			t.Errorf("SHA256SUMS has no checksum for %s: %s", name, files["SHA256SUMS"])
		}
	}

	raw := files["ims.db"]

	for name, secret := range map[string][]byte{"JWT secret": jwt, "OIDC cookie key": cookieKey, "session": sessionHash} {
		if bytes.Contains(raw, secret) {
			t.Errorf("the backup holds the %s", name)
		}
	}

	path := filepath.Join(t.TempDir(), "backup.db")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	conn, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = conn.Close() }()

	var email string
	if err := conn.QueryRowContext(ctx, `SELECT email FROM users`).Scan(&email); err != nil || email != "admin@example.com" {
		t.Fatalf("user in the backup = %q, %v", email, err)
	}

	for table, want := range map[string]int{"users": 1, "api_tokens": 1, "operator": 1, "oidc": 1, "sessions": 0, "jwt_secret": 0, "oidc_cookie_key": 0} {
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != want {
			t.Errorf("%s in the backup: %d rows, %v; want %d", table, n, err, want)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".backup-") {
			t.Errorf("left %s behind", e.Name())
		}
	}
}

func untar(t *testing.T, r io.Reader) map[string][]byte {
	t.Helper()

	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(gz)
	files := map[string][]byte{}

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files
		}

		if err != nil {
			t.Fatal(err)
		}

		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}

		files[h.Name] = b
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}
