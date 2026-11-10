package api_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/cluster"
	"github.com/ellanetworks/ims/internal/db"
)

func backupNode(t *testing.T, store *db.DB) *cluster.Node {
	t.Helper()

	n, err := cluster.Open(context.Background(), cluster.Config{Dir: filepath.Join(t.TempDir(), "raft")}, store)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = n.Close() })

	store.SetProposer(n)

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok, _ := n.Leader(); ok {
			return n
		}
	}

	t.Fatal("no leader")

	return nil
}

func TestAPITokenBacksUp(t *testing.T) {
	ctx := context.Background()
	a := newUninitializedAPI(t)
	node := backupNode(t, a.store)

	secret, err := a.store.JWTSecret(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, a.store)
	cfg.JWTSecret = func() []byte { return secret }
	cfg.Logger = slog.New(slog.NewJSONHandler(&a.logs, nil))
	cfg.Backups = node
	a.handler = api.NewHandler(cfg)
	a.token = initialize(t, a.handler)

	token := a.createToken(t, a.token, "/api/v1/users/me/api-tokens", `{"name": "backup-cron"}`)

	rec := serve(a.handler, request(http.MethodGet, "/api/v1/backup", "", token))
	if rec.Code != http.StatusOK {
		t.Fatalf("backup = %d %s", rec.Code, rec.Body)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Fatalf("Content-Type = %q", ct)
	}

	if cd, want := rec.Header().Get("Content-Disposition"), `attachment; filename="ims-backup-20260929T100000Z.tar.gz"`; cd != want {
		t.Fatalf("Content-Disposition = %q, want %q", cd, want)
	}

	raw := backedUpDatabase(t, rec.Body)
	if bytes.Contains(raw, secret) {
		t.Fatal("the backup holds the JWT secret")
	}

	if events := a.auditEvents(t, "backup"); len(events) != 1 || events[0] != testAdmin+": Database backed up" {
		t.Fatalf("audit = %q", events)
	}
}

type failingBackups struct{}

func (failingBackups) Backup(context.Context, io.Writer) error { return errors.New("disk full") }

func (failingBackups) Restore(context.Context, io.Reader) error { return errors.New("disk full") }

func TestBackupUnavailable(t *testing.T) {
	a := newTestAPI(t)

	code, _, msg := a.do(t, http.MethodGet, "/api/v1/backup", "")
	if code != http.StatusServiceUnavailable || msg != "Backups are not available" {
		t.Fatalf("backup = %d %q", code, msg)
	}
}

func TestBackupFailureAbortsTheResponse(t *testing.T) {
	store := openStore(t)
	cfg := testConfig(t, store)
	cfg.Backups = failingBackups{}
	h := api.NewHandler(cfg)
	token := initialize(t, h)

	defer func() {
		if r := recover(); !errors.Is(asError(r), http.ErrAbortHandler) {
			t.Fatalf("recovered %v, want %v", r, http.ErrAbortHandler)
		}
	}()

	serve(h, request(http.MethodGet, "/api/v1/backup", "", token))
	t.Fatal("the backup did not abort")
}

func asError(r any) error {
	err, _ := r.(error)
	return err
}

func backedUpDatabase(t *testing.T, r io.Reader) []byte {
	t.Helper()

	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(gz)

	for {
		h, err := tr.Next()
		if err != nil {
			t.Fatal(err)
		}

		if h.Name == "ims.db" {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}

			return b
		}
	}
}
