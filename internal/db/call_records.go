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
	NodeID          string
}

type CallRecordFilter struct {
	Search     string
	Start, End time.Time
	Outcomes   []CallOutcome
}

const (
	callRecordColumns = `id, icid, session_id, calling_party, caller_impi, requested_party, called_party, callee_impi,
		requested_at, delivery_start_at, delivery_end_at, sip_status, outcome, ended_by, alerted, media, incomplete, in_trunk_id,
		in_trunk_name, out_trunk_id, out_trunk_name, node_id`

	callRecordBatch = 1000
)

type saveCallRecordsCmd struct {
	Records []CallRecord
	Deleted []int64
}

type savedCallRecord struct {
	ID      int64  `json:"i,omitempty"`
	Code    string `json:"c,omitempty"`
	Message string `json:"m,omitempty"`
}

func (d *DB) SaveCallRecords(ctx context.Context, records []*CallRecord, deleted []int64) (errs []error, err error) {
	ctx, done := d.observe(ctx, "SaveCallRecords", poolWrite)
	defer done(&err)

	p := saveCallRecordsCmd{Records: make([]CallRecord, len(records)), Deleted: deleted}
	for i, r := range records {
		p.Records[i] = *r
	}

	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("save call records: %w", err)
	}

	ctx = WithRequestID(ctx, RequestIDOf(body))

	saved, err := run[[]savedCallRecord](ctx, d, cmdSaveCallRecords, "save call records", p)
	if err != nil {
		return nil, err
	}

	if len(saved) != len(records) {
		return nil, fmt.Errorf("save call records: %d results for %d records", len(saved), len(records))
	}

	for i, s := range saved {
		if s.Code == "" {
			records[i].ID = s.ID
			continue
		}

		if errs == nil {
			errs = make([]error, len(records))
		}

		errs[i] = fmt.Errorf("save call record %s: %w", records[i].ICID, Result{Code: s.Code, Message: s.Message}.Err())
	}

	return errs, nil
}

func applySaveCallRecords(a *applyCtx, p saveCallRecordsCmd) ([]savedCallRecord, error) {
	saved := make([]savedCallRecord, len(p.Records))

	for i := range p.Records {
		r := &p.Records[i]

		switch err := saveCallRecord(a.ctx, a.tx, r); {
		case err == nil:
			saved[i].ID = r.ID
		case isRecordError(err):
			code, _ := codeOf(err)
			saved[i] = savedCallRecord{Code: code, Message: err.Error()}
		default:
			return nil, err
		}
	}

	if len(p.Deleted) > 0 {
		ids, err := json.Marshal(p.Deleted)
		if err != nil {
			return nil, err
		}

		if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM call_records WHERE id IN (SELECT value FROM json_each(?))`,
			string(ids)); err != nil {
			return nil, err
		}
	}

	return saved, nil
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
		ended_by, alerted, media, incomplete, in_trunk_id, in_trunk_name, out_trunk_id, out_trunk_name, node_id) VALUES (`+placeholders(21)+`) RETURNING id`,
		append([]any{
			r.ICID, r.SessionID, nullableString(r.CallerIMPI), r.RequestedParty,
			r.RequestedAt.UTC().UnixNano(),
		}, append(mutable, r.NodeID)...)...).Scan(&r.ID)
	if isConstraint(err, sqlite3.ErrConstraintUnique) {
		return ErrDuplicateICID
	}

	return err
}

func (d *DB) GetCallRecord(ctx context.Context, id int64) (_ CallRecord, err error) {
	ctx, done := d.observe(ctx, "GetCallRecord", poolRead)
	defer done(&err)

	r, err := scanCallRecord(d.reader().QueryRowContext(ctx, `SELECT `+callRecordColumns+` FROM call_records WHERE id = ?`,
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
	ctx, done := d.observe(ctx, "ListCallRecords", poolRead)
	defer done(&err)

	where, args := f.where()

	tx, err := d.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
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
	ctx, done := d.observe(ctx, "CountCallRecords", poolRead)
	defer done(&err)

	where, args := f.where()

	rows, err := d.reader().QueryContext(ctx, `SELECT outcome, COUNT(*) FROM call_records WHERE `+where+
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

type pruneCallRecords struct {
	Before  int64
	MaxRows int
}

func (d *DB) PruneCallRecords(ctx context.Context, before time.Time, maxRows int) (_ int64, err error) {
	ctx, done := d.observe(ctx, "PruneCallRecords", poolWrite)
	defer done(&err)

	return run[int64](ctx, d, cmdPruneCallRecords, "prune call records", pruneCallRecords{
		Before: before.UTC().UnixNano(), MaxRows: maxRows,
	})
}

func applyPruneCallRecords(a *applyCtx, p pruneCallRecords) (int64, error) {
	var deleted int64

	deleteOldest := func(where string, limit int, args ...any) (int64, error) {
		res, err := a.tx.ExecContext(a.ctx, `DELETE FROM call_records WHERE id IN (SELECT id FROM call_records WHERE `+
			where+` ORDER BY requested_at, id LIMIT ?)`, append(args, limit)...)
		if err != nil {
			return 0, err
		}

		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}

		deleted += n

		return n, nil
	}

	for {
		n, err := deleteOldest(`requested_at < ?`, callRecordBatch, p.Before)
		if err != nil {
			return 0, err
		}

		if n < callRecordBatch {
			break
		}
	}

	var count int
	if err := a.tx.QueryRowContext(a.ctx, `SELECT COUNT(*) FROM call_records`).Scan(&count); err != nil {
		return 0, err
	}

	for excess := count - p.MaxRows; excess > 0; {
		n, err := deleteOldest(`1`, min(excess, callRecordBatch))
		if err != nil {
			return 0, err
		}

		if n == 0 {
			break
		}

		excess -= int(n)
	}

	return deleted, nil
}

type closeOpenCallRecords struct {
	NodeID string
	Live   []string
}

func (d *DB) CloseOpenCallRecords(ctx context.Context, nodeID string, live []string) (_ int64, err error) {
	ctx, done := d.observe(ctx, "CloseOpenCallRecords", poolWrite)
	defer done(&err)

	return run[int64](ctx, d, cmdCloseOpenCallRecords, "close open call records", closeOpenCallRecords{
		NodeID: nodeID, Live: nonNilList(live),
	})
}

func applyCloseOpenCallRecords(a *applyCtx, p closeOpenCallRecords) (int64, error) {
	return closeCallRecordsOf(a, p.NodeID, p.Live)
}

func closeCallRecordsOf(a *applyCtx, nodeID string, live []string) (int64, error) {
	icids, err := json.Marshal(nonNilList(live))
	if err != nil {
		return 0, err
	}

	res, err := a.tx.ExecContext(a.ctx, `UPDATE call_records SET incomplete = 1 WHERE node_id = ? AND ended_by IS NULL AND
		incomplete = 0 AND icid NOT IN (SELECT value FROM json_each(?))`, nodeID, string(icids))
	if err != nil {
		return 0, err
	}

	return res.RowsAffected()
}

func init() {
	register(cmdSaveCallRecords, handle(applySaveCallRecords))
	register(cmdPruneCallRecords, handle(applyPruneCallRecords))
	register(cmdCloseOpenCallRecords, handle(applyCloseOpenCallRecords))
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
		&r.Incomplete, &inTrunk, &inTrunkName, &outTrunk, &outTrunkName, &r.NodeID); err != nil {
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
