package ipsmgw

import "github.com/prometheus/client_golang/prometheus"

const (
	directionMO = "mo"
	directionMT = "mt"

	resultSuccess = "success"
	resultFailure = "failure"
)

type Metrics struct {
	shortMessages *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	m := &Metrics{
		shortMessages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_ipsmgw_short_messages_total",
			Help: "Short messages the IP-SM-GW interworked: mobile originated (mo) to an SMSC, and mobile " +
				"terminated (mt) by the domain it last tried (ims, mme, sgsn, smsf_3gpp, smsf_non_3gpp, none), by result.",
		}, []string{"direction", "domain", "result"}),
	}

	for _, result := range []string{resultSuccess, resultFailure} {
		m.shortMessages.WithLabelValues(directionMO, "smsc", result)

		for d := domainIMS; d <= domainNone; d++ {
			m.shortMessages.WithLabelValues(directionMT, d.String(), result)
		}
	}

	return m
}

func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.shortMessages}
}

func (m *Metrics) count(direction, domain string, ok bool) {
	if m == nil {
		return
	}

	result := resultFailure
	if ok {
		result = resultSuccess
	}

	m.shortMessages.WithLabelValues(direction, domain, result).Inc()
}
