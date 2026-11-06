package trunkmon

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	up *prometheus.GaugeVec
}

func NewMetrics() *Metrics {
	return &Metrics{
		up: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ellaims_trunk_up",
			Help: "Whether a SIP trunk answers OPTIONS keepalives (1) or is down (0), by trunk ID. A trunk without " +
				"keepalives, or not probed yet, has no series.",
		}, []string{"trunk"}),
	}
}

func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.up}
}

func (m *Metrics) set(trunk string, up bool) {
	if m == nil {
		return
	}

	v := 0.0
	if up {
		v = 1
	}

	m.up.WithLabelValues(trunk).Set(v)
}

func (m *Metrics) Forget(trunk string) {
	if m == nil {
		return
	}

	m.up.DeleteLabelValues(trunk)
}
