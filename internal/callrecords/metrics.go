package callrecords

import (
	"github.com/ellanetworks/ims/internal/db"
	"github.com/prometheus/client_golang/prometheus"
)

// outcomes are how a call ends, which label the calls.
var outcomes = []db.CallOutcome{
	db.OutcomeAnswered, db.OutcomeCancelled, db.OutcomeBusy, db.OutcomeRejected, db.OutcomeNoAnswer,
	db.OutcomeUnavailable, db.OutcomeFailed,
}

type metrics struct {
	active prometheus.Gauge
	calls  *prometheus.CounterVec
}

func newMetrics() metrics {
	m := metrics{
		active: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ellaims_active_calls",
			Help: "Calls in progress: attempted and not yet ended.",
		}),
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_calls_total",
			Help: "Calls that ended, by outcome, as their call records show it.",
		}, []string{"outcome"}),
	}

	for _, o := range outcomes {
		m.calls.WithLabelValues(string(o))
	}

	return m
}

// Collectors are the metrics of the calls.
func (r *Recorder) Collectors() []prometheus.Collector {
	return []prometheus.Collector{r.metrics.active, r.metrics.calls}
}
