package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// RFC 6733 section 3
const appliedRequestsWindow = 5 * time.Minute

type Events struct {
	Diverged bool
	Replaced bool
	Settings bool
	OIDC     bool
	MCPTT    bool
	Members  bool
	Removed  []string
	Migrated int
}

func (e *Events) merge(o Events) {
	e.Diverged = e.Diverged || o.Diverged
	e.Replaced = e.Replaced || o.Replaced
	e.Settings = e.Settings || o.Settings
	e.OIDC = e.OIDC || o.OIDC
	e.MCPTT = e.MCPTT || o.MCPTT
	e.Members = e.Members || o.Members
	e.Migrated = max(e.Migrated, o.Migrated)
	e.Removed = append(e.Removed, o.Removed...)
}

func (e Events) empty() bool {
	return !e.Diverged && !e.Replaced && !e.Settings && !e.OIDC && !e.MCPTT && !e.Members && e.Migrated == 0 && len(e.Removed) == 0
}

type applyCtx struct {
	ctx        context.Context
	schema     int
	tx         *sql.Tx
	index      uint64
	term       uint64
	leaderTime int64
	events     *Events
}

type handler func(a *applyCtx, payload json.RawMessage) (any, error)

func handle[P, R any](fn func(a *applyCtx, p P) (R, error)) handler {
	return func(a *applyCtx, payload json.RawMessage) (any, error) {
		var p P
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}

		return fn(a, p)
	}
}

type Proposer interface {
	Propose(ctx context.Context, cmds []Command) ([]Result, error)
}

type FatalError struct {
	Index uint64
	Err   error
}

func (e *FatalError) Error() string {
	return fmt.Sprintf("apply entry %d: %v", e.Index, e.Err)
}

func (e *FatalError) Unwrap() error { return e.Err }

type fsmState struct {
	appliedIndex uint64
	appliedTerm  uint64
	hash         []byte
	leaderTime   int64
}

func readFSMState(ctx context.Context, q querier) (fsmState, error) {
	var s fsmState

	err := q.QueryRowContext(ctx, `SELECT applied_index, applied_term, hash, leader_time FROM fsm_state WHERE id = 1`).
		Scan(&s.appliedIndex, &s.appliedTerm, &s.hash, &s.leaderTime)

	return s, err
}

func (d *DB) AppliedIndex() uint64 {
	return d.applied.Load()
}

func (d *DB) setApplied(index uint64) {
	d.applied.Store(index)

	d.appliedMu.Lock()
	if d.appliedCh != nil {
		close(d.appliedCh)
		d.appliedCh = nil
	}
	d.appliedMu.Unlock()
}

func (d *DB) WaitApplied(ctx context.Context, index uint64) error {
	for {
		d.appliedMu.Lock()

		if d.applied.Load() >= index {
			d.appliedMu.Unlock()
			return nil
		}

		if d.appliedCh == nil {
			d.appliedCh = make(chan struct{})
		}

		ch := d.appliedCh
		d.appliedMu.Unlock()

		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (d *DB) LeaderTime() int64 {
	return d.leaderTime.Load()
}

const recentHashes = 4096

type appliedHash struct {
	index uint64
	hash  []byte
}

func (d *DB) remember(index uint64, hash []byte) {
	d.hashMu.Lock()
	defer d.hashMu.Unlock()

	if d.hashes == nil {
		d.hashes = make([]appliedHash, recentHashes)
	}

	d.hashes[index%recentHashes] = appliedHash{index: index, hash: hash}
}

func (d *DB) differs(c HashCheck, local map[uint64][]byte) bool {
	mine, ok := local[c.Index]
	if !ok {
		if mine, ok = d.HashAt(c.Index); !ok {
			return false
		}
	}

	if bytes.Equal(mine, c.Hash) {
		return false
	}

	d.diverged.Store(true)

	return true
}

func (d *DB) Diverged() bool {
	return d.diverged.Load()
}

func (d *DB) Check() *HashCheck {
	index := d.AppliedIndex()

	h, ok := d.HashAt(index)
	if !ok {
		return nil
	}

	return &HashCheck{Index: index, Hash: h}
}

func (d *DB) HashAt(index uint64) ([]byte, bool) {
	d.hashMu.Lock()
	defer d.hashMu.Unlock()

	if d.hashes == nil {
		return nil, false
	}

	h := d.hashes[index%recentHashes]

	return h.hash, h.index == index && h.hash != nil
}

func (d *DB) Hash(ctx context.Context) (_ []byte, _ uint64, err error) {
	defer d.observe(poolRead, &err)()

	s, err := readFSMState(ctx, d.reader())
	if err != nil {
		return nil, 0, fmt.Errorf("read state: %w", err)
	}

	return s.hash, s.appliedIndex, nil
}

type Entry struct {
	Index uint64
	Term  uint64
	Data  []byte
}

func (d *DB) Apply(index, term uint64, entry []byte) ([]Result, error) {
	results, err := d.ApplyEntries([]Entry{{Index: index, Term: term, Data: entry}})
	if err != nil {
		return nil, err
	}

	return results[0], nil
}

func (d *DB) ApplyEntries(entries []Entry) ([][]Result, error) {
	batches := make([]Batch, len(entries))

	for i, e := range entries {
		b, err := DecodeBatch(e.Data)
		if err != nil {
			return nil, &FatalError{Index: e.Index, Err: err}
		}

		batches[i] = b
	}

	d.applyMu.Lock()

	results, events, err := d.applyBatches(entries, batches)

	d.applyMu.Unlock()

	if err != nil {
		return nil, err
	}

	d.publish(events)

	return results, nil
}

func (d *DB) applyBatches(entries []Entry, batches []Batch) ([][]Result, Events, error) {
	ctx := context.Background()

	fatal := func(i int, err error) ([][]Result, Events, error) {
		return nil, Events{}, &FatalError{Index: entries[i].Index, Err: err}
	}

	tx, err := d.writer.BeginTx(ctx, nil)
	if err != nil {
		return fatal(0, err)
	}

	defer func() { _ = tx.Rollback() }()

	state, err := readFSMState(ctx, tx)
	if err != nil {
		return fatal(0, err)
	}

	var schema int

	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return fatal(0, err)
	}

	results := make([][]Result, len(entries))
	hashes := make([][]byte, len(entries))

	var events Events

	applied := state.appliedIndex
	local := map[uint64][]byte{}

	for i, e := range entries {
		b := batches[i]
		results[i] = make([]Result, len(b.Commands))

		if e.Index <= applied {
			for j := range results[i] {
				results[i][j] = Result{Code: "already_applied", Message: ErrAlreadyApplied.Error()}
			}

			continue
		}

		state.leaderTime = max(state.leaderTime, b.LeaderTime)

		for j, c := range b.Commands {
			a := &applyCtx{ctx: ctx, tx: tx, index: e.Index, term: e.Term, leaderTime: state.leaderTime, schema: schema}

			r, ev, err := applyCommand(a, c)
			if err != nil {
				return fatal(i, fmt.Errorf("command %d (type %d): %w", j, c.Type, err))
			}

			results[i][j] = r

			events.merge(ev)

			if ev.Migrated > 0 {
				schema = ev.Migrated
			}

			state.hash = chain(state.hash, e.Index, c, r)
		}

		hashes[i] = state.hash
		local[e.Index] = state.hash
		state.appliedIndex, state.appliedTerm = e.Index, e.Term

		if b.Check != nil && d.differs(*b.Check, local) {
			events.Diverged = true
		}
	}

	if state.appliedIndex == applied {
		return results, Events{}, nil
	}

	last := len(entries) - 1

	if _, err := tx.ExecContext(ctx, `DELETE FROM applied_requests WHERE applied_at < ?`,
		state.leaderTime-int64(appliedRequestsWindow)); err != nil {
		return fatal(last, err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE fsm_state SET applied_index = ?, applied_term = ?, hash = ?,
		leader_time = ? WHERE id = 1`, state.appliedIndex, state.appliedTerm, state.hash, state.leaderTime); err != nil {
		return fatal(last, err)
	}

	if err := tx.Commit(); err != nil {
		return fatal(last, err)
	}

	d.setApplied(state.appliedIndex)
	d.leaderTime.Store(state.leaderTime)

	if events.Migrated > 0 {
		d.schema.Store(int64(events.Migrated))
	}

	for i, e := range entries {
		if hashes[i] != nil {
			d.remember(e.Index, hashes[i])
		}
	}

	return results, events, nil
}

func applyCommand(a *applyCtx, c Command) (Result, Events, error) {
	ctx, tx := a.ctx, a.tx

	h, ok := handlers[c.Type]
	if !ok || c.Version != 1 {
		return Result{}, Events{}, fmt.Errorf("unknown command type %d version %d", c.Type, c.Version)
	}

	if need := commandSchema(c.Type); need > a.schema {
		return Result{}, Events{}, fmt.Errorf("command type %d needs schema %d, the database has %d", c.Type, need,
			a.schema)
	}

	if c.Retryable {
		var stored []byte

		err := tx.QueryRowContext(ctx, `SELECT result FROM applied_requests WHERE request_id = ? AND applied_at >= ?`,
			c.RequestID[:], a.leaderTime-int64(appliedRequestsWindow)).Scan(&stored)
		if err == nil {
			var r Result
			if err := json.Unmarshal(stored, &r); err != nil {
				return Result{}, Events{}, err
			}

			r.Replayed = true

			return r, Events{}, nil
		}

		if !errors.Is(err, sql.ErrNoRows) {
			return Result{}, Events{}, err
		}
	}

	if _, err := tx.ExecContext(ctx, `SAVEPOINT command`); err != nil {
		return Result{}, Events{}, err
	}

	var events Events

	a.events = &events

	value, herr := h(a, c.Payload)

	var r Result

	if herr != nil {
		code, ok := codeOf(herr)
		if !ok {
			return Result{}, Events{}, herr
		}

		if _, err := tx.ExecContext(ctx, `ROLLBACK TO command`); err != nil {
			return Result{}, Events{}, err
		}

		events = Events{}
		r = Result{Code: code, Message: herr.Error()}
	} else if value != nil {
		v, err := json.Marshal(value)
		if err != nil {
			return Result{}, Events{}, err
		}

		r.Value = v
	}

	if _, err := tx.ExecContext(ctx, `RELEASE command`); err != nil {
		return Result{}, Events{}, err
	}

	if c.Retryable {
		stored, err := json.Marshal(r)
		if err != nil {
			return Result{}, Events{}, err
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO applied_requests (request_id, result, applied_at)
			VALUES (?, ?, ?) ON CONFLICT (request_id) DO UPDATE SET result = excluded.result,
			applied_at = excluded.applied_at`, c.RequestID[:], stored, a.leaderTime); err != nil {
			return Result{}, Events{}, err
		}
	}

	return r, events, nil
}

func chain(prev []byte, index uint64, c Command, r Result) []byte {
	h := sha256.New()
	h.Write(prev)

	var head [12]byte
	binary.BigEndian.PutUint64(head[:8], index)
	binary.BigEndian.PutUint16(head[8:10], uint16(c.Type))
	binary.BigEndian.PutUint16(head[10:], c.Version)
	h.Write(head[:])
	h.Write(c.RequestID[:])

	h.Write([]byte(r.Code))
	h.Write([]byte{0})
	h.Write(r.Value)

	return h.Sum(nil)
}

func (d *DB) publish(ev Events) {
	if ev.empty() {
		return
	}

	for _, f := range d.subscribers {
		f(ev)
	}
}

func (d *DB) OnEvents(f func(Events)) {
	d.subscribers = append(d.subscribers, f)
}

func (d *DB) SetProposer(p Proposer) {
	d.proposer.Store(&proposerRef{p})
}

type proposerRef struct{ Proposer }

func (d *DB) currentProposer() Proposer {
	return d.proposer.Load().Proposer
}

type localProposer struct {
	d    *DB
	now  func() time.Time
	mu   sync.Mutex
	last int64
}

func (l *localProposer) Propose(ctx context.Context, cmds []Command) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.last = max(l.last+1, l.now().UnixNano())

	entry, err := EncodeBatch(Batch{LeaderTime: l.last, Commands: cmds})
	if err != nil {
		return nil, err
	}

	return l.d.Apply(l.d.AppliedIndex()+1, 1, entry)
}

func submit[R any](ctx context.Context, d *DB, t CommandType, payload any) (R, error) {
	if commandSchema(t) > d.Schema() {
		var zero R
		return zero, ErrUpgradePending
	}

	r, _, err := submitVia[R](ctx, d.currentProposer(), t, payload)

	return r, err
}

func submitVia[R any](ctx context.Context, p Proposer, t CommandType, payload any) (R, bool, error) {
	var zero R

	raw, err := json.Marshal(payload)
	if err != nil {
		return zero, false, fmt.Errorf("encode command: %w", err)
	}

	id, retryable := requestID(ctx, t)

	results, err := p.Propose(ctx, []Command{{Type: t, Version: 1, RequestID: id, Retryable: retryable, Payload: raw}})
	if err != nil {
		return zero, false, err
	}

	if len(results) != 1 {
		return zero, false, fmt.Errorf("command %d: %d results", t, len(results))
	}

	if err := results[0].Err(); err != nil {
		return zero, results[0].Replayed, err
	}

	var r R

	if len(results[0].Value) > 0 {
		if err := json.Unmarshal(results[0].Value, &r); err != nil {
			return zero, false, fmt.Errorf("decode result: %w", err)
		}
	}

	return r, results[0].Replayed, nil
}

func run[R any](ctx context.Context, d *DB, t CommandType, what string, payload any) (R, error) {
	r, err := submit[R](ctx, d, t, payload)
	if err != nil {
		var zero R
		return zero, fmt.Errorf("%s: %w", what, err)
	}

	return r, nil
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var handlers = map[CommandType]handler{}

func register(t CommandType, h handler) {
	if _, ok := handlers[t]; ok {
		panic(fmt.Sprintf("command type %d registered twice", t))
	}

	handlers[t] = h
}
