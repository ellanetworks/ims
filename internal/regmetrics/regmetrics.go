package regmetrics

import (
	"strconv"

	"github.com/ellanetworks/ims/sip"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	Accept      = "accept"
	AuthFailure = "auth_failure"
	Reject      = "reject"
)

type Registrations struct {
	attempts *prometheus.CounterVec
}

func New() *Registrations {
	r := &Registrations{
		attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_registration_attempts_total",
			Help: "Final answers of the IMS to REGISTER requests that register or re-register, by result. " +
				"The 401 challenge of IMS-AKA is not one.",
		}, []string{"result"}),
	}

	for _, result := range []string{Accept, AuthFailure, Reject} {
		r.attempts.WithLabelValues(result)
	}

	return r
}

func (r *Registrations) Collectors() []prometheus.Collector {
	return []prometheus.Collector{r.attempts}
}

func (r *Registrations) Answered(req *sip.Request, res *sip.Response, authFailed bool) {
	if r == nil || req.Method != "REGISTER" || res.StatusCode < 200 || res.StatusCode == 401 || deregistration(req) {
		return
	}

	result := Reject

	switch {
	case res.StatusCode < 300:
		result = Accept
	case authFailed:
		result = AuthFailure
	}

	r.attempts.WithLabelValues(result).Inc()
}

// RFC 3261 section 10.2.2
func deregistration(req *sip.Request) bool {
	contacts, err := req.Header.Contacts()
	if err != nil || len(contacts) == 0 {
		return false
	}

	expires := ""

	if req.Header.Has("Expires") {
		n, err := req.Header.Expires()
		if err != nil {
			return false
		}

		expires = strconv.FormatUint(uint64(n), 10)
	}

	for _, c := range contacts {
		v, ok := c.Params.Get("expires")
		if c.Star || !ok {
			v = expires
		}

		if n, err := strconv.ParseUint(v, 10, 32); err != nil || n != 0 {
			return false
		}
	}

	return true
}
