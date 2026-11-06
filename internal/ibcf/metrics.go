package ibcf

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	failovers *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	return &Metrics{
		failovers: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_trunk_failovers_total",
			Help: "Calls to a SIP trunk retried on its next server or the next trunk of their route after a transport error, " +
				"a timeout or a 503, by the trunk ID that failed.",
		}, []string{"trunk"}),
	}
}

func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.failovers}
}

func (m *Metrics) failover(trunk string) {
	if m == nil {
		return
	}

	m.failovers.WithLabelValues(trunk).Inc()
}
