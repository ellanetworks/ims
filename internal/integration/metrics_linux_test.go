package integration

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// peerRequests is the number of requests to the policy function of iface that the IMS counted with a result.
func (s *scene) peerRequests(t *testing.T, iface, result string) float64 {
	t.Helper()

	req, err := http.NewRequestWithContext(s.ctx(), http.MethodGet, "http://"+s.srv.APIAddr().String()+"/api/v1/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	parser := expfmt.NewTextParser(model.UTF8Validation)

	families, err := parser.TextToMetricFamilies(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("parse metrics: %v", err)
	}

	label := map[string]string{policyRx: "rx", policyN5: "n5", policyN5TLS: "n5"}[iface]

	for _, m := range families["ellaims_peer_requests_total"].GetMetric() {
		l := map[string]string{}
		for _, p := range m.GetLabel() {
			l[p.GetName()] = p.GetValue()
		}

		if l["interface"] == label && l["result"] == result {
			return m.GetCounter().GetValue()
		}
	}

	return 0
}
