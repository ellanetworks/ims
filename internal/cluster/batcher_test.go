package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
	"go.opentelemetry.io/otel/trace/noop"
)

type echoFSM struct {
	gate    chan struct{}
	mu      sync.Mutex
	entries int
	applied []string
}

func (f *echoFSM) Apply(l *raft.Log) any {
	if f.gate != nil {
		<-f.gate
	}

	b, err := db.DecodeBatch(l.Data)
	if err != nil {
		panic(err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.entries++

	results := make([]db.Result, len(b.Commands))
	for i, c := range b.Commands {
		f.applied = append(f.applied, string(c.Payload))
		results[i] = db.Result{Value: c.Payload}
	}

	return results
}

func (f *echoFSM) Snapshot() (raft.FSMSnapshot, error) { return positionSnapshot{}, nil }

func (f *echoFSM) Restore(rc io.ReadCloser) error { return rc.Close() }

func (f *echoFSM) count() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.entries, append([]string(nil), f.applied...)
}

func newTestBatcher(t *testing.T, f *echoFSM, start bool) *batcher {
	t.Helper()

	conf := raft.DefaultConfig()
	conf.LocalID = "a"
	conf.HeartbeatTimeout, conf.ElectionTimeout, conf.LeaderLeaseTimeout = 50*time.Millisecond, 50*time.Millisecond,
		50*time.Millisecond
	conf.LogLevel = "off"

	addr, transport := raft.NewInmemTransport("a")
	store := raft.NewInmemStore()

	r, err := raft.NewRaft(conf, f, store, store, raft.NewInmemSnapshotStore(), transport)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = r.Shutdown().Error() })

	if err := r.BootstrapCluster(raft.Configuration{Servers: []raft.Server{{ID: "a", Address: addr}}}).Error(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-r.LeaderCh():
	case <-time.After(5 * time.Second):
		t.Fatal("no leader")
	}

	b := &batcher{
		raft:   r,
		tracer: noop.NewTracerProvider().Tracer(tracerName),
		now:    time.Now,
		floor:  func() int64 { return 0 },
		queue:  make(chan *request, queueLength),
		slots:  make(chan struct{}, maxInFlight),
		stop:   make(chan struct{}),
	}

	if start {
		b.wg.Add(1)

		go b.run()
	}

	t.Cleanup(b.close)

	return b
}

func cmd(s string) db.Command {
	return db.Command{Version: 1, Payload: json.RawMessage(`"` + s + `"`)}
}

func TestBatcherMatchesResultsToRequests(t *testing.T) {
	f := &echoFSM{gate: make(chan struct{})}
	b := newTestBatcher(t, f, true)

	var wg sync.WaitGroup

	errs := make(chan error, 200)

	for i := range 200 {
		wg.Go(func() {
			cmds := make([]db.Command, 1+i%3)
			for j := range cmds {
				cmds[j] = cmd(fmt.Sprintf("%d-%d", i, j))
			}

			results, _, err := b.propose(context.Background(), cmds)
			if err != nil {
				errs <- err
				return
			}

			for j, r := range results {
				if string(r.Value) != string(cmds[j].Payload) {
					errs <- fmt.Errorf("request %d command %d got %s", i, j, r.Value)
				}
			}
		})
	}

	time.Sleep(50 * time.Millisecond)
	close(f.gate)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	if entries, _ := f.count(); entries > maxInFlight+maxInFlight {
		t.Fatalf("%d entries for 200 requests queued behind a full pipeline", entries)
	}
}

func TestBatcherSkipsAbandonedRequests(t *testing.T) {
	f := &echoFSM{}
	b := newTestBatcher(t, f, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := b.propose(ctx, []db.Command{cmd("abandoned")}); !errors.Is(err, context.Canceled) ||
		errors.Is(err, errUnknown) {
		t.Fatalf("err = %v", err)
	}

	b.wg.Add(1)

	go b.run()

	if _, _, err := b.propose(timeout(t), []db.Command{cmd("kept")}); err != nil {
		t.Fatal(err)
	}

	if _, applied := f.count(); len(applied) != 1 || applied[0] != `"kept"` {
		t.Fatalf("applied %v", applied)
	}
}

func TestBatcherCloseFailsQueuedRequestsForRetry(t *testing.T) {
	b := newTestBatcher(t, &echoFSM{}, false)

	done := make(chan error, 1)

	go func() {
		_, _, err := b.propose(timeout(t), []db.Command{cmd("queued")})
		done <- err
	}()

	for len(b.queue) == 0 {
		time.Sleep(time.Millisecond)
	}

	b.close()

	if err := <-done; !errors.Is(err, errRetry) {
		t.Fatalf("err = %v", err)
	}

	if _, _, err := b.propose(timeout(t), []db.Command{cmd("late")}); !errors.Is(err, errRetry) {
		t.Fatalf("err = %v", err)
	}
}
