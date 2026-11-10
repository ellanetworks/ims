package cluster

import (
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	"go.etcd.io/bbolt"
)

func openLogStore(path string) (*raftboltdb.BoltStore, error) {
	return raftboltdb.New(raftboltdb.Options{
		Path:        path,
		BoltOptions: &bbolt.Options{NoFreelistSync: true, FreelistType: bbolt.FreelistMapType},
	})
}

var errLogStoreTimeout = errors.New("the log store did not finish writing in time")

type timeoutLogStore struct {
	raft.LogStore
	timeout time.Duration
}

func (s timeoutLogStore) StoreLog(l *raft.Log) error {
	return s.StoreLogs([]*raft.Log{l})
}

func (s timeoutLogStore) StoreLogs(logs []*raft.Log) error {
	done := make(chan error, 1)

	go func() { done <- s.LogStore.StoreLogs(logs) }()

	timer := time.NewTimer(s.timeout)
	defer timer.Stop()

	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("%w: %d entries after %s", errLogStoreTimeout, len(logs), s.timeout)
	}
}

func (s timeoutLogStore) IsMonotonic() bool {
	m, ok := s.LogStore.(raft.MonotonicLogStore)
	return ok && m.IsMonotonic()
}

func errorsJoin(errs ...error) error {
	return errors.Join(errs...)
}
