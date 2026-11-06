package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

var ErrDuplicateICID = errors.New("a call record has this ICID")

type CallOutcome string

const (
	OutcomeAnswered    CallOutcome = "answered"
	OutcomeCancelled   CallOutcome = "cancelled"
	OutcomeBusy        CallOutcome = "busy"
	OutcomeRejected    CallOutcome = "rejected"
	OutcomeNoAnswer    CallOutcome = "no_answer"
	OutcomeUnavailable CallOutcome = "unavailable"
	OutcomeFailed      CallOutcome = "failed"
)

type CallParty string

const (
	PartyCaller  CallParty = "caller"
	PartyCallee  CallParty = "callee"
	PartyNetwork CallParty = "network"
)

// TS 32.298 clause 5.1.3.1
type CallRecord struct {
	ID              int64
	ICID            string
	SessionID       string
	CallingParty    []string
	CallerIMPI      string
	RequestedParty  string
	CalledParty     string
	CalleeIMPI      string
	RequestedAt     time.Time
	DeliveryStartAt time.Time
	DeliveryEndAt   time.Time
	SIPStatus       int
	Outcome         CallOutcome
	EndedBy         CallParty
	Alerted         bool
	Media           []string
	Incomplete      bool
	InTrunk         string
	InTrunkName     string
	OutTrunk        string
	OutTrunkName    string
}

type CallRecordFilter struct {
	Search     string
	Start, End time.Time
	Outcomes   []CallOutcome
}

const (
	callRecordColumns = `id, icid, session_id, calling_party, caller_impi, requested_party, called_party, callee_impi,
		requested_at, delivery_start_at, delivery_end_at, sip_status, outcome, ended_by, alerted, media, incomplete, in_trunk_id,
		in_trunk_name, out_trunk_id, out_trunk_name`

	callRecordBatch = 1000
)

func (d *DB) SaveCallRecords(ctx context.Context, records []*CallRecord, deleted []int64) (errs []error, err error) {
	defer d.observe(poolWrite, &err)()

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("save call records: %w", err)
	}

	var inserted []*CallRecord

	fail := func(err error) error {
		_ = tx.Rollback()

		for _, r := range inserted {
			r.ID = 0
		}

		return fmt.Errorf("save call records: %w", err)
	}

	for i, r := range records {
		insert := r.ID == 0

		switch err := saveCallRecord(ctx, tx, r); {
		case err == nil:
			if insert {
				inserted = append(inserted, r)
			}
		case isRecordError(err):
			if errs == nil {
				errs = make([]error, len(records))
			}

			errs[i] = fmt.Errorf("save call record %s: %w", r.ICID, err)
		default:
			return nil, fail(err)
		}
	}

	if len(deleted) > 0 {
		ids, err := json.Marshal(deleted)
		if err != nil {
			return nil, fail(err)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM call_records WHERE id IN (SELECT value FROM json_each(?))`,
			string(ids)); err != nil {
			return nil, fail(err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fail(err)
	}

	return errs, nil
}

func isRecordError(err error) bool {
	var e sqlite3.Error

	return errors.Is(err, ErrDuplicateICID) || errors.Is(err, ErrNotFound) ||
		errors.As(err, &e) && e.Code == sqlite3.ErrConstraint
}

func saveCallRecord(ctx context.Context, tx *sql.Tx, r *CallRecord) error {
	calling, err := jsonList(r.CallingParty)
	if err != nil {
		return err
	}

	media, err := jsonList(r.Media)
	if err != nil {
		return err
	}

	mutable := []any{
		calling, nullableString(r.CalledParty), nullableString(r.CalleeIMPI), nullableTime(r.DeliveryStartAt),
		nullableTime(r.DeliveryEndAt), nullableInt(int64(r.SIPStatus)), nullableString(string(r.Outcome)),
		nullableString(string(r.EndedBy)), r.Alerted, media, r.Incomplete, nullableString(r.InTrunk),
		nullableString(r.InTrunkName), nullableString(r.OutTrunk), nullableString(r.OutTrunkName),
	}

	if r.ID != 0 {
		res, err := tx.ExecContext(ctx, `UPDATE call_records SET calling_party = ?, called_party = ?, callee_impi = ?,
			delivery_start_at = ?, delivery_end_at = ?, sip_status = ?, outcome = ?, ended_by = ?, alerted = ?,
			media = ?, incomplete = ?, in_trunk_id = ?, in_trunk_name = ?, out_trunk_id = ?,
			out_trunk_name = ? WHERE id = ?`,
			append(mutable, r.ID)...)
		if err != nil {
			return err
		}

		return checkAffected(res)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM call_records WHERE session_id = ? AND caller_impi IS ? AND
		sip_status >= 300`, r.SessionID, nullableString(r.CallerIMPI)); err != nil {
		return err
	}

	err = tx.QueryRowContext(ctx, `INSERT INTO call_records (icid, session_id, caller_impi, requested_party,
		requested_at, calling_party, called_party, callee_impi, delivery_start_at, delivery_end_at, sip_status, outcome,
		ended_by, alerted, media, incomplete, in_trunk_id, in_trunk_name, out_trunk_id, out_trunk_name) VALUES (`+placeholders(20)+`) RETURNING id`,
		append([]any{
			r.ICID, r.SessionID, nullableString(r.CallerIMPI), r.RequestedParty,
			r.RequestedAt.UTC().UnixNano(),
		}, mutable...)...).Scan(&r.ID)
	if isConstraint(err, sqlite3.ErrConstraintUnique) {
		return ErrDuplicateICID
	}

	return err
}

func (d *DB) GetCallRecord(ctx context.Context, id int64) (_ CallRecord, err error) {
	defer d.observe(poolRead, &err)()

	r, err := scanCallRecord(d.read.QueryRowContext(ctx, `SELECT `+callRecordColumns+` FROM call_records WHERE id = ?`,
		id))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}

	if err != nil {
		return CallRecord{}, fmt.Errorf("get call record %d: %w", id, err)
	}

	return r, nil
}

func (d *DB) ListCallRecords(ctx context.Context, f CallRecordFilter, page, perPage int) (_ []CallRecord, _ int, err error) {
	defer d.observe(poolRead, &err)()

	where, args := f.where()

	tx, err := d.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, fmt.Errorf("list call records: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM call_records WHERE `+where, args...).Scan(
		&total); err != nil {
		return nil, 0, fmt.Errorf("list call records: %w", err)
	}

	records, err := queryCallRecords(ctx, tx, `SELECT `+callRecordColumns+` FROM call_records WHERE `+where+`
		ORDER BY requested_at DESC, id DESC LIMIT ? OFFSET ?`, append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list call records: %w", err)
	}

	return records, total, nil
}

func (d *DB) CountCallRecords(ctx context.Context, f CallRecordFilter) (_ map[CallOutcome]int, err error) {
	defer d.observe(poolRead, &err)()

	where, args := f.where()

	rows, err := d.read.QueryContext(ctx, `SELECT outcome, COUNT(*) FROM call_records WHERE `+where+
		` AND outcome IS NOT NULL GROUP BY outcome`, args...)
	if err != nil {
		return nil, fmt.Errorf("count call records: %w", err)
	}

	defer func() { _ = rows.Close() }()

	counts := map[CallOutcome]int{}

	for rows.Next() {
		var (
			outcome CallOutcome
			n       int
		)

		if err := rows.Scan(&outcome, &n); err != nil {
			return nil, fmt.Errorf("count call records: %w", err)
		}

		counts[outcome] = n
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count call records: %w", err)
	}

	return counts, nil
}

func (d *DB) PruneCallRecords(ctx context.Context, before time.Time, maxRows int) (_ int64, err error) {
	defer d.observe(poolWrite, &err)()

	var deleted int64

	deleteOldest := func(where string, limit int, args ...any) (int64, error) {
		res, err := d.conn.ExecContext(ctx, `DELETE FROM call_records WHERE id IN (SELECT id FROM call_records WHERE `+
			where+` ORDER BY requested_at, id LIMIT ?)`, append(args, limit)...)
		if err != nil {
			return 0, fmt.Errorf("prune call records: %w", err)
		}

		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("prune call records: %w", err)
		}

		deleted += n

		return n, nil
	}

	for {
		n, err := deleteOldest(`requested_at < ?`, callRecordBatch, before.UTC().UnixNano())
		if err != nil {
			return deleted, err
		}

		if n < callRecordBatch {
			break
		}
	}

	var count int
	if err := d.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM call_records`).Scan(&count); err != nil {
		return deleted, fmt.Errorf("prune call records: %w", err)
	}

	for excess := count - maxRows; excess > 0; {
		n, err := deleteOldest(`1`, min(excess, callRecordBatch))
		if err != nil {
			return deleted, err
		}

		if n == 0 {
			break
		}

		excess -= int(n)
	}

	return deleted, nil
}

func (d *DB) CloseOpenCallRecords(ctx context.Context, live []string) (_ int64, err error) {
	defer d.observe(poolWrite, &err)()

	icids, err := json.Marshal(nonNilList(live))
	if err != nil {
		return 0, fmt.Errorf("close open call records: %w", err)
	}

	res, err := d.conn.ExecContext(ctx, `UPDATE call_records SET incomplete = 1 WHERE ended_by IS NULL AND
		incomplete = 0 AND icid NOT IN (SELECT value FROM json_each(?))`, string(icids))
	if err != nil {
		return 0, fmt.Errorf("close open call records: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("close open call records: %w", err)
	}

	return n, nil
}

func (f CallRecordFilter) where() (string, []any) {
	conds := []string{"1"}

	var args []any

	if f.Search != "" {
		like := "%" + escapeLike(f.Search) + "%"

		conds = append(conds, `(icid LIKE ? ESCAPE '\' OR caller_impi LIKE ? ESCAPE '\' OR requested_party LIKE ? ESCAPE '\'
			OR called_party LIKE ? ESCAPE '\' OR callee_impi LIKE ? ESCAPE '\' OR EXISTS (
				SELECT 1 FROM json_each(calling_party) WHERE value LIKE ? ESCAPE '\'))`)
		args = append(args, like, like, like, like, like, like)
	}

	if !f.Start.IsZero() {
		conds = append(conds, `requested_at >= ?`)
		args = append(args, f.Start.UTC().UnixNano())
	}

	if !f.End.IsZero() {
		conds = append(conds, `requested_at < ?`)
		args = append(args, f.End.UTC().UnixNano())
	}

	if len(f.Outcomes) > 0 {
		conds = append(conds, `outcome IN (`+placeholders(len(f.Outcomes))+`)`)
		for _, o := range f.Outcomes {
			args = append(args, string(o))
		}
	}

	return strings.Join(conds, " AND "), args
}

func queryCallRecords(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]CallRecord, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	records := []CallRecord{}

	for rows.Next() {
		r, err := scanCallRecord(rows)
		if err != nil {
			return nil, err
		}

		records = append(records, r)
	}

	return records, rows.Err()
}

func scanCallRecord(row scanner) (CallRecord, error) {
	var (
		r                                         CallRecord
		callerIMPI, calledParty, calleeIMPI       sql.NullString
		calling, media                            sql.NullString
		outcome, endedBy                          sql.NullString
		inTrunk, inTrunkName                      sql.NullString
		outTrunk, outTrunkName                    sql.NullString
		requestedAt                               int64
		deliveryStartAt, deliveryEndAt, sipStatus sql.NullInt64
	)

	if err := row.Scan(&r.ID, &r.ICID, &r.SessionID, &calling, &callerIMPI, &r.RequestedParty, &calledParty,
		&calleeIMPI, &requestedAt, &deliveryStartAt, &deliveryEndAt, &sipStatus, &outcome, &endedBy, &r.Alerted, &media,
		&r.Incomplete, &inTrunk, &inTrunkName, &outTrunk, &outTrunkName); err != nil {
		return CallRecord{}, err
	}

	r.CallerIMPI, r.CalledParty, r.CalleeIMPI = callerIMPI.String, calledParty.String, calleeIMPI.String
	r.RequestedAt = time.Unix(0, requestedAt).UTC()
	r.DeliveryStartAt, r.DeliveryEndAt = timeOf(deliveryStartAt), timeOf(deliveryEndAt)
	r.SIPStatus = int(sipStatus.Int64)
	r.Outcome, r.EndedBy = CallOutcome(outcome.String), CallParty(endedBy.String)
	r.InTrunk, r.InTrunkName = inTrunk.String, inTrunkName.String
	r.OutTrunk, r.OutTrunkName = outTrunk.String, outTrunkName.String

	for _, l := range []struct {
		dst *[]string
		src sql.NullString
	}{{&r.CallingParty, calling}, {&r.Media, media}} {
		if !l.src.Valid {
			continue
		}

		if err := json.Unmarshal([]byte(l.src.String), l.dst); err != nil {
			return CallRecord{}, fmt.Errorf("call record %d: %w", r.ID, err)
		}
	}

	return r, nil
}

func nonNilList(l []string) []string {
	if l == nil {
		return []string{}
	}

	return l
}

func jsonList(l []string) (sql.NullString, error) {
	if len(l) == 0 {
		return sql.NullString{}, nil
	}

	b, err := json.Marshal(l)
	if err != nil {
		return sql.NullString{}, err
	}

	return sql.NullString{String: string(b), Valid: true}, nil
}

func nullableTime(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}

	return sql.NullInt64{Int64: t.UTC().UnixNano(), Valid: true}
}

func timeOf(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}

	return time.Unix(0, n.Int64).UTC()
}
