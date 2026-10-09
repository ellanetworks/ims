package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/settings"
)

// CallRecords are the records of the calls the IMS keeps.
type CallRecords interface {
	// ListCallRecords returns a page of the records the filter selects, the most recently requested first, and their
	// count.
	ListCallRecords(ctx context.Context, f db.CallRecordFilter, page, perPage int) ([]db.CallRecord, int, error)
	// GetCallRecord returns a record, or an error of the kind db.ErrNotFound.
	GetCallRecord(ctx context.Context, id int64) (db.CallRecord, error)
}

// CallRecordResponse is a call record (TS 32.298 §5.1.3.1). A value the IMS did not observe is absent.
type CallRecordResponse struct {
	ID              int64    `json:"id"`
	ICID            string   `json:"icid"`
	SessionID       string   `json:"session_id"`
	CallingParty    []string `json:"calling_party"`
	CallerIMPI      string   `json:"caller_impi,omitempty"`
	RequestedParty  string   `json:"requested_party"`
	CalledParty     string   `json:"called_party,omitempty"`
	CalleeIMPI      string   `json:"callee_impi,omitempty"`
	RequestedAt     string   `json:"requested_at"`
	DeliveryStartAt string   `json:"delivery_start_at,omitempty"`
	DeliveryEndAt   string   `json:"delivery_end_at,omitempty"`
	SIPStatus       int      `json:"sip_status,omitempty"`
	Outcome         string   `json:"outcome,omitempty"`
	EndedBy         string   `json:"ended_by,omitempty"`
	Alerted         bool     `json:"alerted"`
	Media           []string `json:"media"`
	// InProgress reports a call that has not ended.
	InProgress bool `json:"in_progress"`
	Incomplete bool `json:"incomplete"`
	// DurationMS is how long an answered call that ended lasted, in milliseconds.
	DurationMS *int64 `json:"duration_ms,omitempty"`
}

type ListCallRecordsResponse struct {
	Items      []CallRecordResponse `json:"items"`
	Page       int                  `json:"page"`
	PerPage    int                  `json:"per_page"`
	TotalCount int                  `json:"total_count"`
}

type CallRecordRetention struct {
	Days int `json:"days"`
}

var outcomes = map[string]db.CallOutcome{
	"answered": db.OutcomeAnswered, "cancelled": db.OutcomeCancelled, "busy": db.OutcomeBusy,
	"rejected": db.OutcomeRejected, "no_answer": db.OutcomeNoAnswer, "unavailable": db.OutcomeUnavailable,
	"failed": db.OutcomeFailed,
}

func ListCallRecords(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		f, ok := callRecordFilter(w, r, cfg)
		if !ok {
			return
		}

		records, total, err := cfg.CallRecords.ListCallRecords(r.Context(), f, page, perPage)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list call records", err, cfg.Logger)
			return
		}

		resp := ListCallRecordsResponse{
			Items: make([]CallRecordResponse, 0, len(records)), Page: page, PerPage: perPage, TotalCount: total,
		}

		for _, rec := range records {
			resp.Items = append(resp.Items, callRecordResponse(rec))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func GetCallRecord(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusNotFound, "call record not found", nil, cfg.Logger)
			return
		}

		rec, err := cfg.CallRecords.GetCallRecord(r.Context(), id)

		switch {
		case errors.Is(err, db.ErrNotFound):
			writeError(w, http.StatusNotFound, "call record not found", nil, cfg.Logger)
		case err != nil:
			writeError(w, http.StatusInternalServerError, "failed to get the call record", err, cfg.Logger)
		default:
			writeResponse(w, callRecordResponse(rec), http.StatusOK, cfg.Logger)
		}
	})
}

func GetCallRecordRetention(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponse(w, CallRecordRetention{Days: cfg.Settings.Get().CallRecords.RetentionDays}, http.StatusOK, cfg.Logger)
	})
}

// UpdateCallRecordRetention changes how long call records are kept, from their next pruning.
func UpdateCallRecordRetention(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params CallRecordRetention
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		if err := cfg.Settings.UpdateCallRecords(r.Context(), settings.CallRecords{RetentionDays: params.Days}); err != nil {
			writeSettingsError(w, err, "Failed to update the call record retention", cfg.Logger)
			return
		}

		writeResponse(w, CallRecordRetention{Days: cfg.Settings.Get().CallRecords.RetentionDays}, http.StatusOK, cfg.Logger)
	})
}

func callRecordFilter(w http.ResponseWriter, r *http.Request, cfg Config) (db.CallRecordFilter, bool) {
	q := r.URL.Query()
	f := db.CallRecordFilter{Search: q.Get("search")}

	for _, b := range []struct {
		name string
		t    *time.Time
	}{{"start", &f.Start}, {"end", &f.End}} {
		v := q.Get(b.name)
		if v == "" {
			continue
		}

		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, b.name+" must be an RFC 3339 time", nil, cfg.Logger)
			return db.CallRecordFilter{}, false
		}

		// Times are stored in nanoseconds since the epoch.
		if !time.Unix(0, t.UnixNano()).Equal(t) {
			writeError(w, http.StatusBadRequest, b.name+" must be from 1678 to 2262", nil, cfg.Logger)
			return db.CallRecordFilter{}, false
		}

		*b.t = t
	}

	if !f.Start.IsZero() && !f.End.IsZero() && f.End.Before(f.Start) {
		writeError(w, http.StatusBadRequest, "end must not be before start", nil, cfg.Logger)
		return db.CallRecordFilter{}, false
	}

	for _, v := range q["outcome"] {
		o, ok := outcomes[v]
		if !ok {
			writeError(w, http.StatusBadRequest,
				"outcome must be answered, cancelled, busy, rejected, no_answer, unavailable or failed", nil, cfg.Logger)

			return db.CallRecordFilter{}, false
		}

		f.Outcomes = append(f.Outcomes, o)
	}

	return f, true
}

func callRecordResponse(r db.CallRecord) CallRecordResponse {
	resp := CallRecordResponse{
		ID: r.ID, ICID: r.ICID, SessionID: r.SessionID, CallingParty: nonNil(r.CallingParty), CallerIMPI: r.CallerIMPI,
		RequestedParty: r.RequestedParty, CalledParty: r.CalledParty, CalleeIMPI: r.CalleeIMPI,
		RequestedAt: formatTime(r.RequestedAt), SIPStatus: r.SIPStatus, Outcome: string(r.Outcome),
		EndedBy: string(r.EndedBy), Alerted: r.Alerted, Media: nonNil(r.Media),
		InProgress: r.EndedBy == "" && !r.Incomplete, Incomplete: r.Incomplete,
	}

	if !r.DeliveryStartAt.IsZero() {
		resp.DeliveryStartAt = formatTime(r.DeliveryStartAt)
	}

	if !r.DeliveryEndAt.IsZero() {
		resp.DeliveryEndAt = formatTime(r.DeliveryEndAt)
	}

	if d, ok := callrecords.Duration(r); ok {
		resp.DurationMS = new(d.Milliseconds())
	}

	return resp
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}

	return l
}
