package trunkmon

import (
	"sync/atomic"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/prometheus/client_golang/prometheus"
)

var trunkUpDesc = prometheus.NewDesc(
	"ellaims_trunk_up",
	"Whether a SIP trunk answers OPTIONS keepalives (1) or is down (0), by trunk ID. A trunk without keepalives, "+
		"disabled, or not probed yet, has no series.",
	[]string{"trunk"}, nil,
)

type Metrics struct {
	mon atomic.Pointer[Monitor]
}

func NewMetrics() *Metrics {
	return &Metrics{}
}

func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m}
}

func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- trunkUpDesc
}

func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	mon := m.mon.Load()
	if mon == nil {
		return
	}

	for id, up := range mon.known() {
		v := 0.0
		if up {
			v = 1
		}

		ch <- prometheus.MustNewConstMetric(trunkUpDesc, prometheus.GaugeValue, v, id)
	}
}

func (m *Monitor) known() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string]bool, len(m.states))

	for id, st := range m.states {
		if _, probed := m.probes[id]; probed && (st.State == settings.TrunkUp || st.State == settings.TrunkDown) {
			out[id] = st.State == settings.TrunkUp
		}
	}

	return out
}
