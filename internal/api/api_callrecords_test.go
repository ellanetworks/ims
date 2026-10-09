package api

import (
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
)

var callT0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// callRecordsDB is a database with an answered call, a busy one and one in progress, an hour apart.
func callRecordsDB(t *testing.T) (*db.DB, []*db.CallRecord) {
	t.Helper()

	d, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	answered := &db.CallRecord{
		ICID: "AAAA", SessionID: "c1", CallingParty: []string{"sip:+15551230001@example.org;user=phone"},
		CallerIMPI: "alice@example.org", RequestedParty: "tel:5551230002", CalledParty: "tel:+15551230002",
		CalleeIMPI: "bob@example.org", RequestedAt: callT0, DeliveryStartAt: callT0.Add(5 * time.Second),
		DeliveryEndAt: callT0.Add(65*time.Second + 250*time.Millisecond), SIPStatus: 200, Outcome: db.OutcomeAnswered,
		EndedBy: db.PartyCaller, Alerted: true, Media: []string{"audio"},
	}
	busy := &db.CallRecord{
		ICID: "BBBB", SessionID: "c2", CallerIMPI: "carol@example.org", RequestedParty: "tel:+15551230003",
		RequestedAt: callT0.Add(time.Hour), DeliveryStartAt: callT0.Add(time.Hour + time.Second), SIPStatus: 486,
		Outcome: db.OutcomeBusy, EndedBy: db.PartyCallee,
	}
	ringing := &db.CallRecord{
		ICID: "CCCC", SessionID: "c3", CallerIMPI: "alice@example.org", RequestedParty: "tel:+15551230003",
		RequestedAt: callT0.Add(2 * time.Hour), Alerted: true,
	}

	records := []*db.CallRecord{answered, busy, ringing}

	if errs, err := d.SaveCallRecords(t.Context(), records, nil); err != nil || errs != nil {
		t.Fatalf("SaveCallRecords = %v, %v", errs, err)
	}

	return d, records
}

func TestListCallRecords(t *testing.T) {
	d, recs := callRecordsDB(t)
	answered, busy, ringing := recs[0].ID, recs[1].ID, recs[2].ID
	cfg := Config{CallRecords: d}

	tests := []struct {
		query string
		want  []int64
		total int
	}{
		{"", []int64{ringing, busy, answered}, 3},
		{"?outcome=busy&outcome=answered", []int64{busy, answered}, 2},
		{"?search=alice", []int64{ringing, answered}, 2},
		{"?search=bbbb", []int64{busy}, 1},
		{"?start=" + callT0.Add(time.Hour).Format(time.RFC3339) + "&end=" + callT0.Add(2*time.Hour).Format(time.RFC3339), []int64{busy}, 1},
		{"?per_page=1&page=2", []int64{busy}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			code, body := serve(t, cfg, http.MethodGet, "/api/v1/call-records"+tt.query, "")
			if code != http.StatusOK {
				t.Fatalf("status %d: %s", code, body)
			}

			resp := decodeResult[ListCallRecordsResponse](t, body)

			var got []int64
			for _, it := range resp.Items {
				got = append(got, it.ID)
			}

			if !reflect.DeepEqual(got, tt.want) || resp.TotalCount != tt.total {
				t.Fatalf("records %v of %d, want %v of %d", got, resp.TotalCount, tt.want, tt.total)
			}
		})
	}
}

func TestListCallRecordsRejected(t *testing.T) {
	d, _ := callRecordsDB(t)

	for query, want := range map[string]string{
		"?start=yesterday":          "start must be an RFC 3339 time",
		"?end=2026-10-08":           "end must be an RFC 3339 time",
		"?end=9999-01-01T00:00:00Z": "end must be from 1678 to 2262",
		"?start=2026-10-08T12:00:00Z&end=2026-10-08T12:00:00Z": "end must be after start",
		"?outcome=engaged": "outcome must be answered, cancelled, busy, rejected, no_answer, unavailable or failed",
		"?per_page=0":      "per_page must be an integer between 1 and 100",
		"?page=0":          "page must be an integer >= 1",
	} {
		code, body := serve(t, Config{CallRecords: d}, http.MethodGet, "/api/v1/call-records"+query, "")
		if code != http.StatusBadRequest || decodeError(t, body) != want {
			t.Errorf("%s = %d %s, want 400 %q", query, code, body, want)
		}
	}
}

func TestGetCallRecord(t *testing.T) {
	d, recs := callRecordsDB(t)
	cfg := Config{CallRecords: d}

	code, body := serve(t, cfg, http.MethodGet, "/api/v1/call-records/"+strconv.FormatInt(recs[0].ID, 10), "")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}

	want := CallRecordResponse{
		ID: recs[0].ID, ICID: "AAAA", SessionID: "c1", CallingParty: []string{"sip:+15551230001@example.org;user=phone"},
		CallerIMPI: "alice@example.org", RequestedParty: "tel:5551230002", CalledParty: "tel:+15551230002",
		CalleeIMPI: "bob@example.org", RequestedAt: "2026-10-08T12:00:00.000Z", DeliveryStartAt: "2026-10-08T12:00:05.000Z",
		DeliveryEndAt: "2026-10-08T12:01:05.250Z", SIPStatus: 200, Outcome: "answered", EndedBy: "caller", Alerted: true,
		Media: []string{"audio"}, DurationMS: new(int64(60250)),
	}

	if got := decodeResult[CallRecordResponse](t, body); !reflect.DeepEqual(got, want) {
		t.Fatalf("record = %+v\nwant %+v", got, want)
	}

	code, body = serve(t, cfg, http.MethodGet, "/api/v1/call-records/"+strconv.FormatInt(recs[2].ID, 10), "")
	if got := decodeResult[CallRecordResponse](t, body); code != http.StatusOK || !got.InProgress || got.Outcome != "" ||
		got.DurationMS != nil || got.CallingParty == nil || got.Media == nil {
		t.Fatalf("call in progress = %d %+v, want it in progress, with empty lists", code, got)
	}

	for _, id := range []string{"999", "0", "x"} {
		if code, body := serve(t, cfg, http.MethodGet, "/api/v1/call-records/"+id, ""); code != http.StatusNotFound {
			t.Errorf("GET %s = %d %s, want 404", id, code, body)
		}
	}
}

func TestCallRecordRetention(t *testing.T) {
	cfg := Config{Settings: newFakeSettings()}

	code, body := serve(t, cfg, http.MethodGet, "/api/v1/call-records/retention", "")
	if got := decodeResult[CallRecordRetention](t, body); code != http.StatusOK || got.Days != 90 {
		t.Fatalf("GET = %d %s, want 90 days", code, body)
	}

	code, body = serve(t, cfg, http.MethodPut, "/api/v1/call-records/retention", `{"days": 30}`)
	if got := decodeResult[CallRecordRetention](t, body); code != http.StatusOK || got.Days != 30 ||
		cfg.Settings.Get().CallRecords.RetentionDays != 30 {
		t.Fatalf("PUT 30 = %d %s, want 30 days", code, body)
	}

	for body, want := range map[string]string{
		`{"days": 0}`:              "call record retention must be 1 to 3650 days",
		`{"days": 3651}`:           "call record retention must be 1 to 3650 days",
		`{}`:                       "call record retention must be 1 to 3650 days",
		`{"days": 30, "weeks": 1}`: "Invalid request data",
	} {
		if code, resp := serve(t, cfg, http.MethodPut, "/api/v1/call-records/retention", body); code != http.StatusBadRequest ||
			decodeError(t, resp) != want {
			t.Errorf("PUT %s = %d %s, want 400 %q", body, code, resp, want)
		}
	}
}
