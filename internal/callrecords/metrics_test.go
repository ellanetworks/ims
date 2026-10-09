package callrecords

import (
	"testing"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestRecorderMetrics(t *testing.T) {
	r := newTestRecorder(t, openDB(t))

	for _, icid := range []string{"ANSWERED", "BUSY", "OPEN", "UNKNOWN"} {
		r.Attempt(attempt(icid))
	}

	// An attempt that the caller tries again, which fails, is not a call.
	for _, icid := range []string{"FIRST", "RETRY"} {
		a := attempt(icid)
		a.CallID = "call-retried"
		r.Attempt(a)
	}

	wantGauge(t, r.metrics.active, 6)

	r.Answered("ANSWERED", 200)
	wantGauge(t, r.metrics.active, 6)

	r.Ended("ANSWERED", End{By: proxy.Caller, Cause: proxy.EndBye})
	r.Ended("BUSY", End{Code: 486, By: proxy.Callee, Cause: proxy.EndFailed})
	r.Ended("FIRST", End{Code: 422, By: proxy.Callee, Cause: proxy.EndFailed})
	// Its final response is unknown, so it has no outcome.
	r.Ended("UNKNOWN", End{By: proxy.Callee, Cause: proxy.EndFailed})

	wantGauge(t, r.metrics.active, 2)
	wantCalls(t, r, map[db.CallOutcome]float64{db.OutcomeAnswered: 1, db.OutcomeBusy: 1, db.OutcomeFailed: 0})

	// The calls the IMS loses are no longer in progress.
	r.CloseOpen()
	wantGauge(t, r.metrics.active, 0)
	wantCalls(t, r, map[db.CallOutcome]float64{db.OutcomeAnswered: 1, db.OutcomeBusy: 1, db.OutcomeFailed: 0})
}

func TestRecorderMetricsOfDroppedRecords(t *testing.T) {
	store := &testStore{}
	store.pause()

	r := New(Config{Store: store, MaxUnsaved: 1, Clock: &tickClock{now: t0}})
	t.Cleanup(r.Close)
	t.Cleanup(store.resume)

	r.Attempt(attempt("KEPT"))
	r.Attempt(attempt("DROPPED"))

	r.Ended("KEPT", End{Code: 486, By: proxy.Callee, Cause: proxy.EndFailed})
	r.Ended("DROPPED", End{Code: 486, By: proxy.Callee, Cause: proxy.EndFailed})

	// The dropped call is no longer in progress, and it has no record that shows how it ended.
	wantGauge(t, r.metrics.active, 0)
	wantCalls(t, r, map[db.CallOutcome]float64{db.OutcomeBusy: 1})
}

func wantGauge(t *testing.T, g prometheus.Gauge, want float64) {
	t.Helper()

	if got := value(t, g).GetGauge().GetValue(); got != want {
		t.Fatalf("active calls = %v, want %v", got, want)
	}
}

func wantCalls(t *testing.T, r *Recorder, want map[db.CallOutcome]float64) {
	t.Helper()

	for outcome, n := range want {
		if got := value(t, r.metrics.calls.WithLabelValues(string(outcome))).GetCounter().GetValue(); got != n {
			t.Errorf("calls{outcome=%q} = %v, want %v", outcome, got, n)
		}
	}
}

func value(t *testing.T, m prometheus.Metric) *dto.Metric {
	t.Helper()

	var v dto.Metric
	if err := m.Write(&v); err != nil {
		t.Fatal(err)
	}

	return &v
}
