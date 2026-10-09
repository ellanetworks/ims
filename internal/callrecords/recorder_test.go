package callrecords

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
)

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// tickClock is a second later at each reading, so that each event has its own time.
type tickClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *tickClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(time.Second)

	return c.now
}

// at is the time of the nth event.
func at(n int) time.Time {
	return t0.Add(time.Duration(n) * time.Second)
}

func newTestRecorder(t *testing.T, store Store) *Recorder {
	t.Helper()

	r := New(Config{Store: store, Clock: &tickClock{now: t0}})
	t.Cleanup(r.Close)

	return r
}

func openDB(t *testing.T) *db.DB {
	t.Helper()

	d, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() { _ = d.Close() })

	return d
}

func records(t *testing.T, d *db.DB) map[string]db.CallRecord {
	t.Helper()

	l, _, err := d.ListCallRecords(t.Context(), db.CallRecordFilter{}, 1, 10_000)
	if err != nil {
		t.Fatalf("ListCallRecords: %v", err)
	}

	m := map[string]db.CallRecord{}

	for _, r := range l {
		r.ID = 0
		m[r.ICID] = r
	}

	return m
}

func attempt(icid string) Attempt {
	return Attempt{
		ICID: icid, CallID: "call-" + icid,
		RequestURI: "tel:5551230002", IMPI: "alice@example.org", Asserted: []string{"sip:alice@example.org"},
	}
}

// attempted is the record of attempt, opened at the first event.
func attempted(icid string) db.CallRecord {
	return db.CallRecord{
		ICID: icid, SessionID: "call-" + icid,
		CallingParty: []string{"sip:alice@example.org"}, CallerIMPI: "alice@example.org",
		RequestedParty: "tel:5551230002", RequestedAt: at(1),
	}
}

func TestRecorder(t *testing.T) {
	tests := []struct {
		name   string
		report func(r *Recorder, icid string)
		want   func(w *db.CallRecord)
	}{
		{
			name: "answered, ended by the callee",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Routed(icid, Routing{
					Asserted:   []string{"sip:alice@example.org", "tel:+15551230001"},
					RequestURI: "tel:+15551230002",
				})
				r.Alerted(icid)
				r.Media(icid, []string{"audio"})
				r.Reached(icid, "bob@example.org")
				r.Answered(icid, 200)
				r.Media(icid, []string{"audio", "video"})
				r.Ended(icid, End{Code: 200, By: proxy.Callee, Cause: proxy.EndBye})
			},
			want: func(w *db.CallRecord) {
				w.CallingParty = []string{"sip:alice@example.org", "tel:+15551230001"}
				w.CalledParty = "tel:+15551230002"
				w.Alerted = true
				w.CalleeIMPI = "bob@example.org"
				w.SIPStatus, w.Outcome, w.DeliveryStartAt = 200, db.OutcomeAnswered, at(6)
				w.Media = []string{"audio", "video"}
				w.DeliveryEndAt, w.EndedBy = at(8), db.PartyCallee
			},
		},
		{
			name: "answered, in progress",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Answered(icid, 200)
			},
			want: func(w *db.CallRecord) {
				w.SIPStatus, w.Outcome, w.DeliveryStartAt = 200, db.OutcomeAnswered, at(2)
			},
		},
		{
			name: "ringing, saved as it is on close",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Alerted(icid)
			},
			want: func(w *db.CallRecord) {
				w.Alerted = true
			},
		},
		{
			name: "no Service-Route, rejected by the P-CSCF",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Rejecting(icid, 403, "pcscf")
				r.Ended(icid, End{Code: 403, Tag: "pcscf", By: proxy.Caller, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 403, db.OutcomeFailed, db.PartyNetwork, at(3)
			},
		},
		{
			name: "local number rejected by the S-CSCF, then relayed by the P-CSCF",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Rejecting(icid, 404, "scscf")
				r.Ended(icid, End{Code: 404, Tag: "scscf", By: proxy.Callee, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 404, db.OutcomeFailed, db.PartyNetwork, at(3)
			},
		},
		{
			name: "cancelled while the S-CSCF was rejecting it",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Rejecting(icid, 404, "scscf")
				r.Ended(icid, End{Code: 487, Tag: "scscf", By: proxy.Caller, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 487, db.OutcomeCancelled, db.PartyCaller, at(3)
			},
		},
		{
			name: "rejected by the S-CSCF, and by the callee's UE on another branch",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Rejecting(icid, 500, "scscf")
				r.Ended(icid, End{Code: 486, Tag: "ue", By: proxy.Callee, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 486, db.OutcomeBusy, db.PartyCallee, at(3)
			},
		},
		{
			name: "refused by a P-CSCF, and with the same status by the callee's UE on another branch",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Rejecting(icid, 480, "pcscf")
				r.Ended(icid, End{Code: 480, Tag: "ue", By: proxy.Callee, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 480, db.OutcomeUnavailable, db.PartyCallee, at(3)
			},
		},
		{
			name: "a 422 the caller does not retry",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Ended(icid, End{Code: 422, By: proxy.Callee, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 422, db.OutcomeFailed, db.PartyCallee, at(2)
			},
		},
		{
			name: "cancelled",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Alerted(icid)
				r.Ended(icid, End{Code: 487, By: proxy.Caller, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.Alerted = true
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 487, db.OutcomeCancelled, db.PartyCaller, at(3)
			},
		},
		{
			name: "busy",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Ended(icid, End{Code: 486, By: proxy.Callee, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 486, db.OutcomeBusy, db.PartyCallee, at(2)
			},
		},
		{
			name: "rang out",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Alerted(icid)
				r.Ended(icid, End{Code: 408, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.Alerted = true
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 408, db.OutcomeNoAnswer, db.PartyNetwork, at(3)
			},
		},
		{
			name: "408 without ringing",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Ended(icid, End{Code: 408, By: proxy.Callee, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 408, db.OutcomeFailed, db.PartyCallee, at(2)
			},
		},
		{
			name: "480 after a device rang",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Alerted(icid)
				r.Ended(icid, End{Code: 480, By: proxy.Callee, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.Alerted = true
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 480, db.OutcomeUnavailable, db.PartyCallee, at(3)
			},
		},
		{
			name: "released by the S-CSCF, a BYE from the callee's side",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Answered(icid, 200)
				r.Released(icid)
				r.Ended(icid, End{Code: 200, By: proxy.Callee, Cause: proxy.EndBye})
			},
			want: func(w *db.CallRecord) {
				w.SIPStatus, w.Outcome, w.DeliveryStartAt = 200, db.OutcomeAnswered, at(2)
				w.DeliveryEndAt, w.EndedBy = at(4), db.PartyNetwork
			},
		},
		{
			name: "released while early, a final response from the callee's side",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Alerted(icid)
				r.Released(icid)
				r.Ended(icid, End{Code: 480, By: proxy.Callee, Cause: proxy.EndFailed})
			},
			want: func(w *db.CallRecord) {
				w.Alerted = true
				w.SIPStatus, w.Outcome, w.EndedBy, w.DeliveryStartAt = 480, db.OutcomeUnavailable, db.PartyNetwork, at(4)
			},
		},
		{
			name: "no ACK",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Answered(icid, 200)
				r.Ended(icid, End{Code: 200, Cause: proxy.EndNoAck})
			},
			want: func(w *db.CallRecord) {
				w.SIPStatus, w.Outcome, w.DeliveryStartAt = 200, db.OutcomeAnswered, at(2)
				w.DeliveryEndAt, w.EndedBy = at(3), db.PartyNetwork
			},
		},
		{
			name: "ended before its final status was known",
			report: func(r *Recorder, icid string) {
				r.Attempt(attempt(icid))
				r.Ended(icid, End{Cause: proxy.EndReleased})
			},
			want: func(w *db.CallRecord) {
				w.Incomplete = true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := openDB(t)
			r := newTestRecorder(t, d)

			tt.report(r, "ICID1")
			r.Close()

			got := records(t, d)

			if tt.want == nil {
				if len(got) != 0 {
					t.Fatalf("records = %+v, want none", got)
				}

				return
			}

			want := attempted("ICID1")
			tt.want(&want)

			if len(got) != 1 || !reflect.DeepEqual(got["ICID1"], want) {
				t.Fatalf("records = %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestRecorderIgnoresWhatItHasNoOpenRecordFor(t *testing.T) {
	d := openDB(t)
	r := newTestRecorder(t, d)

	r.Attempt(attempt("ICID1"))
	r.Ended("ICID1", End{Code: 486, By: proxy.Callee, Cause: proxy.EndFailed})
	r.Ended("ICID1", End{Code: 200, By: proxy.Caller, Cause: proxy.EndBye})
	r.Alerted("ICID1")
	r.Answered("ICID2", 200)

	dup := attempt("ICID1")
	dup.CallID = "other"

	r.Attempt(attempt("ICID3"))
	r.Attempt(dup)
	r.Close()

	got := records(t, d)

	want := attempted("ICID1")
	want.SIPStatus, want.Outcome, want.EndedBy, want.DeliveryStartAt = 486, db.OutcomeBusy, db.PartyCallee, at(2)

	if len(got) != 2 || !reflect.DeepEqual(got["ICID1"], want) {
		t.Fatalf("records = %+v\nwant ICID1 %+v and ICID3", got, want)
	}
}

func TestRecorderDuplicateAttemptOfAnOpenCall(t *testing.T) {
	d := openDB(t)
	r := newTestRecorder(t, d)

	r.Attempt(attempt("ICID1"))

	dup := attempt("ICID1")
	dup.CallID = "other"
	r.Attempt(dup)
	r.Close()

	if got := records(t, d); got["ICID1"].SessionID != "call-ICID1" {
		t.Fatalf("records = %+v, want the first attempt kept", got)
	}
}

func TestRecorderCloseOpen(t *testing.T) {
	d := openDB(t)
	r := newTestRecorder(t, d)

	r.Attempt(attempt("RINGING"))
	r.Attempt(attempt("INCALL"))
	r.Answered("INCALL", 200)
	r.Attempt(attempt("BUSY"))
	r.Ended("BUSY", End{Code: 486, By: proxy.Callee, Cause: proxy.EndFailed})
	r.CloseOpen()
	r.Ended("INCALL", End{Code: 200, By: proxy.Caller, Cause: proxy.EndBye})
	r.Close()

	got := records(t, d)

	for icid, want := range map[string]bool{"RINGING": true, "INCALL": true, "BUSY": false} {
		if got[icid].Incomplete != want {
			t.Fatalf("%s incomplete = %t, want %t", icid, got[icid].Incomplete, want)
		}
	}

	if r := got["INCALL"]; r.EndedBy != "" || !r.DeliveryEndAt.IsZero() || r.Outcome != db.OutcomeAnswered {
		t.Fatalf("an incomplete call in progress = %+v, want it answered and not ended", r)
	}
}

// TestRecorderSavesOpenCalls checks that a call is saved before it ends, so that a crash leaves a record of it.
func TestRecorderSavesOpenCalls(t *testing.T) {
	store := &testStore{saved: make(chan []db.CallRecord, 10)}
	r := newTestRecorder(t, store)

	r.Attempt(attempt("ICID1"))

	if s := <-store.saved; len(s) != 1 || s[0].SIPStatus != 0 {
		t.Fatalf("saved %+v, want the attempt", s)
	}

	r.Alerted("ICID1")
	r.Answered("ICID1", 200)

	for s := range store.saved {
		if len(s) != 1 || !s[0].Alerted {
			t.Fatalf("saved %+v, want the call alerted", s)
		}

		if s[0].Outcome == db.OutcomeAnswered {
			break
		}
	}
}

// TestRecorderNeverWaitsOnTheStore checks that reporting does not wait for the records to be saved, and that
// the records changed meanwhile are saved as they last are.
func TestRecorderNeverWaitsOnTheStore(t *testing.T) {
	d := openDB(t)
	store := &testStore{next: d, gate: make(chan struct{})}
	r := newTestRecorder(t, store)

	const calls = 2000

	reported := make(chan struct{})

	go func() {
		defer close(reported)

		for i := range calls {
			icid := fmt.Sprintf("ICID%d", i)
			r.Attempt(attempt(icid))
			r.Alerted(icid)
			r.Ended(icid, End{Code: 486, By: proxy.Callee, Cause: proxy.EndFailed})
		}
	}()

	select {
	case <-reported:
	case <-time.After(10 * time.Second):
		t.Fatal("reporting waited on the store")
	}

	close(store.gate)
	r.Close()

	got := records(t, d)
	if len(got) != calls {
		t.Fatalf("saved %d records, want %d", len(got), calls)
	}

	for icid, rec := range got {
		if rec.Outcome != db.OutcomeBusy || !rec.Alerted {
			t.Fatalf("%s = %+v, want it busy after ringing", icid, rec)
		}
	}
}

// TestRecorderDropsWhatItCannotKeep checks that the records of ended calls the store has not saved are bounded:
// past the bound, the records of the calls that end are dropped, and those of calls in progress are kept. Once
// the store catches up, the records the dropped calls left open are closed as incomplete.
func TestRecorderDropsWhatItCannotKeep(t *testing.T) {
	d := openDB(t)
	store := &testStore{next: d}
	r := New(Config{Store: store, MaxUnsaved: 2, Clock: &tickClock{now: t0}})
	t.Cleanup(r.Close)

	icids := []string{"ENDED1", "ENDED2", "DROPPED", "OPEN"}

	for _, icid := range icids {
		r.Attempt(attempt(icid))
	}

	eventually(t, func() bool { return len(records(t, d)) == len(icids) })
	store.pause()

	for _, icid := range []string{"ENDED1", "ENDED2", "DROPPED"} {
		r.Ended(icid, End{Code: 486, By: proxy.Callee, Cause: proxy.EndFailed})
	}

	r.Alerted("DROPPED")
	r.Alerted("OPEN")
	store.resume()

	eventually(t, func() bool { return records(t, d)["DROPPED"].Incomplete })

	got := records(t, d)

	for _, icid := range []string{"ENDED1", "ENDED2"} {
		if got[icid].Outcome != db.OutcomeBusy {
			t.Fatalf("%s = %+v, want it busy", icid, got[icid])
		}
	}

	if rec := got["DROPPED"]; rec.EndedBy != "" || rec.Alerted {
		t.Fatalf("DROPPED = %+v, want it as first saved, closed as incomplete", rec)
	}

	if rec := got["OPEN"]; !rec.Alerted || rec.Incomplete {
		t.Fatalf("OPEN = %+v, want the call in progress kept, open", rec)
	}
}

// TestRecorderSupersedesARetriedAttempt checks that the record of a failed attempt is deleted when the caller
// tries it again with its Call-ID, whether the recorder still keeps the record or only the database has it.
func TestRecorderSupersedesARetriedAttempt(t *testing.T) {
	for _, kept := range []bool{true, false} {
		t.Run(fmt.Sprintf("kept %t", kept), func(t *testing.T) {
			d := openDB(t)
			r := newTestRecorder(t, d)

			retry := func(icid string) Attempt {
				a := attempt(icid)
				a.CallID = "call-1"

				return a
			}

			r.Attempt(retry("ICID1"))
			r.Ended("ICID1", End{Code: 422, By: proxy.Callee, Cause: proxy.EndFailed})

			if !kept {
				eventually(t, func() bool { return records(t, d)["ICID1"].SIPStatus == 422 })
			}

			r.Attempt(retry("ICID2"))
			r.Answered("ICID2", 200)

			// Another caller's Call-ID, and an answered call of the session, are left as they are.
			other := retry("ICID3")
			other.IMPI = "bob@example.org"
			r.Attempt(other)
			r.Ended("ICID3", End{Code: 486, By: proxy.Callee, Cause: proxy.EndFailed})
			r.Attempt(retry("ICID4"))
			r.Close()

			got := records(t, d)
			if len(got) != 3 || got["ICID2"].Outcome != db.OutcomeAnswered || got["ICID3"].Outcome != db.OutcomeBusy {
				t.Fatalf("records = %+v, want the retry answered, the other caller's busy and the last attempt", got)
			}
		})
	}
}

// TestRecorderSupersedesAnAttemptInProgress checks that an attempt the caller tries again before its end is
// discarded once it fails.
func TestRecorderSupersedesAnAttemptInProgress(t *testing.T) {
	d := openDB(t)
	r := newTestRecorder(t, d)

	for _, icid := range []string{"ICID1", "ICID2"} {
		a := attempt(icid)
		a.CallID = "call-1"
		r.Attempt(a)
	}

	r.Ended("ICID1", End{Code: 422, By: proxy.Callee, Cause: proxy.EndFailed})
	r.Close()

	if got := records(t, d); len(got) != 1 || got["ICID2"].ICID == "" {
		t.Fatalf("records = %+v, want the retry alone", got)
	}
}

func TestRecorderRejectingRequest(t *testing.T) {
	d := openDB(t)
	r := newTestRecorder(t, d)

	invite := func(icid, to string) *sip.Request {
		req := sip.NewRequest("INVITE", sip.URI{Scheme: "tel", User: "+15551230002"})
		req.Header.Add("To", to)
		req.Header.Add("P-Charging-Vector", "icid-value="+icid)

		return req
	}

	for _, icid := range []string{"MAPPED", "REINVITE", "OTHER"} {
		r.Attempt(attempt(icid))
	}

	// A 503 reaches the caller as a 500 (RFC 3261 §16.7 step 6).
	// A 503 reaches the caller as a 500 of the proxy's, of another tag (RFC 3261 §16.7 step 6).
	r.RejectingRequest(invite("MAPPED", "<tel:+15551230002>"), 503, "icscf")
	r.RejectingRequest(invite("REINVITE", "<tel:+15551230002>;tag=b"), 500, "scscf")

	other := invite("OTHER", "<tel:+15551230002>")
	other.Method = "MESSAGE"
	r.RejectingRequest(other, 500, "scscf")

	for _, icid := range []string{"MAPPED", "REINVITE", "OTHER"} {
		r.Ended(icid, End{Code: 500, Tag: "scscf", By: proxy.Callee, Cause: proxy.EndFailed})
	}

	r.Close()

	got := records(t, d)

	for icid, want := range map[string]db.CallParty{
		"MAPPED": db.PartyNetwork, "REINVITE": db.PartyCallee, "OTHER": db.PartyCallee,
	} {
		if got[icid].EndedBy != want {
			t.Fatalf("%s ended by %q, want %q", icid, got[icid].EndedBy, want)
		}
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestRecorderRetriesAFailedTransaction(t *testing.T) {
	d := openDB(t)
	store := &testStore{next: d, fail: 2}
	r := newTestRecorder(t, store)

	r.Attempt(attempt("ICID1"))
	r.Answered("ICID1", 200)

	deadline := time.Now().Add(10 * time.Second)

	for records(t, d)["ICID1"].Outcome != db.OutcomeAnswered {
		if time.Now().After(deadline) {
			t.Fatal("the record was not saved again")
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// TestRecorderDropsWhatCannotBeSaved checks that a record the store refuses is not saved again and again, and
// does not keep the others from being saved.
func TestRecorderDropsWhatCannotBeSaved(t *testing.T) {
	d := openDB(t)

	if errs, err := d.SaveCallRecords(t.Context(), []*db.CallRecord{new(attempted("TAKEN"))}, nil); err != nil || errs != nil {
		t.Fatalf("SaveCallRecords = %v, %v", errs, err)
	}

	store := &testStore{next: d, saved: make(chan []db.CallRecord, 10)}
	r := newTestRecorder(t, store)

	r.Attempt(attempt("TAKEN"))
	r.Attempt(attempt("ICID1"))

	// Until both were tried.
	for tried := map[string]bool{}; !tried["TAKEN"] || !tried["ICID1"]; {
		for _, rec := range <-store.saved {
			tried[rec.ICID] = true
		}
	}

	r.Ended("TAKEN", End{Code: 486, By: proxy.Callee, Cause: proxy.EndFailed})
	r.Ended("ICID1", End{Code: 486, By: proxy.Callee, Cause: proxy.EndFailed})
	r.Close()
	close(store.saved)

	for batch := range store.saved {
		for _, rec := range batch {
			if rec.ICID == "TAKEN" {
				t.Fatal("a record the store refused was saved again")
			}
		}
	}

	got := records(t, d)
	if got["TAKEN"].SIPStatus != 0 || got["ICID1"].Outcome != db.OutcomeBusy {
		t.Fatalf("records = %+v, want TAKEN as it was and ICID1 busy", got)
	}
}

func TestRecorderConcurrentReports(t *testing.T) {
	d := openDB(t)
	r := newTestRecorder(t, d)

	var wg sync.WaitGroup

	for i := range 50 {
		wg.Go(func() {
			icid := fmt.Sprintf("ICID%d", i)
			r.Attempt(attempt(icid))
			r.Routed(icid, Routing{Asserted: []string{"tel:+15551230001"}, RequestURI: "tel:+15551230002"})
			r.Media(icid, []string{"audio"})
			r.Answered(icid, 200)
			r.Media(icid, []string{"video"})
			r.Ended(icid, End{Code: 200, By: proxy.Caller, Cause: proxy.EndBye})
		})
	}

	wg.Wait()
	r.Close()

	got := records(t, d)
	if len(got) != 50 {
		t.Fatalf("saved %d records, want 50", len(got))
	}

	for icid, rec := range got {
		if rec.EndedBy != db.PartyCaller || !slices.Equal(rec.Media, []string{"audio", "video"}) {
			t.Fatalf("%s = %+v, want it ended by the caller with audio and video", icid, rec)
		}
	}
}

func TestRecorderAfterClose(t *testing.T) {
	d := openDB(t)
	r := New(Config{Store: d})
	r.Close()

	r.Attempt(attempt("ICID1"))
	r.CloseOpen()
	r.Close()

	if got := records(t, d); len(got) != 0 {
		t.Fatalf("a closed recorder saved %+v", got)
	}
}

func TestNilRecorder(t *testing.T) {
	var r *Recorder

	r.Attempt(attempt("ICID1"))
	r.Rejecting("ICID1", 403, "")
	r.RejectingRequest(nil, 403, "")
	r.Routed("ICID1", Routing{})
	r.Reached("ICID1", "")
	r.Alerted("ICID1")
	r.Answered("ICID1", 200)
	r.Media("ICID1", nil)
	r.Released("ICID1")
	r.Ended("ICID1", End{})
	r.CloseOpen()
	r.Close()
}

// testStore saves to next, if any, once gate, if any, is closed. It fails the first fail transactions, and sends
// a copy of each batch it saves to saved, if any.
type testStore struct {
	next  Store
	gate  chan struct{}
	saved chan []db.CallRecord

	mu   sync.Mutex
	fail int
}

// pause makes the store wait to save until resume.
func (s *testStore) pause() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.gate = make(chan struct{})
}

func (s *testStore) resume() {
	s.mu.Lock()
	defer s.mu.Unlock()

	close(s.gate)
}

func (s *testStore) CloseOpenCallRecords(ctx context.Context, live []string) (int64, error) {
	if s.next == nil {
		return 0, nil
	}

	return s.next.CloseOpenCallRecords(ctx, live)
}

func (s *testStore) SaveCallRecords(ctx context.Context, records []*db.CallRecord, deleted []int64) ([]error, error) {
	s.mu.Lock()
	gate := s.gate
	s.mu.Unlock()

	if gate != nil {
		<-gate
	}

	s.mu.Lock()
	fail := s.fail > 0
	s.fail--
	s.mu.Unlock()

	if fail {
		return nil, errors.New("disk I/O error")
	}

	var (
		errs []error
		err  error
	)

	if s.next != nil {
		errs, err = s.next.SaveCallRecords(ctx, records, deleted)
	}

	if s.saved != nil && err == nil {
		c := make([]db.CallRecord, len(records))
		for i, r := range records {
			c[i] = *r
		}

		s.saved <- c
	}

	return errs, err
}
