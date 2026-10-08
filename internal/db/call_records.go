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

// CallOutcome is how a call ended, from its final status, who ended it and whether a device rang.
type CallOutcome string

const (
	OutcomeAnswered  CallOutcome = "answered"
	OutcomeCancelled CallOutcome = "cancelled"
	OutcomeBusy      CallOutcome = "busy"
	OutcomeRejected  CallOutcome = "rejected"
	OutcomeNoAnswer  CallOutcome = "no_answer"
	OutcomeFailed    CallOutcome = "failed"
)

// CallParty is the side that ended a call.
type CallParty string

const (
	PartyCaller  CallParty = "caller"
	PartyCallee  CallParty = "callee"
	PartyNetwork CallParty = "network"
)

// CallRecord is what the IMS observed of one call attempt, identified by its ICID. The fields follow the IMS CDR
// parameters of TS 32.298 §5.1.3.1. Zero values are absent: a call in progress has no SIPStatus until its final
// response, and no EndedBy until it ends.
type CallRecord struct {
	ID int64
	// ICID is the icid-value of the P-Charging-Vector (§5.1.3.1.19).
	ICID string
	// SessionID is the Call-ID of the INVITE (§5.1.3.1.59).
	SessionID string
	// FromAddress is the From header field of the INVITE from the UE (§5.1.3.1.16A).
	FromAddress string
	// CallingParty is the P-Asserted-Identity of the INVITE (§5.1.3.1.24), empty when nothing was asserted.
	CallingParty []string
	// CallerIMPI is the private identity of the caller's registration (§5.1.3.1.36).
	CallerIMPI string
	// RequestedParty is the Request-URI as received from the UE (§5.1.3.1.43).
	RequestedParty string
	// CalledParty is the Request-URI the originating S-CSCF sent on (§5.1.3.1.9).
	CalledParty string
	// CalledAsserted is the P-Asserted-Identity of the 2xx (§5.1.3.1.23).
	CalledAsserted []string
	// CalleeIMPI is the private identity of the registration that answered (§5.1.3.1.36).
	CalleeIMPI string
	// RequestedAt is when the INVITE was received from the UE (§5.1.3.1.58).
	RequestedAt time.Time
	// DeliveryStartAt is when the final response was sent toward the caller (§5.1.3.1.55).
	DeliveryStartAt time.Time
	// DeliveryEndAt is when an answered call ended (§5.1.3.1.54).
	DeliveryEndAt time.Time
	// SIPStatus is the final status of the INVITE.
	SIPStatus int
	Outcome   CallOutcome
	EndedBy   CallParty
	// Alerted reports whether a 180 reached the caller.
	Alerted bool
	// ReasonHeaders are those of the BYE or CANCEL that ended the call (§5.1.3.1.28A).
	ReasonHeaders []string
	// Media are the media types of the m= lines that an SDP answer accepted, derived from §5.1.3.1.49.
	Media []string
	// Incomplete reports that the record was closed without the end of its call, which the IMS lost.
	Incomplete bool
}

// CallRecordFilter selects call records. Its zero value selects all.
type CallRecordFilter struct {
	// Search matches a part of an identity of the caller or the callee, or of the ICID.
	Search string
	// From and To bound when the calls were requested, in [From, To). A zero bound is open.
	From, To time.Time
	// Outcomes, if any, are the outcomes to select.
	Outcomes []CallOutcome
}

const (
	callRecordColumns = `id, icid, session_id, from_address, calling_party, caller_impi, requested_party, called_party,
		called_asserted, callee_impi, requested_at, delivery_start_at, delivery_end_at, sip_status, outcome, ended_by,
		alerted, reason_headers, media, incomplete`

	// callRecordBatch is how many records a statement deletes or an export reads at a time, so that the SIP
	// handling waiting on the database connection is not held up for long.
	callRecordBatch = 1000
)

// SaveCallRecords inserts the records without an ID, giving them one, and updates the others, in one transaction.
// A record that cannot be saved does not keep the others from being saved: its error is returned with theirs. An
// update leaves the fields that never change after the record is inserted, such as its ICID, as they were.
func (d *DB) SaveCallRecords(ctx context.Context, records ...*CallRecord) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save call records: %w", err)
	}

	var (
		errs     []error
		inserted []*CallRecord
	)

	for _, r := range records {
		insert := r.ID == 0

		if err := saveCallRecord(ctx, tx, r); err != nil {
			errs = append(errs, fmt.Errorf("save call record %s: %w", r.ICID, err))
		} else if insert {
			inserted = append(inserted, r)
		}
	}

	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()

		for _, r := range inserted {
			r.ID = 0
		}

		return fmt.Errorf("save call records: %w", err)
	}

	return errors.Join(errs...)
}

func saveCallRecord(ctx context.Context, tx *sql.Tx, r *CallRecord) error {
	calling, err := jsonList(r.CallingParty)
	if err != nil {
		return err
	}

	asserted, err := jsonList(r.CalledAsserted)
	if err != nil {
		return err
	}

	reasons, err := jsonList(r.ReasonHeaders)
	if err != nil {
		return err
	}

	media, err := jsonList(r.Media)
	if err != nil {
		return err
	}

	mutable := []any{
		calling, nullableString(r.CalledParty), asserted, nullableString(r.CalleeIMPI), nullableTime(r.DeliveryStartAt),
		nullableTime(r.DeliveryEndAt), nullableInt(int64(r.SIPStatus)), nullableString(string(r.Outcome)),
		nullableString(string(r.EndedBy)), r.Alerted, reasons, media, r.Incomplete,
	}

	if r.ID != 0 {
		res, err := tx.ExecContext(ctx, `UPDATE call_records SET calling_party = ?, called_party = ?,
			called_asserted = ?, callee_impi = ?, delivery_start_at = ?, delivery_end_at = ?, sip_status = ?,
			outcome = ?, ended_by = ?, alerted = ?, reason_headers = ?, media = ?, incomplete = ? WHERE id = ?`,
			append(mutable, r.ID)...)
		if err != nil {
			return err
		}

		return checkAffected(res)
	}

	err = tx.QueryRowContext(ctx, `INSERT INTO call_records (icid, session_id, from_address, caller_impi,
		requested_party, requested_at, calling_party, called_party, called_asserted, callee_impi, delivery_start_at,
		delivery_end_at, sip_status, outcome, ended_by, alerted, reason_headers, media, incomplete)
		VALUES (`+placeholders(19)+`) RETURNING id`,
		append([]any{
			r.ICID, r.SessionID, r.FromAddress, nullableString(r.CallerIMPI), r.RequestedParty,
			r.RequestedAt.UTC().UnixNano(),
		}, mutable...)...).Scan(&r.ID)
	if isConstraint(err, sqlite3.ErrConstraintUnique) {
		return ErrDuplicateICID
	}

	return err
}

func (d *DB) GetCallRecord(ctx context.Context, id int64) (CallRecord, error) {
	r, err := scanCallRecord(d.conn.QueryRowContext(ctx, `SELECT `+callRecordColumns+` FROM call_records WHERE id = ?`,
		id))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}

	if err != nil {
		return CallRecord{}, fmt.Errorf("get call record %d: %w", id, err)
	}

	return r, nil
}

// ListCallRecords returns a page of the records the filter selects, the most recently requested first, and their
// count.
func (d *DB) ListCallRecords(ctx context.Context, f CallRecordFilter, page, perPage int) ([]CallRecord, int, error) {
	where, args := f.where()

	var total int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM call_records WHERE `+where, args...).Scan(
		&total); err != nil {
		return nil, 0, fmt.Errorf("list call records: %w", err)
	}

	records, err := d.queryCallRecords(ctx, `SELECT `+callRecordColumns+` FROM call_records WHERE `+where+`
		ORDER BY requested_at DESC, id DESC LIMIT ? OFFSET ?`, append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list call records: %w", err)
	}

	return records, total, nil
}

// StreamCallRecords calls fn with each record the filter selects, the most recently requested first, until fn
// fails. It reads them in batches, so that the database is free for others while fn runs: a record requested
// after the stream started is not included, and one deleted meanwhile may not be.
func (d *DB) StreamCallRecords(ctx context.Context, f CallRecordFilter, fn func(CallRecord) error) error {
	where, args := f.where()

	var last *CallRecord

	for {
		query, qargs := `SELECT `+callRecordColumns+` FROM call_records WHERE `+where, args

		if last != nil {
			at := last.RequestedAt.UnixNano()
			query += ` AND (requested_at < ? OR requested_at = ? AND id < ?)`

			qargs = append(qargs[:len(qargs):len(qargs)], at, at, last.ID)
		}

		records, err := d.queryCallRecords(ctx, query+` ORDER BY requested_at DESC, id DESC LIMIT ?`,
			append(qargs[:len(qargs):len(qargs)], callRecordBatch)...)
		if err != nil {
			return fmt.Errorf("stream call records: %w", err)
		}

		for _, r := range records {
			if err := fn(r); err != nil {
				return err
			}
		}

		if len(records) < callRecordBatch {
			return nil
		}

		last = &records[len(records)-1]
	}
}

// PruneCallRecords deletes the records requested before a time, then the oldest beyond maxRows, and returns how
// many it deleted. It deletes them in batches, so that the SIP handling waiting on the database connection is not
// held up for long.
func (d *DB) PruneCallRecords(ctx context.Context, before time.Time, maxRows int) (int64, error) {
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
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM call_records`).Scan(&count); err != nil {
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

// CloseOpenCallRecords marks the records of the calls that have not ended as incomplete, since the IMS lost them,
// and returns how many it marked.
func (d *DB) CloseOpenCallRecords(ctx context.Context) (int64, error) {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE call_records SET incomplete = 1 WHERE ended_by IS NULL AND incomplete = 0`)
	if err != nil {
		return 0, fmt.Errorf("close open call records: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("close open call records: %w", err)
	}

	return n, nil
}

// where is the condition of the filter, and its arguments.
func (f CallRecordFilter) where() (string, []any) {
	conds := []string{"1"}

	var args []any

	if f.Search != "" {
		like := "%" + escapeLike(f.Search) + "%"
		inList := func(column string) string {
			return `EXISTS (SELECT 1 FROM json_each(` + column + `) WHERE value LIKE ? ESCAPE '\')`
		}

		conds = append(conds, `(icid LIKE ? ESCAPE '\' OR from_address LIKE ? ESCAPE '\' OR caller_impi LIKE ? ESCAPE '\'
			OR requested_party LIKE ? ESCAPE '\' OR called_party LIKE ? ESCAPE '\' OR callee_impi LIKE ? ESCAPE '\'
			OR `+inList("calling_party")+` OR `+inList("called_asserted")+`)`)
		args = append(args, like, like, like, like, like, like, like, like)
	}

	if !f.From.IsZero() {
		conds = append(conds, `requested_at >= ?`)
		args = append(args, f.From.UTC().UnixNano())
	}

	if !f.To.IsZero() {
		conds = append(conds, `requested_at < ?`)
		args = append(args, f.To.UTC().UnixNano())
	}

	if len(f.Outcomes) > 0 {
		conds = append(conds, `outcome IN (`+placeholders(len(f.Outcomes))+`)`)
		for _, o := range f.Outcomes {
			args = append(args, string(o))
		}
	}

	return strings.Join(conds, " AND "), args
}

func (d *DB) queryCallRecords(ctx context.Context, query string, args ...any) ([]CallRecord, error) {
	rows, err := d.conn.QueryContext(ctx, query, args...)
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
		calling, asserted, reasons, media         sql.NullString
		outcome, endedBy                          sql.NullString
		requestedAt                               int64
		deliveryStartAt, deliveryEndAt, sipStatus sql.NullInt64
	)

	if err := row.Scan(&r.ID, &r.ICID, &r.SessionID, &r.FromAddress, &calling, &callerIMPI, &r.RequestedParty,
		&calledParty, &asserted, &calleeIMPI, &requestedAt, &deliveryStartAt, &deliveryEndAt, &sipStatus, &outcome,
		&endedBy, &r.Alerted, &reasons, &media, &r.Incomplete); err != nil {
		return CallRecord{}, err
	}

	r.CallerIMPI, r.CalledParty, r.CalleeIMPI = callerIMPI.String, calledParty.String, calleeIMPI.String
	r.RequestedAt = time.Unix(0, requestedAt).UTC()
	r.DeliveryStartAt, r.DeliveryEndAt = timeOf(deliveryStartAt), timeOf(deliveryEndAt)
	r.SIPStatus = int(sipStatus.Int64)
	r.Outcome, r.EndedBy = CallOutcome(outcome.String), CallParty(endedBy.String)

	for _, l := range []struct {
		dst *[]string
		src sql.NullString
	}{{&r.CallingParty, calling}, {&r.CalledAsserted, asserted}, {&r.ReasonHeaders, reasons}, {&r.Media, media}} {
		if !l.src.Valid {
			continue
		}

		if err := json.Unmarshal([]byte(l.src.String), l.dst); err != nil {
			return CallRecord{}, fmt.Errorf("call record %d: %w", r.ID, err)
		}
	}

	return r, nil
}

// jsonList is a list as a JSON array, or NULL if empty.
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
