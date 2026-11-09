package cluster

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

const (
	backupFormat     = 1
	backupDatabase   = "ims.db"
	backupManifest   = "manifest.json"
	backupChecksums  = "SHA256SUMS"
	maxBackupEntries = 3
	restoreTimeout   = 10 * time.Minute
)

var ErrInvalidBackup = errors.New("invalid backup")

type Manifest struct {
	Format       int              `json:"format"`
	Schema       int              `json:"schema"`
	Protocol     int              `json:"protocol"`
	AppliedIndex uint64           `json:"applied_index"`
	AppliedTerm  uint64           `json:"applied_term"`
	Sequences    map[string]int64 `json:"sequences"`
	CreatedAt    time.Time        `json:"created_at"`
}

func (n *Node) Backup(ctx context.Context, w io.Writer) error {
	return n.backupLocal(ctx, w)
}

func (n *Node) backupLocal(ctx context.Context, w io.Writer) error {
	c, err := n.db.CopyTo(ctx, n.scratch())
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(c.Path) }()

	if err := db.StripSecrets(ctx, c.Path); err != nil {
		return fmt.Errorf("leave the secrets out: %w", err)
	}

	return n.archive(ctx, w, c)
}

func (n *Node) archive(ctx context.Context, w io.Writer, c db.Copy) error {
	seqs, err := db.Sequences(ctx, c.Path)
	if err != nil {
		return fmt.Errorf("read the sequences: %w", err)
	}

	manifest, err := json.MarshalIndent(Manifest{
		Format: backupFormat, Schema: c.Position.Schema, Protocol: db.ProtocolVersion, AppliedIndex: c.Position.Index,
		AppliedTerm: c.Position.Term, Sequences: seqs, CreatedAt: n.cfg.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}

	sum, size, err := fileDigest(c.Path)
	if err != nil {
		return err
	}

	manifestSum := sha256.Sum256(manifest)
	checksums := fmt.Sprintf("%s  %s\n%s  %s\n", sum, backupDatabase, hex.EncodeToString(manifestSum[:]), backupManifest)

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	for _, e := range []struct {
		name string
		body []byte
	}{{backupManifest, manifest}, {backupChecksums, []byte(checksums)}} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body))}); err != nil {
			return err
		}

		if _, err := tw.Write(e.body); err != nil {
			return err
		}
	}

	if err := tw.WriteHeader(&tar.Header{Name: backupDatabase, Mode: 0o600, Size: size}); err != nil {
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

func (n *Node) scratch() string {
	return filepath.Join(n.cfg.Dir, "scratch")
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

func (n *Node) Restore(ctx context.Context, r io.Reader) error {
	if leading, _ := n.Leader(); !leading {
		return n.notLeader(ctx)
	}

	return n.restoreLocal(ctx, r)
}

func (n *Node) restoreLocal(ctx context.Context, r io.Reader) error {
	if ok, _ := n.Leader(); !ok {
		return ErrNotLeader
	}

	dir, err := os.MkdirTemp(mkdirAll(n.scratch()), "restore-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(dir) }()

	manifest, path, err := extract(r, dir)
	if err != nil {
		return err
	}

	live, err := n.db.Sequences(ctx)
	if err != nil {
		return err
	}

	if err := db.RaiseSequences(ctx, path, live); err != nil {
		return fmt.Errorf("keep the IDs moving forward: %w", err)
	}

	if err := n.db.KeepClusterState(ctx, path); err != nil {
		return fmt.Errorf("keep the cluster members and secrets: %w", err)
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}

	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	header := db.SnapshotHeader(manifest.Schema)

	return n.pauseEngines(func() error {
		r, _ := n.current()

		return r.Restore(&raft.SnapshotMeta{
			Version: raft.SnapshotVersionMax, Index: manifest.AppliedIndex, Term: manifest.AppliedTerm,
			Size: int64(len(header)) + info.Size(),
		}, io.MultiReader(bytes.NewReader(header), f), restoreTimeout)
	})
}

func mkdirAll(dir string) string {
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

func extract(r io.Reader, dir string) (Manifest, string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return Manifest{}, "", fmt.Errorf("%w: %w", ErrInvalidBackup, err)
	}

	tr := tar.NewReader(gz)

	var (
		manifestRaw, sums []byte
		path              string
		digest            string
	)

	for range maxBackupEntries {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return Manifest{}, "", fmt.Errorf("%w: %w", ErrInvalidBackup, err)
		}

		switch h.Name {
		case backupManifest:
			if manifestRaw, err = io.ReadAll(io.LimitReader(tr, 1<<20)); err != nil {
				return Manifest{}, "", fmt.Errorf("%w: %w", ErrInvalidBackup, err)
			}
		case backupChecksums:
			if sums, err = io.ReadAll(io.LimitReader(tr, 1<<20)); err != nil {
				return Manifest{}, "", fmt.Errorf("%w: %w", ErrInvalidBackup, err)
			}
		case backupDatabase:
			path = filepath.Join(dir, backupDatabase)

			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return Manifest{}, "", err
			}

			h := sha256.New()

			if _, err := io.Copy(io.MultiWriter(f, h), tr); err != nil {
				_ = f.Close()
				return Manifest{}, "", fmt.Errorf("%w: %w", ErrInvalidBackup, err)
			}

			if err := f.Close(); err != nil {
				return Manifest{}, "", err
			}

			digest = hex.EncodeToString(h.Sum(nil))
		default:
			return Manifest{}, "", fmt.Errorf("%w: unexpected file %q", ErrInvalidBackup, h.Name)
		}
	}

	if manifestRaw == nil || sums == nil || path == "" {
		return Manifest{}, "", fmt.Errorf("%w: missing %s, %s or %s", ErrInvalidBackup, backupManifest, backupChecksums,
			backupDatabase)
	}

	manifestSum := sha256.Sum256(manifestRaw)
	want := map[string]string{backupDatabase: digest, backupManifest: hex.EncodeToString(manifestSum[:])}

	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || want[fields[1]] != fields[0] {
			return Manifest{}, "", fmt.Errorf("%w: checksum mismatch on %q", ErrInvalidBackup, line)
		}

		delete(want, fields[1])
	}

	if len(want) != 0 {
		return Manifest{}, "", fmt.Errorf("%w: no checksum for every file", ErrInvalidBackup)
	}

	var m Manifest
	if err := json.Unmarshal(manifestRaw, &m); err != nil {
		return Manifest{}, "", fmt.Errorf("%w: %w", ErrInvalidBackup, err)
	}

	switch {
	case m.Format != backupFormat:
		return Manifest{}, "", fmt.Errorf("%w: format %d", ErrInvalidBackup, m.Format)
	case m.Schema > db.SchemaVersion():
		return Manifest{}, "", fmt.Errorf("%w: schema %d is newer than this version's %d", ErrInvalidBackup, m.Schema,
			db.SchemaVersion())
	case m.Protocol > db.ProtocolVersion:
		return Manifest{}, "", fmt.Errorf("%w: protocol %d", ErrInvalidBackup, m.Protocol)
	}

	return m, path, nil
}

func (n *Node) pauseEngines(fn func() error) error {
	n.mu.Lock()
	cancel := n.cancel
	n.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	n.runs.Wait()

	err := fn()

	r, _ := n.current()
	n.notify <- r.State() == raft.Leader

	return err
}
