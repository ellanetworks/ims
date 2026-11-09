package cluster

import (
	"context"
	"fmt"
	"io"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

type fsm struct {
	db  *db.DB
	dir string
}

var _ raft.BatchingFSM = (*fsm)(nil)

func (f *fsm) Apply(l *raft.Log) any {
	return f.ApplyBatch([]*raft.Log{l})[0]
}

func (f *fsm) ApplyBatch(logs []*raft.Log) []any {
	out := make([]any, len(logs))

	var run []int

	flush := func() {
		if len(run) == 0 {
			return
		}

		entries := make([]db.Entry, len(run))
		for j, i := range run {
			entries[j] = db.Entry{Index: logs[i].Index, Term: logs[i].Term, Data: logs[i].Data}
		}

		results, err := f.db.ApplyEntries(entries)
		if err != nil {
			panic(fmt.Sprintf("halting: %v", err))
		}

		for j, i := range run {
			out[i] = results[j]
		}

		run = run[:0]
	}

	for i, l := range logs {
		switch l.Type {
		case raft.LogCommand:
			run = append(run, i)
		case raft.LogConfiguration:
			flush()

			if err := f.db.RecordConfiguration(l.Index, l.Term, l.Data); err != nil {
				panic(fmt.Sprintf("halting: %v", err))
			}
		}
	}

	flush()

	return out
}

func (f *fsm) Snapshot() (raft.FSMSnapshot, error) {
	return positionSnapshot{}, nil
}

type positionSnapshot struct{}

func (positionSnapshot) Persist(sink raft.SnapshotSink) error {
	return sink.Close()
}

func (positionSnapshot) Release() {}

func (f *fsm) Restore(rc io.ReadCloser) error {
	defer func() { _ = rc.Close() }()

	source, sourced := sourceOf(rc)
	if sourced && source.self {
		return nil
	}

	path, err := db.ReadSnapshot(rc, f.dir)
	if err != nil {
		return err
	}

	var at *db.Position
	if sourced {
		at = &source.position
	}

	return f.db.Install(context.Background(), path, at)
}
