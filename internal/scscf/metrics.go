package scscf

import (
	"github.com/ellanetworks/ims/sip"
	"github.com/prometheus/client_golang/prometheus"
)

// The results of a REGISTER, which label the registration attempts.
const (
	registrationAccept      = "accept"
	registrationAuthFailure = "auth_failure"
	registrationReject      = "reject"
)

// Metrics are the S-CSCF's, which outlive the registrars that the IMS restarts. A nil Metrics records nothing.
type Metrics struct {
	registrations *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	m := &Metrics{
		registrations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_registration_attempts_total",
			Help: "Final answers of the S-CSCF to REGISTER requests that register or re-register, by result. " +
				"The 401 challenge of IMS-AKA is not one.",
		}, []string{"result"}),
	}

	for _, r := range []string{registrationAccept, registrationAuthFailure, registrationReject} {
		m.registrations.WithLabelValues(r)
	}

	return m
}

func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.registrations}
}

// registration counts the answer to a REGISTER, of a UE that failed IMS-AKA if authFailed. A 401 challenges the
// UE, which registers with its next REGISTER, so it is not counted.
func (m *Metrics) registration(res *sip.Response, authFailed bool) {
	if m == nil || res.StatusCode == 401 {
		return
	}

	result := registrationReject

	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		result = registrationAccept
	case authFailed:
		result = registrationAuthFailure
	}

	m.registrations.WithLabelValues(result).Inc()
}
