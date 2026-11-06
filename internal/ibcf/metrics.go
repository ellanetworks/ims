package ibcf

import (
	"sync"

	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	inbound  = "inbound"
	outbound = "outbound"
)

type Metrics struct {
	failovers *prometheus.CounterVec
	calls     *prometheus.CounterVec
	active    *prometheus.GaugeVec

	mu         sync.Mutex
	configured map[string]bool
	known      map[string]bool
	open       map[string]int
}

func NewMetrics() *Metrics {
	return &Metrics{
		failovers: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_trunk_failovers_total",
			Help: "Calls to a SIP trunk retried on its next server or the next trunk of their route after a transport error, " +
				"a timeout or a 503, by the trunk ID that failed.",
		}, []string{"trunk"}),
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_trunk_calls_total",
			Help: "Calls over a SIP trunk that ended, by the ID of the trunk that gave the final answer, direction (inbound, " +
				"outbound) and outcome.",
		}, []string{"trunk", "direction", "outcome"}),
		active: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ellaims_trunk_active_calls",
			Help: "Calls over a SIP trunk in progress, by trunk ID and direction (inbound, outbound).",
		}, []string{"trunk", "direction"}),
		known: map[string]bool{},
		open:  map[string]int{},
	}
}

func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.failovers, m.calls, m.active}
}

func (m *Metrics) configure(trunks []string) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.configured = make(map[string]bool, len(trunks))

	for _, id := range trunks {
		m.configured[id] = true
		m.trunkLocked(id)
	}

	for id := range m.known {
		if !m.configured[id] && m.open[id] == 0 {
			m.forgetLocked(id)
		}
	}
}

func (m *Metrics) trunkLocked(id string) {
	if m.known[id] {
		return
	}

	m.known[id] = true
	m.failovers.WithLabelValues(id)

	for _, d := range []string{inbound, outbound} {
		m.active.WithLabelValues(id, d)

		for _, o := range callrecords.Outcomes {
			m.calls.WithLabelValues(id, d, string(o))
		}
	}
}

func (m *Metrics) forgetLocked(id string) {
	delete(m.known, id)
	delete(m.open, id)

	labels := prometheus.Labels{"trunk": id}
	m.failovers.DeletePartialMatch(labels)
	m.calls.DeletePartialMatch(labels)
	m.active.DeletePartialMatch(labels)
}

func (m *Metrics) failover(trunk string) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.known[trunk] {
		m.failovers.WithLabelValues(trunk).Inc()
	}
}

func (m *Metrics) enter(trunk, direction string) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.trunkLocked(trunk)
	m.open[trunk]++
	m.active.WithLabelValues(trunk, direction).Inc()
}

func (m *Metrics) leave(trunk, direction string, outcome db.CallOutcome) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.known[trunk] {
		return
	}

	if outcome != "" {
		m.calls.WithLabelValues(trunk, direction, string(outcome)).Inc()
	}

	m.active.WithLabelValues(trunk, direction).Dec()
	m.open[trunk]--

	if m.open[trunk] == 0 && m.configured != nil && !m.configured[trunk] {
		m.forgetLocked(trunk)
	}
}
