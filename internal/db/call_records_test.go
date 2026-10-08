package db

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

var callT0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func attempt(icid string, at time.Time) *CallRecord {
	return &CallRecord{
		ICID: icid, SessionID: "call-" + icid, FromAddress: "<sip:alice@example.org>;tag=1",
		CallingParty: []string{"sip:alice@example.org", "tel:+15551230001"}, CallerIMPI: "alice@example.org",
		RequestedParty: "tel:5551230002", RequestedAt: at,
	}
}

// ended makes r a call that ended with status, by a party, a second after it was requested; and that was
// answered, then lasted a minute, if status is 2xx.
func ended(r *CallRecord, status int, outcome CallOutcome, by CallParty) *CallRecord {
	r.SIPStatus, r.Outcome, r.EndedBy = status, outcome, by
	r.DeliveryStartAt = r.RequestedAt.Add(time.Second)

	if outcome == OutcomeAnswered {
		r.DeliveryEndAt = r.DeliveryStartAt.Add(time.Minute)
	}

	return r
}

func saveCalls(t *testing.T, d *DB, records ...*CallRecord) {
	t.Helper()

	if err := d.SaveCallRecords(t.Context(), records...); err != nil {
		t.Fatalf("SaveCallRecords: %v", err)
	}
}

func TestCallRecordLifecycle(t *testing.T) {
	d := openTestDB(t)

	r := attempt("ICID1", callT0)
	saveCalls(t, d, r)

	if r.ID == 0 {
		t.Fatal("an inserted record has no ID")
	}

	r.CallingParty = []string{"sip:+15551230001@example.org;user=phone"}
	r.CalledParty = "tel:+15551230002"
	r.Alerted = true
	saveCalls(t, d, r)

	r.SIPStatus, r.Outcome = 200, OutcomeAnswered
	r.DeliveryStartAt = callT0.Add(3 * time.Second)
	r.CalledAsserted = []string{"unknown"}
	r.CalleeIMPI = "bob@example.org"
	r.Media = []string{"audio", "video"}
	saveCalls(t, d, r)

	r.DeliveryEndAt = callT0.Add(time.Minute)
	r.EndedBy = PartyCallee
	r.ReasonHeaders = []string{`SIP;cause=200;text="Call completed elsewhere"`}

	saved := *r
	saved.SessionID, saved.RequestedParty = "changed", "changed" // never updated

	saveCalls(t, d, &saved)

	got, err := d.GetCallRecord(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("GetCallRecord: %v", err)
	}

	if !reflect.DeepEqual(got, *r) {
		t.Fatalf("record = %+v\nwant %+v", got, *r)
	}

	if _, err := d.GetCallRecord(t.Context(), r.ID+1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCallRecord of a missing record = %v, want %v", err, ErrNotFound)
	}
}

func TestSaveCallRecordsKeepsTheOthers(t *testing.T) {
	d := openTestDB(t)

	saveCalls(t, d, attempt("ICID1", callT0))

	dup, ok := attempt("ICID1", callT0), attempt("ICID2", callT0)
	pruned := attempt("ICID3", callT0)
	pruned.ID = 1000

	err := d.SaveCallRecords(t.Context(), dup, ok, pruned)
	if !errors.Is(err, ErrDuplicateICID) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("SaveCallRecords = %v, want %v and %v", err, ErrDuplicateICID, ErrNotFound)
	}

	if dup.ID != 0 || ok.ID == 0 {
		t.Fatalf("IDs = %d, %d; want only the second saved", dup.ID, ok.ID)
	}

	if _, err := d.GetCallRecord(t.Context(), ok.ID); err != nil {
		t.Fatalf("the record saved with failing ones was lost: %v", err)
	}
}

// TestCallRecordConstraints checks that the database refuses records whose fields contradict each other.
func TestCallRecordConstraints(t *testing.T) {
	cases := map[string]func(r *CallRecord){
		"answered with an error status": func(r *CallRecord) {
			ended(r, 486, OutcomeAnswered, PartyCallee)
		},
		"failed with a 2xx": func(r *CallRecord) {
			ended(r, 200, OutcomeFailed, PartyCallee)
		},
		"status without outcome": func(r *CallRecord) {
			ended(r, 486, "", PartyCallee)
		},
		"status out of range": func(r *CallRecord) {
			ended(r, 100, OutcomeFailed, PartyNetwork)
		},
		"unknown outcome": func(r *CallRecord) {
			ended(r, 486, "engaged", PartyCallee)
		},
		"unknown party": func(r *CallRecord) {
			ended(r, 486, OutcomeBusy, "operator")
		},
		"ended before its final response": func(r *CallRecord) {
			r.EndedBy = PartyCaller
		},
		"final response without its time": func(r *CallRecord) {
			ended(r, 486, OutcomeBusy, PartyCallee).DeliveryStartAt = time.Time{}
		},
		"end time of an unanswered call": func(r *CallRecord) {
			ended(r, 486, OutcomeBusy, PartyCallee).DeliveryEndAt = callT0.Add(time.Minute)
		},
		"answered call ended without its end time": func(r *CallRecord) {
			ended(r, 200, OutcomeAnswered, PartyCaller).DeliveryEndAt = time.Time{}
		},
		"end time of a call in progress": func(r *CallRecord) {
			ended(r, 200, OutcomeAnswered, "")
		},
		"ended and incomplete": func(r *CallRecord) {
			ended(r, 486, OutcomeBusy, PartyCallee).Incomplete = true
		},
	}

	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			d := openTestDB(t)

			r := attempt("ICID1", callT0)
			corrupt(r)

			if err := d.SaveCallRecords(t.Context(), r); !isConstraint(err, sqlite3.ErrConstraintCheck) {
				t.Fatalf("SaveCallRecords = %v, want a CHECK constraint failure", err)
			}
		})
	}
}

func TestListCallRecords(t *testing.T) {
	d := openTestDB(t)

	answered := ended(attempt("AAAA", callT0), 200, OutcomeAnswered, PartyCaller)
	answered.CalledAsserted = []string{"sip:bob@example.org", "tel:+15551230002"}
	answered.CalleeIMPI = "bob@example.org"

	busy := ended(attempt("BBBB", callT0.Add(time.Hour)), 486, OutcomeBusy, PartyCallee)
	busy.CallerIMPI, busy.CallingParty, busy.RequestedParty = "carol@example.org", nil, "sip:dave%40x@example.org"

	unregistered := ended(attempt("CCCC", callT0.Add(2*time.Hour)), 403, OutcomeFailed, PartyNetwork)
	unregistered.CallerIMPI, unregistered.CallingParty = "", nil

	ringing := attempt("DDDD", callT0.Add(3*time.Hour))

	saveCalls(t, d, answered, busy, unregistered, ringing)

	cases := []struct {
		name   string
		filter CallRecordFilter
		want   []*CallRecord
	}{
		{"all, newest first", CallRecordFilter{}, []*CallRecord{ringing, unregistered, busy, answered}},
		{"caller IMPI", CallRecordFilter{Search: "carol"}, []*CallRecord{busy}},
		{"callee IMPI", CallRecordFilter{Search: "bob@"}, []*CallRecord{answered}},
		{"calling party", CallRecordFilter{Search: "+1555123000"}, []*CallRecord{ringing, answered}},
		{"called asserted identity", CallRecordFilter{Search: "tel:+15551230002"}, []*CallRecord{answered}},
		{"requested party", CallRecordFilter{Search: "tel:5551230002"}, []*CallRecord{ringing, unregistered, answered}},
		{"ICID", CallRecordFilter{Search: "cccc"}, []*CallRecord{unregistered}},
		{"literal %", CallRecordFilter{Search: "dave%40"}, []*CallRecord{busy}},
		{"literal _", CallRecordFilter{Search: "alice_"}, nil},
		{"from", CallRecordFilter{From: callT0.Add(time.Hour)}, []*CallRecord{ringing, unregistered, busy}},
		{"to, excluded", CallRecordFilter{To: callT0.Add(time.Hour)}, []*CallRecord{answered}},
		{"outcomes", CallRecordFilter{Outcomes: []CallOutcome{OutcomeBusy, OutcomeFailed}}, []*CallRecord{unregistered, busy}},
		{
			"all filters",
			CallRecordFilter{Search: "alice", From: callT0, To: callT0.Add(3 * time.Hour), Outcomes: []CallOutcome{OutcomeFailed}},
			[]*CallRecord{unregistered},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, total, err := d.ListCallRecords(t.Context(), c.filter, 1, 25)
			if err != nil {
				t.Fatalf("ListCallRecords: %v", err)
			}

			if ids(got) != idsOf(c.want) || total != len(c.want) {
				t.Fatalf("records = %s (%d), want %s", ids(got), total, idsOf(c.want))
			}
		})
	}

	page, total, err := d.ListCallRecords(t.Context(), CallRecordFilter{}, 2, 3)
	if err != nil {
		t.Fatalf("ListCallRecords: %v", err)
	}

	if ids(page) != idsOf([]*CallRecord{answered}) || total != 4 {
		t.Fatalf("page 2 = %s of %d, want %s of 4", ids(page), total, idsOf([]*CallRecord{answered}))
	}
}

func TestStreamCallRecords(t *testing.T) {
	d := openTestDB(t)

	// More than a batch, with ties on the time, which the ID orders.
	n := 2*callRecordBatch + 1

	records := make([]*CallRecord, n)
	for i := range records {
		records[i] = attempt(fmt.Sprintf("ICID%d", i), callT0.Add(time.Duration(i/2)*time.Second))
		if i%3 == 0 {
			ended(records[i], 404, OutcomeFailed, PartyNetwork)
		}
	}

	saveCalls(t, d, records...)

	for _, f := range []CallRecordFilter{{}, {Outcomes: []CallOutcome{OutcomeFailed}}} {
		var want []*CallRecord

		for i := n - 1; i >= 0; i-- {
			if len(f.Outcomes) == 0 || records[i].Outcome == OutcomeFailed {
				want = append(want, records[i])
			}
		}

		var got []CallRecord

		if err := d.StreamCallRecords(t.Context(), f, func(r CallRecord) error {
			got = append(got, r)
			return nil
		}); err != nil {
			t.Fatalf("StreamCallRecords: %v", err)
		}

		if ids(got) != idsOf(want) {
			t.Fatalf("streamed %d records, want %d, newest first", len(got), len(want))
		}
	}

	stop := errors.New("stop")
	calls := 0

	err := d.StreamCallRecords(t.Context(), CallRecordFilter{}, func(CallRecord) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("StreamCallRecords = %v after %d calls, want %v after 1", err, calls, stop)
	}
}

func TestPruneCallRecords(t *testing.T) {
	d := openTestDB(t)

	n := callRecordBatch + 10

	records := make([]*CallRecord, n)
	for i := range records {
		records[i] = attempt(fmt.Sprintf("ICID%d", i), callT0.Add(time.Duration(i)*time.Second))
	}

	saveCalls(t, d, records...)

	// More than a batch is too old.
	before := callT0.Add(time.Duration(callRecordBatch+2) * time.Second)

	deleted, err := d.PruneCallRecords(t.Context(), before, n)
	if err != nil {
		t.Fatalf("PruneCallRecords: %v", err)
	}

	if deleted != int64(callRecordBatch+2) {
		t.Fatalf("deleted %d, want %d", deleted, callRecordBatch+2)
	}

	// Then the oldest beyond the cap.
	if deleted, err = d.PruneCallRecords(t.Context(), before, 3); err != nil || deleted != 5 {
		t.Fatalf("PruneCallRecords = %d, %v; want 5 deleted", deleted, err)
	}

	got, total, err := d.ListCallRecords(t.Context(), CallRecordFilter{}, 1, 25)
	if err != nil {
		t.Fatalf("ListCallRecords: %v", err)
	}

	if want := records[n-3:]; total != 3 || ids(got) != idsOf([]*CallRecord{want[2], want[1], want[0]}) {
		t.Fatalf("left %s, want the 3 newest", ids(got))
	}
}

func TestCloseOpenCallRecords(t *testing.T) {
	d := openTestDB(t)

	ringing := attempt("ICID1", callT0)
	inCall := ended(attempt("ICID2", callT0), 200, OutcomeAnswered, "")
	inCall.DeliveryEndAt = time.Time{}
	done := ended(attempt("ICID3", callT0), 487, OutcomeCancelled, PartyCaller)
	saveCalls(t, d, ringing, inCall, done)

	n, err := d.CloseOpenCallRecords(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("CloseOpenCallRecords = %d, %v; want 2", n, err)
	}

	for _, r := range []*CallRecord{ringing, inCall, done} {
		got, err := d.GetCallRecord(t.Context(), r.ID)
		if err != nil {
			t.Fatalf("GetCallRecord: %v", err)
		}

		if want := r != done; got.Incomplete != want {
			t.Fatalf("%s incomplete = %t, want %t", r.ICID, got.Incomplete, want)
		}
	}

	if n, err := d.CloseOpenCallRecords(t.Context()); err != nil || n != 0 {
		t.Fatalf("second CloseOpenCallRecords = %d, %v; want 0", n, err)
	}
}

func ids(records []CallRecord) string {
	s := make([]int64, len(records))
	for i, r := range records {
		s[i] = r.ID
	}

	return fmt.Sprint(s)
}

func idsOf(records []*CallRecord) string {
	s := make([]int64, len(records))
	for i, r := range records {
		s[i] = r.ID
	}

	return fmt.Sprint(s)
}
