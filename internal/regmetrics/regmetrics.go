// Package regmetrics counts the IMS's final answers to the REGISTER requests that register or re-register. Each
// node counts the answers it makes itself: the P-CSCF and the I-CSCF their rejections, and the S-CSCF the rest, so
// that no REGISTER is counted twice.
package regmetrics

import (
	"strconv"

	"github.com/ellanetworks/ims/sip"
	"github.com/prometheus/client_golang/prometheus"
)

// The results of a REGISTER, which label the registration attempts.
const (
	Accept      = "accept"
	AuthFailure = "auth_failure"
	Reject      = "reject"
)

// Registrations counts the registration attempts. A nil Registrations counts nothing.
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

// Answered counts the answer to a request, if it is a REGISTER that registers or re-registers, of a UE that failed
// IMS-AKA if authFailed. A 401 challenges the UE, which registers with its next REGISTER, so it is not counted.
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

// deregistration reports a REGISTER whose contacts all expire now (RFC 3261 §10.2.2). One that cannot be parsed
// is not.
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
