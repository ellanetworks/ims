package cluster

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/hashicorp/raft"
	raftwal "github.com/hashicorp/raft-wal"
)

const walSegmentSize = 16 * 1024 * 1024

func openLogStore(dir string) (*raftwal.WAL, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	return raftwal.Open(dir, raftwal.WithSegmentSize(walSegmentSize))
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
