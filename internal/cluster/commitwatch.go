package cluster

import (
	"sync"
	"sync/atomic"

	"github.com/hashicorp/raft"
)

type commitWatch struct {
	raft.Transport
	seen     *atomic.Uint64
	consumer chan raft.RPC
	done     chan struct{}
	once     sync.Once
}

func watchCommits(t raft.Transport, seen *atomic.Uint64) *commitWatch {
	w := &commitWatch{Transport: t, seen: seen, consumer: make(chan raft.RPC), done: make(chan struct{})}

	go w.relay()

	return w
}

func (w *commitWatch) record(rpc raft.RPC) {
	req, ok := rpc.Command.(*raft.AppendEntriesRequest)
	if !ok {
		return
	}

	for {
		seen := w.seen.Load()
		if req.LeaderCommitIndex <= seen || w.seen.CompareAndSwap(seen, req.LeaderCommitIndex) {
			return
		}
	}
}

func (w *commitWatch) relay() {
	for {
		select {
		case <-w.done:
			return
		case rpc := <-w.Transport.Consumer():
			w.record(rpc)

			select {
			case w.consumer <- rpc:
			case <-w.done:
				return
			}
		}
	}
}

func (w *commitWatch) Consumer() <-chan raft.RPC { return w.consumer }

func (w *commitWatch) SetHeartbeatHandler(cb func(rpc raft.RPC)) {
	if cb == nil {
		w.Transport.SetHeartbeatHandler(nil)
		return
	}

	w.Transport.SetHeartbeatHandler(func(rpc raft.RPC) {
		w.record(rpc)
		cb(rpc)
	})
}

func (w *commitWatch) RequestPreVote(id raft.ServerID, target raft.ServerAddress, args *raft.RequestPreVoteRequest,
	resp *raft.RequestPreVoteResponse,
) error {
	return w.Transport.(raft.WithPreVote).RequestPreVote(id, target, args, resp)
}

func (w *commitWatch) Close() error {
	w.once.Do(func() { close(w.done) })

	if c, ok := w.Transport.(raft.WithClose); ok {
		return c.Close()
	}

	return nil
}
