package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/ipsec/ipsectest"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/siptest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil/promlint"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func TestMetrics(t *testing.T) {
	srv := &Server{Config: testConfig(t).seeded(t), Logger: slog.New(slog.DiscardHandler), IPsec: ipsectest.NewKernel()}

	ctx := context.Background()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(ctx)

	body := scrape(t, "http://"+srv.APIAddr().String()+"/api/v1/metrics")

	problems, err := promlint.New(bytes.NewReader(body)).Lint()
	if err != nil {
		t.Fatalf("lint: %v", err)
	}

	for _, p := range problems {
		t.Errorf("lint: %s: %s", p.Metric, p.Text)
	}

	parser := expfmt.NewTextParser(model.UTF8Validation)

	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, name := range []string{
		"go_goroutines",
		"process_start_time_seconds",
		"ellaims_build_info",
		"ellaims_database_query_duration_seconds",
		"ellaims_database_query_errors_total",
		"ellaims_database_storage_bytes",
		"ellaims_registered_subscribers",
		"ellaims_registration_attempts_total",
		"ellaims_active_calls",
		"ellaims_calls_total",
	} {
		if _, ok := families[name]; !ok {
			t.Errorf("%s is missing", name)
		}
	}

	// The IMS has read its settings at start, so the writer pool has timed a call.
	for _, m := range families["ellaims_database_query_duration_seconds"].GetMetric() {
		if m.GetLabel()[0].GetValue() == "write" && m.GetHistogram().GetSampleCount() == 0 {
			t.Error("no database call on the write pool was timed")
		}
	}

	if n := len(families["ellaims_database_storage_bytes"].GetMetric()); n != 2 {
		t.Errorf("ellaims_database_storage_bytes has %d series, want 2", n)
	}
}

func TestMetricsOfRegistrations(t *testing.T) {
	sc := newCallScene(t, nil)

	families := scrapeFamilies(t, sc.srv)

	if got := families["ellaims_registered_subscribers"].GetMetric()[0].GetGauge().GetValue(); got != 2 {
		t.Errorf("registered subscribers = %v, want 2", got)
	}

	for _, m := range families["ellaims_registration_attempts_total"].GetMetric() {
		want := 0.0
		if m.GetLabel()[0].GetValue() == "accept" {
			want = 2
		}

		if got := m.GetCounter().GetValue(); got != want {
			t.Errorf("registration attempts %s = %v, want %v", m.GetLabel()[0].GetValue(), got, want)
		}
	}
}

// TestMetricsOfRejectedRegistrations checks that the registrations that the P-CSCF and the I-CSCF reject
// themselves, which never reach the S-CSCF, are counted.
func TestMetricsOfRejectedRegistrations(t *testing.T) {
	hss, peer := newHSS(t)

	cfg := testConfig(t)
	cfg.SIP.Addresses = []netip.Addr{loopback}
	cfg.Peers = seedPeers(peer)

	srv := startIMS(t, cfg)
	waitOpen(t, srv, peer.ID)

	pcscf := sipListener(t, srv, rolePCSCF, loopback)
	ue := newPeer(t, srv, netip.MustParseAddrPort("127.0.0.2:6000"))

	// The HSS does not know the subscriber: the I-CSCF answers its UAR with a 403.
	unknown := newRegister(ue.Addr())
	unknown.Header.Set("To", "<sip:unknown@"+imsRealm+">")
	unknown.Header.Set("From", "<sip:unknown@"+imsRealm+">;tag="+sip.NewTag())
	unknown.Header.Set("Authorization", `Digest username="unknown@`+imsRealm+`", realm="`+imsRealm+`", uri="sip:`+
		imsRealm+`", nonce="", response=""`)
	unknown.Header.Add("Expires", "600")
	unknown.Header.Add("Security-Client", "ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=6301;"+
		"port-s=6300;alg=hmac-sha-1-96;ealg=null")
	unknown.Header.Add("Require", "sec-agree")
	unknown.Header.Add("Proxy-Require", "sec-agree")
	ue.Send(sip.UDP, pcscf, unknown)

	hss.Next(t)
	wantResponse(t, ue, 403, "REGISTER")

	// The P-CSCF refuses a Security-Client without an algorithm it supports itself.
	bad := newRegister(ue.Addr())
	bad.Header.Add("Expires", "600")
	bad.Header.Add("Security-Client", "ipsec-3gpp;prot=esp;spi-c=bad")
	bad.Header.Add("Require", "sec-agree")
	bad.Header.Add("Proxy-Require", "sec-agree")
	ue.Send(sip.UDP, pcscf, bad)

	wantResponse(t, ue, 403, "REGISTER")

	got := map[string]float64{}
	for _, m := range scrapeFamilies(t, srv)["ellaims_registration_attempts_total"].GetMetric() {
		got[labels(m)["result"]] = m.GetCounter().GetValue()
	}

	if want := map[string]float64{"accept": 0, "auth_failure": 0, "reject": 2}; !maps.Equal(got, want) {
		t.Errorf("registration attempts = %v, want %v", got, want)
	}
}

func TestMetricsOfSIPAndPeers(t *testing.T) {
	hss, peer := newHSS(t)

	cfg := testConfig(t)
	cfg.SIP.Addresses = []netip.Addr{loopback}
	cfg.Peers = seedPeers(peer)

	srv := startIMS(t, cfg)
	waitOpen(t, srv, peer.ID)

	pcscf := sipListener(t, srv, rolePCSCF, loopback)

	// A UE registers through the P-CSCF, which sends it the S-CSCF's challenge after a UAR and a MAR.
	ue := newPeer(t, srv, netip.MustParseAddrPort("127.0.0.2:6000"))
	register := newRegister(ue.Addr())
	register.Header.Add("Expires", "600")
	register.Header.Add("Security-Client", "ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=6301;"+
		"port-s=6300;alg=hmac-sha-1-96;ealg=null")
	register.Header.Add("Require", "sec-agree")
	register.Header.Add("Proxy-Require", "sec-agree")
	ue.Send(sip.UDP, pcscf, register)

	hss.Next(t)
	hss.Next(t)
	wantResponse(t, ue, 401, "REGISTER")

	// A node of the IMS is not a UE.
	core := newPeer(t, srv, netip.AddrPortFrom(loopback, 6001))
	options := siptest.NewRequest("OPTIONS", "sip:"+imsRealm, sip.UDP, core.Addr())
	options.Header.Set("To", "<sip:"+imsRealm+">")
	options.Header.Set("From", "<sip:"+imsRealm+">;tag="+sip.NewTag())
	core.Send(sip.UDP, pcscf, options)
	core.RecvResponse()

	families := scrapeFamilies(t, srv)

	responses := map[string]float64{}
	for _, m := range families["ellaims_sip_responses_total"].GetMetric() {
		responses[labels(m)["method"]+" "+labels(m)["class"]] += m.GetCounter().GetValue()
	}

	for key, n := range responses {
		if want := map[string]float64{"REGISTER 4xx": 1}[key]; n != want {
			t.Errorf("SIP responses %s = %v, want %v", key, n, want)
		}
	}

	for _, m := range families["ellaims_peer_requests_total"].GetMetric() {
		l := labels(m)

		want := 0.0
		if l["interface"] == "cx" && l["result"] == "success" {
			want = 2
		}

		if got := m.GetCounter().GetValue(); got != want {
			t.Errorf("peer requests %v = %v, want %v", l, got, want)
		}
	}

	up := map[string]float64{}
	for _, m := range families["ellaims_diameter_peer_up"].GetMetric() {
		up[labels(m)["peer"]+" "+labels(m)["application"]] = m.GetGauge().GetValue()
	}

	if want := map[string]float64{peer.ID + " cx": 1}; !maps.Equal(up, want) {
		t.Errorf("Diameter peers up = %v, want %v", up, want)
	}
}

func labels(m *dto.Metric) map[string]string {
	l := map[string]string{}
	for _, p := range m.GetLabel() {
		l[p.GetName()] = p.GetValue()
	}

	return l
}

func scrapeFamilies(t *testing.T, srv *Server) map[string]*dto.MetricFamily {
	t.Helper()

	parser := expfmt.NewTextParser(model.UTF8Validation)

	families, err := parser.TextToMetricFamilies(bytes.NewReader(scrape(t, "http://"+srv.APIAddr().String()+"/api/v1/metrics")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	return families
}

func scrape(t *testing.T, url string) []byte {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func TestDiameterResult(t *testing.T) {
	answer := func(code uint32) *diameter.Message {
		return &diameter.Message{AVPs: []diameter.AVP{diameter.Unsigned32(diameter.AVPResultCode, 0, 0, code)}}
	}

	for _, tt := range []struct {
		name string
		ans  *diameter.Message
		err  error
		want string
	}{
		{"success", answer(diameter.ResultSuccess), nil, peerSuccess},
		{"failure", answer(diameter.ResultUnableToComply), nil, peerFailure},
		{"no result", &diameter.Message{}, nil, peerError},
		{"not connected", nil, diameter.ErrNotConnected, peerError},
		{"timeout", nil, fmt.Errorf("MAR: %w", context.DeadlineExceeded), peerTimeout},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := diameterResult(tt.ans, tt.err); got != tt.want {
				t.Fatalf("diameterResult = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestObserveN5(t *testing.T) {
	m := testMetrics(t)

	m.observeN5(201, nil, time.Millisecond)
	m.observeN5(403, nil, time.Millisecond)
	m.observeN5(0, errors.New("connection refused"), time.Millisecond)
	m.observeN5(0, fmt.Errorf("create: %w", context.DeadlineExceeded), time.Second)

	for _, result := range []string{peerSuccess, peerFailure, peerError, peerTimeout} {
		var v dto.Metric
		if err := m.peerRequests.WithLabelValues(interfaceN5, result).Write(&v); err != nil {
			t.Fatal(err)
		}

		if v.GetCounter().GetValue() != 1 {
			t.Errorf("n5 %s = %v, want 1", result, v.GetCounter().GetValue())
		}
	}
}

func TestSIPResponseOfAnotherMethod(t *testing.T) {
	m := testMetrics(t)

	req := siptest.NewRequest("PUBLISH", "sip:"+imsRealm, sip.UDP, netip.MustParseAddrPort("127.0.0.2:6000"))
	m.sipResponse(req, sip.NewResponse(req, 405, ""))
	m.sipResponse(req, sip.NewResponse(req, 180, ""))

	var v dto.Metric
	if err := m.sipResponses.WithLabelValues(sipOtherMethod, "4xx").Write(&v); err != nil {
		t.Fatal(err)
	}

	if v.GetCounter().GetValue() != 1 {
		t.Fatalf("other 4xx = %v, want 1", v.GetCounter().GetValue())
	}
}

func TestDiameterPeersDown(t *testing.T) {
	peers := []settings.Peer{
		{ID: "hss", Applications: []settings.Application{settings.ApplicationCx}},
		{ID: "pcrf", Applications: []settings.Application{settings.ApplicationRx}},
	}

	c := diameterPeersCollector{func() ([]settings.Peer, []diameter.PeerStatus) {
		return peers, []diameter.PeerStatus{{ID: "hss", State: diameter.PeerOpen}, {ID: "pcrf", State: diameter.PeerSuspect}}
	}}

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	up := map[string]float64{}
	for _, m := range families[0].GetMetric() {
		up[labels(m)["peer"]] = m.GetGauge().GetValue()
	}

	if want := map[string]float64{"hss": 1, "pcrf": 0}; !maps.Equal(up, want) {
		t.Fatalf("up = %v, want %v", up, want)
	}
}

func testMetrics(t *testing.T) *metrics {
	t.Helper()

	d, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "ims.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	records := callrecords.New(callrecords.Config{Store: d})
	t.Cleanup(records.Close)

	return newMetrics(d, records, func() ([]settings.Peer, []diameter.PeerStatus) { return nil, nil })
}
