package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ellanetworks/ims/internal/db"
)

const (
	format        = 1
	databaseFile  = "ims.db"
	manifestFile  = "manifest.json"
	checksumsFile = "SHA256SUMS"
)

type Manifest struct {
	Format    int       `json:"format"`
	Schema    int       `json:"schema"`
	CreatedAt time.Time `json:"created_at"`
}

type Backups struct {
	DB  *db.DB
	Dir string
	Now func() time.Time
}

func (b *Backups) Backup(ctx context.Context, w io.Writer) error {
	c, err := b.DB.CopyTo(ctx, b.Dir)
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(c.Path) }()

	if err := db.StripForBackup(ctx, c.Path); err != nil {
		return fmt.Errorf("leave the secrets and the runtime state out: %w", err)
	}

	manifest, err := json.MarshalIndent(Manifest{Format: format, Schema: c.Schema, CreatedAt: b.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}

	sum, size, err := fileDigest(c.Path)
	if err != nil {
		return err
	}

	manifestSum := sha256.Sum256(manifest)
	checksums := fmt.Sprintf("%s  %s\n%s  %s\n", sum, databaseFile, hex.EncodeToString(manifestSum[:]), manifestFile)

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	for _, e := range []struct {
		name string
		body []byte
	}{{manifestFile, manifest}, {checksumsFile, []byte(checksums)}} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body))}); err != nil {
			return err
		}

		if _, err := tw.Write(e.body); err != nil {
			return err
		}
	}

	if err := tw.WriteHeader(&tar.Header{Name: databaseFile, Mode: 0o600, Size: size}); err != nil {
		return err
	}

	f, err := os.Open(c.Path)
	if err != nil {
		return err
	}

	defer func() { _ = f.Close() }()

	if _, err := io.Copy(tw, f); err != nil {
		return err
	}

	return errors.Join(tw.Close(), gz.Close())
}

func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}

	defer func() { _ = f.Close() }()

	h := sha256.New()

	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}

	return hex.EncodeToString(h.Sum(nil)), size, nil
}
