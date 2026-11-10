package callrecords

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type pruneCall struct {
	before  time.Time
	maxRows int
}

type pruneStore chan pruneCall

func (s pruneStore) PruneCallRecords(_ context.Context, before time.Time, maxRows int) (int64, error) {
	s <- pruneCall{before, maxRows}
	return 0, nil
}

type fixedClock time.Time

func (c fixedClock) Now() time.Time { return time.Time(c) }

func TestPrune(t *testing.T) {
	const day = 24 * time.Hour

	store := make(pruneStore)

	var retention atomic.Int64
	retention.Store(int64(90 * day))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		defer close(done)

		Prune(ctx, PruneConfig{
			Store: store, Interval: time.Millisecond, Clock: fixedClock(t0),
			Retention: func() time.Duration { return time.Duration(retention.Load()) },
		})
	}()

	if c := <-store; c.maxRows != MaxRecords || !c.before.Equal(t0.Add(-90*day)) {
		t.Fatalf("first prune %+v, want at once, of the records older than 90 days or past %d", c, MaxRecords)
	}

	retention.Store(int64(day))

	for c := range store {
		if c.before.Equal(t0.Add(-day)) {
			break
		}
	}

	cancel()

	for {
		select {
		case <-store:
		case <-done:
			return
		}
	}
}

func TestPruneOnlyWhenActive(t *testing.T) {
	store := make(pruneStore, 1)

	var active atomic.Bool

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		defer close(done)

		Prune(ctx, PruneConfig{
			Store: store, Interval: time.Millisecond, Clock: fixedClock(t0), Active: active.Load,
			Retention: func() time.Duration { return time.Hour },
		})
	}()

	select {
	case c := <-store:
		t.Fatalf("pruned %+v while not active", c)
	case <-time.After(50 * time.Millisecond):
	}

	active.Store(true)
	<-store

	cancel()

	for {
		select {
		case <-store:
		case <-done:
			return
		}
	}
}
