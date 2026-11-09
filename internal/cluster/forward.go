package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/ellanetworks/ims/internal/cluster/listener"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

const (
	maxApplyGap      = 5000
	minForwardBudget = 5 * time.Second
	maxBackoff       = 500 * time.Millisecond
	minBackoff       = 10 * time.Millisecond
	rpcTimeout       = 30 * time.Second
	maxRPCBody       = 64 << 20
)

var (
	ErrNoLeader = fmt.Errorf("%w: no cluster leader", db.ErrBusy)
	errRetry    = errors.New("retry")
	errUnknown  = errors.New("the outcome of the write is unknown")
)

func (n *Node) forwardBudget() time.Duration {
	return max(minForwardBudget, 2*n.electionTimeout())
}

func (n *Node) Propose(ctx context.Context, cmds []db.Command) ([]db.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, n.forwardBudget())
	defer cancel()

	var last error

	for attempt := 0; ; attempt++ {
		results, err := n.proposeOnce(ctx, cmds)

		switch {
		case errors.Is(err, errUnknown) && !retryable(cmds):
			return nil, fmt.Errorf("%w: %w", db.ErrBusy, err)
		case !errors.Is(err, errRetry) && !errors.Is(err, errUnknown):
			return results, err
		}

		last = err

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %w", ErrNoLeader, last)
		case <-n.done:
			return nil, raft.ErrRaftShutdown
		case <-n.removed:
			return nil, ErrRemoved
		case <-n.readyCh():
		case <-time.After(backoff(attempt)):
		}
	}
}

func retryable(cmds []db.Command) bool {
	for _, c := range cmds {
		if !c.Retryable {
			return false
		}
	}

	return true
}

func backoff(attempt int) time.Duration {
	d := min(maxBackoff, minBackoff<<min(attempt, 6))

	return d/2 + rand.N(d/2+1)
}

func (n *Node) proposeOnce(ctx context.Context, cmds []db.Command) ([]db.Result, error) {
	select {
	case <-n.removed:
		return nil, ErrRemoved
	default:
	}

	if r, _ := n.current(); r.CommitIndex() > n.db.AppliedIndex()+maxApplyGap {
		return nil, fmt.Errorf("%w: the database is %d entries behind the log", db.ErrBusy,
			r.CommitIndex()-n.db.AppliedIndex())
	}

	if leading, _ := n.Leader(); leading {
		_, b := n.current()
		if b == nil {
			return nil, fmt.Errorf("%w: raft is restarting", errRetry)
		}

		results, _, err := b.propose(ctx, cmds)

		return results, classify(err)
	}

	r, _ := n.current()

	_, leaderID := r.LeaderWithID()

	switch {
	case leaderID == "":
		return nil, fmt.Errorf("%w: no leader", errRetry)
	case string(leaderID) == n.id:
		return nil, fmt.Errorf("%w: the leader term is starting", errRetry)
	case n.peers == nil:
		return nil, fmt.Errorf("%w: leader %s is not reachable without a cluster address", errRetry, leaderID)
	}

	var reply proposeReply

	if err := n.peers.call(ctx, "/cluster/v1/propose", proposeRequest{Commands: cmds}, &reply); err != nil {
		return nil, err
	}

	if reply.Busy {
		return nil, db.ErrBusy
	}

	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), n.forwardBudget())
	defer cancel()

	if err := n.db.WaitApplied(waitCtx, reply.Index); err != nil {
		return nil, fmt.Errorf("%w: committed at index %d but not applied on this node: %w", errUnknown, reply.Index, err)
	}

	return reply.Results, nil
}

func classify(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, raft.ErrNotLeader), errors.Is(err, raft.ErrLeadershipTransferInProgress),
		errors.Is(err, raft.ErrEnqueueTimeout):
		return fmt.Errorf("%w: %w", errRetry, err)
	case errors.Is(err, raft.ErrLeadershipLost):
		return fmt.Errorf("%w: %w", errUnknown, err)
	}

	return err
}

func applyOutcome(err error) error {
	switch {
	case err == nil, errors.Is(err, raft.ErrNotLeader), errors.Is(err, raft.ErrLeadershipTransferInProgress),
		errors.Is(err, raft.ErrEnqueueTimeout), errors.Is(err, raft.ErrLeadershipLost):
		return err
	}

	return fmt.Errorf("%w: %w", errUnknown, err)
}

type proposeRequest struct {
	Commands []db.Command `json:"commands"`
}

type proposeReply struct {
	Results []db.Result `json:"results"`
	Index   uint64      `json:"index,omitempty"`
	Busy    bool        `json:"busy,omitempty"`
}

type rpcError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

type peerClient struct {
	n         *Node
	transport *http.Transport
	client    *http.Client
	streams   *http.Client
}

func newPeerClient(n *Node) *peerClient {
	p := &peerClient{n: n}
	p.transport = &http.Transport{
		DialTLSContext:        p.dial,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       time.Minute,
		ResponseHeaderTimeout: rpcTimeout,
	}
	p.client = &http.Client{Transport: p.transport, Timeout: rpcTimeout}
	p.streams = &http.Client{Transport: p.transport}

	return p
}

func (p *peerClient) close() {
	p.transport.CloseIdleConnections()
}

type leaderKey struct{}

func (p *peerClient) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	conn, peer, err := p.n.ln.Dial(ctx, addr, listener.ALPNHTTP, nil)
	if err != nil {
		return nil, &dialError{err}
	}

	if want, ok := ctx.Value(leaderKey{}).(string); ok && want != "" && peer != want {
		_ = conn.Close()
		return nil, &dialError{fmt.Errorf("%s is node %s, not %s", addr, peer, want)}
	}

	return conn, nil
}

func (p *peerClient) leader() (string, string, error) {
	r, _ := p.n.current()

	addr, id := r.LeaderWithID()
	if id == "" {
		return "", "", fmt.Errorf("%w: no leader", errRetry)
	}

	return string(addr), string(id), nil
}

func (p *peerClient) call(ctx context.Context, path string, in, out any) error {
	addr, id, err := p.leader()
	if err != nil {
		return err
	}

	return p.callAt(ctx, addr, id, path, in, out)
}

func (p *peerClient) callAt(ctx context.Context, addr, id, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}

	resp, err := p.do(ctx, addr, id, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	return decodeReply(resp, out)
}

func (p *peerClient) do(ctx context.Context, addr, id, method, path string, body io.Reader) (*http.Response, error) {
	if _, ok := ctx.Deadline(); ok {
		return p.send(ctx, p.streams, addr, id, method, path, body)
	}

	return p.send(ctx, p.client, addr, id, method, path, body)
}

func (p *peerClient) stream(ctx context.Context, addr, id, method, path string, body io.Reader) (*http.Response, error) {
	return p.send(ctx, p.streams, addr, id, method, path, body)
}

func (p *peerClient) send(ctx context.Context, client *http.Client, addr, id, method, path string,
	body io.Reader,
) (*http.Response, error) {
	ctx = context.WithValue(ctx, leaderKey{}, id)

	req, err := http.NewRequestWithContext(ctx, method, "https://"+addr+path, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)

	var dial *dialError

	switch {
	case errors.As(err, &dial):
		return nil, fmt.Errorf("%w: %w", errRetry, err)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", errUnknown, err)
	case resp.StatusCode == http.StatusGone:
		var e rpcError

		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		_ = resp.Body.Close()

		if e.Code != codeRemoved {
			return nil, &RemoteError{Status: resp.StatusCode, Code: e.Code, Message: e.Error}
		}

		p.n.fence(ctx, id)

		return nil, ErrRemoved
	}

	return resp, nil
}

func decodeReply(resp *http.Response, out any) error {
	switch resp.StatusCode {
	case http.StatusOK:
		if out == nil {
			return nil
		}

		return json.NewDecoder(io.LimitReader(resp.Body, maxRPCBody)).Decode(out)
	case http.StatusMisdirectedRequest, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		var e rpcError

		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)

		if e.Code == codeUnknown {
			return fmt.Errorf("%w: %s", errUnknown, e.Error)
		}

		return fmt.Errorf("%w: %s: %s", errRetry, resp.Status, e.Error)
	}

	var e rpcError

	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)

	return &RemoteError{Status: resp.StatusCode, Code: e.Code, Message: e.Error}
}

type RemoteError struct {
	Status  int
	Code    string
	Message string
}

func (e *RemoteError) Error() string {
	return e.Message
}

func (e *RemoteError) Unwrap() error {
	switch e.Code {
	case "":
		return nil
	case codeInvalidBackup:
		return ErrInvalidBackup
	case codeInvalidJoin:
		return ErrInvalidJoin
	case codeNotDrained:
		return ErrNotDrained
	case codeQuorum:
		return ErrQuorum
	}

	return db.Result{Code: e.Code, Message: e.Message}.Err()
}

type dialError struct{ err error }

func (e *dialError) Error() string { return e.err.Error() }

func (e *dialError) Unwrap() error { return e.err }
