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

type CallRecords interface {
	ListCallRecords(ctx context.Context, f db.CallRecordFilter, page, perPage int) ([]db.CallRecord, int, error)
	CountCallRecords(ctx context.Context, f db.CallRecordFilter) (map[db.CallOutcome]int, error)
	GetCallRecord(ctx context.Context, id int64) (db.CallRecord, error)
}

// TS 32.298 clause 5.1.3.1
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
	InProgress      bool     `json:"in_progress"`
	Incomplete      bool     `json:"incomplete"`
	DurationMS      *int64   `json:"duration_ms,omitempty"`
}

type ListCallRecordsResponse struct {
	Items      []CallRecordResponse `json:"items"`
	Page       int                  `json:"page"`
	PerPage    int                  `json:"per_page"`
	TotalCount int                  `json:"total_count"`
}

type CallRecordCounts struct {
	Answered    int `json:"answered"`
	Cancelled   int `json:"cancelled"`
	Busy        int `json:"busy"`
	Rejected    int `json:"rejected"`
	NoAnswer    int `json:"no_answer"`
	Unavailable int `json:"unavailable"`
	Failed      int `json:"failed"`
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
			writeError(r.Context(), w, http.StatusInternalServerError, "failed to list call records", err, cfg.Logger)
			return
		}

		resp := ListCallRecordsResponse{
			Items: make([]CallRecordResponse, 0, len(records)), Page: page, PerPage: perPage, TotalCount: total,
		}

		for _, rec := range records {
			resp.Items = append(resp.Items, callRecordResponse(rec))
		}

		writeResponse(r.Context(), w, resp, http.StatusOK, cfg.Logger)
	})
}

func CountCallRecords(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := callRecordFilter(w, r, cfg)
		if !ok {
			return
		}

		counts, err := cfg.CallRecords.CountCallRecords(r.Context(), f)
		if err != nil {
			writeError(r.Context(), w, http.StatusInternalServerError, "failed to count call records", err, cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, CallRecordCounts{
			Answered: counts[db.OutcomeAnswered], Cancelled: counts[db.OutcomeCancelled], Busy: counts[db.OutcomeBusy],
			Rejected: counts[db.OutcomeRejected], NoAnswer: counts[db.OutcomeNoAnswer],
			Unavailable: counts[db.OutcomeUnavailable], Failed: counts[db.OutcomeFailed],
		}, http.StatusOK, cfg.Logger)
	})
}

func GetCallRecord(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			writeError(r.Context(), w, http.StatusNotFound, "call record not found", nil, cfg.Logger)
			return
		}

		rec, err := cfg.CallRecords.GetCallRecord(r.Context(), id)

		switch {
		case errors.Is(err, db.ErrNotFound):
			writeError(r.Context(), w, http.StatusNotFound, "call record not found", nil, cfg.Logger)
		case err != nil:
			writeError(r.Context(), w, http.StatusInternalServerError, "failed to get the call record", err, cfg.Logger)
		default:
			writeResponse(r.Context(), w, callRecordResponse(rec), http.StatusOK, cfg.Logger)
		}
	})
}

func GetCallRecordRetention(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponse(r.Context(), w, CallRecordRetention{Days: cfg.Settings.Get().CallRecords.RetentionDays}, http.StatusOK, cfg.Logger)
	})
}

func UpdateCallRecordRetention(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params CallRecordRetention
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		if err := cfg.Settings.UpdateCallRecords(r.Context(), settings.CallRecords{RetentionDays: params.Days}); err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to update the call record retention", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, CallRecordRetention{Days: cfg.Settings.Get().CallRecords.RetentionDays}, http.StatusOK, cfg.Logger)
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
			writeError(r.Context(), w, http.StatusBadRequest, b.name+" must be an RFC 3339 time", nil, cfg.Logger)
			return db.CallRecordFilter{}, false
		}

		if !time.Unix(0, t.UnixNano()).Equal(t) {
			writeError(r.Context(), w, http.StatusBadRequest, b.name+" must be from 1678 to 2262", nil, cfg.Logger)
			return db.CallRecordFilter{}, false
		}

		*b.t = t
	}

	if !f.Start.IsZero() && !f.End.IsZero() && !f.End.After(f.Start) {
		writeError(r.Context(), w, http.StatusBadRequest, "end must be after start", nil, cfg.Logger)
		return db.CallRecordFilter{}, false
	}

	for _, v := range q["outcome"] {
		o, ok := outcomes[v]
		if !ok {
			writeError(r.Context(), w, http.StatusBadRequest,
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
