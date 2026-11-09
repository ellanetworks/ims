package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

const (
	databaseSnapshot = "database"
	installSuffix    = ".db"
	stateSuffix      = ".state"
)

type snapshotStore struct {
	db  *db.DB
	dir string
}

var _ raft.SnapshotStore = (*snapshotStore)(nil)

func newSnapshotStore(d *db.DB, dir string) (*snapshotStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the snapshot directory: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read the snapshot directory: %w", err)
	}

	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return nil, fmt.Errorf("remove a leftover snapshot: %w", err)
		}
	}

	return &snapshotStore{db: d, dir: dir}, nil
}

func meta(id string, p db.Position) *raft.SnapshotMeta {
	m := &raft.SnapshotMeta{
		Version:            raft.SnapshotVersionMax,
		ID:                 id,
		Index:              p.Index,
		Term:               p.Term,
		ConfigurationIndex: p.ConfigurationIndex,
	}

	if p.Configuration != nil {
		m.Configuration = raft.DecodeConfiguration(p.Configuration)
	}

	return m
}

func (s *snapshotStore) Create(_ raft.SnapshotVersion, index, term uint64, configuration raft.Configuration,
	configurationIndex uint64, _ raft.Transport,
) (raft.SnapshotSink, error) {
	return &sink{store: s, position: db.Position{
		Index: index, Term: term, Configuration: raft.EncodeConfiguration(configuration),
		ConfigurationIndex: configurationIndex,
	}}, nil
}

func (s *snapshotStore) List() ([]*raft.SnapshotMeta, error) {
	p, err := s.db.Position(context.Background())
	if err != nil {
		return nil, err
	}

	if p.Index == 0 {
		return nil, nil
	}

	return []*raft.SnapshotMeta{meta(databaseSnapshot, p)}, nil
}

func (s *snapshotStore) Open(id string) (*raft.SnapshotMeta, io.ReadCloser, error) {
	if id == databaseSnapshot {
		return s.openDatabase()
	}

	return s.openInstall(id)
}

func (s *snapshotStore) openDatabase() (*raft.SnapshotMeta, io.ReadCloser, error) {
	c, err := s.db.CopyTo(context.Background(), s.dir)
	if err != nil {
		return nil, nil, err
	}

	f, err := os.Open(c.Path)
	if err != nil {
		_ = os.Remove(c.Path)
		return nil, nil, err
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		_ = os.Remove(c.Path)

		return nil, nil, err
	}

	header := db.SnapshotHeader(c.Position.Schema)

	m := meta(databaseSnapshot, c.Position)
	m.Size = int64(len(header)) + info.Size()

	return m, &source{Reader: io.MultiReader(bytes.NewReader(header), f), position: c.Position, self: true, release: func() {
		_ = f.Close()
		_ = os.Remove(c.Path)
	}}, nil
}

func (s *snapshotStore) openInstall(id string) (*raft.SnapshotMeta, io.ReadCloser, error) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return nil, nil, fmt.Errorf("invalid snapshot %q", id)
	}

	data, state := filepath.Join(s.dir, id+installSuffix), filepath.Join(s.dir, id+stateSuffix)

	raw, err := os.ReadFile(state)
	if err != nil {
		return nil, nil, fmt.Errorf("read snapshot %s: %w", id, err)
	}

	var p db.Position
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, nil, fmt.Errorf("read snapshot %s: %w", id, err)
	}

	f, err := os.Open(data)
	if err != nil {
		return nil, nil, fmt.Errorf("open snapshot %s: %w", id, err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("open snapshot %s: %w", id, err)
	}

	m := meta(id, p)
	m.Size = info.Size()

	return m, &source{Reader: f, position: p, release: func() {
		_ = f.Close()
		_ = os.Remove(data)
		_ = os.Remove(state)
	}}, nil
}

type source struct {
	io.Reader
	position db.Position
	self     bool
	release  func()
	once     sync.Once
}

func (s *source) Close() error {
	s.once.Do(s.release)
	return nil
}

func sourceOf(rc io.ReadCloser) (*source, bool) {
	for {
		switch r := rc.(type) {
		case *source:
			return r, true
		case raft.ReadCloserWrapper:
			rc = r.WrappedReadCloser()
		default:
			return nil, false
		}
	}
}

type sink struct {
	store    *snapshotStore
	position db.Position

	mu     sync.Mutex
	file   *os.File
	id     string
	closed bool
}

func (k *sink) Write(p []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.file == nil {
		f, err := os.CreateTemp(k.store.dir, "receive-*.tmp")
		if err != nil {
			return 0, err
		}

		k.file = f
	}

	return k.file.Write(p)
}

func (k *sink) ID() string {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.id == "" {
		return databaseSnapshot
	}

	return k.id
}

func (k *sink) Cancel() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.closed {
		return nil
	}

	k.closed = true

	if k.file != nil {
		_ = k.file.Close()
		return os.Remove(k.file.Name())
	}

	return nil
}

func (k *sink) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.closed {
		return nil
	}

	k.closed = true

	if k.file == nil {
		return k.store.db.MakeDurable(context.Background(), k.position)
	}

	tmp := k.file.Name()

	if err := errorsJoin(k.file.Sync(), k.file.Close()); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	id := fmt.Sprintf("%d-%d-%d", k.position.Term, k.position.Index, time.Now().UnixMilli())

	raw, err := json.Marshal(k.position)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if err := os.WriteFile(filepath.Join(k.store.dir, id+stateSuffix), raw, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if err := os.Rename(tmp, filepath.Join(k.store.dir, id+installSuffix)); err != nil {
		_ = os.Remove(tmp)
		_ = os.Remove(filepath.Join(k.store.dir, id+stateSuffix))

		return err
	}

	k.id = id

	return nil
}
