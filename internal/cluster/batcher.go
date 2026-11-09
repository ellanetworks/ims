package cluster

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

const (
	maxBatchCommands = 256
	maxBatchBytes    = 512 * 1024
	maxInFlight      = 4
	queueLength      = 4096
	applyTimeout     = 30 * time.Second
)

const (
	queued int32 = iota
	taken
	abandoned
)

var (
	ErrTooLarge   = errors.New("command too large")
	errRestarting = fmt.Errorf("%w: raft is restarting", errRetry)
)

type request struct {
	cmds    []db.Command
	size    int
	state   atomic.Int32
	results []db.Result
	index   uint64
	err     error
	done    chan struct{}
}

type batcher struct {
	raft   *raft.Raft
	now    func() time.Time
	floor  func() int64
	check  func() *db.HashCheck
	queue  chan *request
	slots  chan struct{}
	stop   chan struct{}
	mu     sync.RWMutex
	closed bool
	wg     sync.WaitGroup
	last   int64
}

func newBatcher(r *raft.Raft, now func() time.Time, floor func() int64, check func() *db.HashCheck) *batcher {
	b := &batcher{
		raft:  r,
		now:   now,
		floor: floor,
		check: check,
		queue: make(chan *request, queueLength),
		slots: make(chan struct{}, maxInFlight),
		stop:  make(chan struct{}),
	}

	b.wg.Add(1)

	go b.run()

	return b
}

func (b *batcher) propose(ctx context.Context, cmds []db.Command) ([]db.Result, uint64, error) {
	size := 0
	for _, c := range cmds {
		size += len(c.Payload) + 64
	}

	if size > maxBatchBytes {
		return nil, 0, fmt.Errorf("%w: %d bytes", ErrTooLarge, size)
	}

	req := &request{cmds: cmds, size: size, done: make(chan struct{})}

	if err := b.enqueue(req); err != nil {
		return nil, 0, err
	}

	select {
	case <-req.done:
		return req.results, req.index, req.err
	case <-ctx.Done():
		if req.state.CompareAndSwap(queued, abandoned) {
			return nil, 0, ctx.Err()
		}

		return nil, 0, fmt.Errorf("%w: %w", errUnknown, ctx.Err())
	}
}

func (b *batcher) enqueue(req *request) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return errRestarting
	}

	select {
	case b.queue <- req:
		return nil
	default:
		return db.ErrBusy
	}
}

func (b *batcher) close() {
	b.mu.Lock()
	closed := b.closed
	b.closed = true
	b.mu.Unlock()

	if closed {
		return
	}

	close(b.stop)
	b.wg.Wait()

	for {
		select {
		case r := <-b.queue:
			if r.state.CompareAndSwap(queued, taken) {
				finish([]*request{r}, nil, 0, errRestarting)
			}
		default:
			return
		}
	}
}

func (b *batcher) run() {
	defer b.wg.Done()

	var carry *request

	for {
		select {
		case <-b.stop:
			if carry != nil {
				finish([]*request{carry}, nil, 0, errRestarting)
			}

			return
		case b.slots <- struct{}{}:
		}

		first := carry
		carry = nil

		for first == nil {
			select {
			case <-b.stop:
				<-b.slots
				return
			case r := <-b.queue:
				if r.state.CompareAndSwap(queued, taken) {
					first = r
				}
			}
		}

		batch := []*request{first}
		count, size := len(first.cmds), first.size

	fill:
		for count < maxBatchCommands {
			select {
			case next := <-b.queue:
				if !next.state.CompareAndSwap(queued, taken) {
					continue
				}

				if count+len(next.cmds) > maxBatchCommands || size+next.size > maxBatchBytes {
					carry = next
					break fill
				}

				batch = append(batch, next)
				count += len(next.cmds)
				size += next.size
			default:
				break fill
			}
		}

		b.send(batch)
	}
}

func (b *batcher) send(batch []*request) {
	b.last = max(b.last+1, b.now().UnixNano(), b.floor()+1)

	var cmds []db.Command
	for _, r := range batch {
		cmds = append(cmds, r.cmds...)
	}

	var check *db.HashCheck
	if b.check != nil {
		check = b.check()
	}

	entry, err := db.EncodeBatch(db.Batch{LeaderTime: b.last, Commands: cmds, Check: check})
	if err != nil {
		<-b.slots

		finish(batch, nil, 0, err)

		return
	}

	future := b.raft.Apply(entry, applyTimeout)

	b.wg.Add(1)

	go func() {
		defer b.wg.Done()
		defer func() { <-b.slots }()

		if err := future.Error(); err != nil {
			finish(batch, nil, 0, applyOutcome(err))
			return
		}

		results, ok := future.Response().([]db.Result)
		if !ok || len(results) != len(cmds) {
			finish(batch, nil, 0, fmt.Errorf("unexpected apply response %T", future.Response()))
			return
		}

		finish(batch, results, future.Index(), nil)
	}()
}

func finish(batch []*request, results []db.Result, index uint64, err error) {
	offset := 0

	for _, r := range batch {
		if err != nil {
			r.err = err
		} else {
			r.results = results[offset : offset+len(r.cmds)]
			r.index = index
		}

		offset += len(r.cmds)

		close(r.done)
	}
}
