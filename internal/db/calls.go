package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type CallOutcome string

const (
	OutcomeAnswered    CallOutcome = "answered"
	OutcomeBusy        CallOutcome = "busy"
	OutcomeNoAnswer    CallOutcome = "no_answer"
	OutcomeCancelled   CallOutcome = "cancelled"
	OutcomeRejected    CallOutcome = "rejected"
	OutcomeNotFound    CallOutcome = "not_found"
	OutcomeUnreachable CallOutcome = "unreachable"
	OutcomeFailed      CallOutcome = "failed"
)

type CallParty string

const (
	PartyCaller  CallParty = "caller"
	PartyCallee  CallParty = "callee"
	PartyNetwork CallParty = "network"
)

// Call is a finished call. AnsweredAt is zero if the call was never answered.
type Call struct {
	ID         int64
	CallID     string
	Caller     string
	Callee     string
	StartedAt  time.Time
	AnsweredAt time.Time
	EndedAt    time.Time
	Outcome    CallOutcome
	SIPStatus  int
	EndedBy    CallParty
	Reason     string
}

// CallFilter selects calls. Number matches the caller or the callee exactly.
type CallFilter struct {
	Number string
}

const callColumns = `id, call_id, caller, callee, started_at, answered_at, ended_at, outcome, sip_status, ended_by, reason`

func (d *DB) CreateCall(ctx context.Context, c Call) (int64, error) {
	var answeredAt sql.Null[int64]
	if !c.AnsweredAt.IsZero() {
		answeredAt = sql.Null[int64]{V: c.AnsweredAt.UTC().UnixNano(), Valid: true}
	}

	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO calls (call_id, caller, callee, started_at, answered_at, ended_at, outcome, sip_status, ended_by, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.CallID, c.Caller, c.Callee, c.StartedAt.UTC().UnixNano(), answeredAt, c.EndedAt.UTC().UnixNano(),
		c.Outcome, c.SIPStatus, c.EndedBy, nullableString(c.Reason))
	if err != nil {
		return 0, fmt.Errorf("create call: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create call: %w", err)
	}

	return id, nil
}

// ListCalls returns calls newest first.
func (d *DB) ListCalls(ctx context.Context, f CallFilter, page, perPage int) ([]Call, int, error) {
	var (
		clause string
		args   []any
	)

	if f.Number != "" {
		clause, args = ` WHERE caller = ? OR callee = ?`, []any{f.Number, f.Number}
	}

	var total int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM calls`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list calls: %w", err)
	}

	rows, err := d.conn.QueryContext(ctx, `SELECT `+callColumns+` FROM calls`+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list calls: %w", err)
	}

	defer func() { _ = rows.Close() }()

	calls := []Call{}

	for rows.Next() {
		var (
			c                  Call
			startedAt, endedAt int64
			answeredAt         sql.Null[int64]
			reason             sql.NullString
		)

		if err := rows.Scan(&c.ID, &c.CallID, &c.Caller, &c.Callee, &startedAt, &answeredAt, &endedAt,
			&c.Outcome, &c.SIPStatus, &c.EndedBy, &reason); err != nil {
			return nil, 0, fmt.Errorf("list calls: %w", err)
		}

		c.StartedAt = time.Unix(0, startedAt).UTC()
		c.EndedAt = time.Unix(0, endedAt).UTC()

		if answeredAt.Valid {
			c.AnsweredAt = time.Unix(0, answeredAt.V).UTC()
		}

		c.Reason = reason.String
		calls = append(calls, c)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list calls: %w", err)
	}

	return calls, total, nil
}

// DeleteCallsEndedBefore deletes calls that ended strictly before t, and
// returns how many were deleted.
func (d *DB) DeleteCallsEndedBefore(ctx context.Context, t time.Time) (int64, error) {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM calls WHERE ended_at < ?`, t.UTC().UnixNano())
	if err != nil {
		return 0, fmt.Errorf("delete calls: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete calls: %w", err)
	}

	return n, nil
}
