package scscf

import (
	"fmt"
	"testing"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestRegistrationMetrics(t *testing.T) {
	h := newHarness(t)
	u := h.newUE()

	// The 401 challenge does not count, the REGISTER that answers it does.
	u.register(registerOptions{})
	h.hss.nextSAR(t)
	wantRegistrations(t, h.cfg.Metrics, map[string]float64{"accept": 1, "auth_failure": 0, "reject": 0})

	// A re-registration counts, a de-registration does not.
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES)}), 200)
	wantStatus(t, u.send(registerOptions{auth: u.protected(testNonce(), testVector.XRES), expires: "0"}), 200)
	h.hss.wantSAR(t, cx.AssignmentUserDeregistration)
	wantRegistrations(t, h.cfg.Metrics, map[string]float64{"accept": 2, "auth_failure": 0, "reject": 0})

	nonce := u.challenged(registerOptions{})
	auth := fmt.Sprintf(`Digest username="%s", realm="%s", uri="sip:%s", nonce="%s", response="", `+
		`algorithm=AKAv1-MD5, integrity-protected="no"`, u.impi, homeDomain, homeDomain, nonce)
	wantStatus(t, u.send(registerOptions{auth: auth}), 403)
	h.hss.wantSAR(t, cx.AssignmentAuthenticationFailure)
	wantRegistrations(t, h.cfg.Metrics, map[string]float64{"accept": 2, "auth_failure": 1, "reject": 0})

	wantStatus(t, u.send(registerOptions{expires: "1"}), 423)
	wantRegistrations(t, h.cfg.Metrics, map[string]float64{"accept": 2, "auth_failure": 1, "reject": 1})
}

func wantRegistrations(t *testing.T, m *Metrics, want map[string]float64) {
	t.Helper()

	for result, n := range want {
		var got dto.Metric
		if err := m.registrations.WithLabelValues(result).(prometheus.Metric).Write(&got); err != nil {
			t.Fatal(err)
		}

		if got.GetCounter().GetValue() != n {
			t.Errorf("registration_attempts_total{result=%q} = %v, want %v", result, got.GetCounter().GetValue(), n)
		}
	}
}
