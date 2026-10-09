package server

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/core/diameter/rx"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regmetrics"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
)

// registeredTimeout bounds counting the registered subscribers for a scrape.
const registeredTimeout = 2 * time.Second

// The interfaces to the HSS and the policy function, which label the requests to peers.
const (
	interfaceCx = "cx"
	interfaceRx = "rx"
	interfaceN5 = "n5"
)

// The results of a request to a peer: answered with success, answered with a failure, not answered, or not
// answered in time.
const (
	peerSuccess = "success"
	peerFailure = "failure"
	peerError   = "error"
	peerTimeout = "timeout"
)

// sipMethods are the methods that label the SIP responses. The others are "other", so that a UE cannot add
// series by sending requests of new methods. ACK has no response.
var sipMethods = []string{
	"REGISTER", "INVITE", "BYE", "CANCEL", "UPDATE", "PRACK", "MESSAGE", "SUBSCRIBE", "NOTIFY", "OPTIONS",
}

const sipOtherMethod = "other"

// metrics are those the API serves. They outlive the cores, so that a restart keeps the counts.
type metrics struct {
	registry      *prometheus.Registry
	registrations *regmetrics.Registrations

	sipResponses *prometheus.CounterVec
	peerRequests *prometheus.CounterVec
	peerDuration *prometheus.HistogramVec
}

// newMetrics registers the metrics of the Go runtime, the process, the build, the database, the registrations, the
// calls, SIP and the peers. The Diameter peers are those of the settings, in the state the core has them in.
func newMetrics(database *db.DB, records *callrecords.Recorder, peers func() ([]settings.Peer, []diameter.PeerStatus),
) *metrics {
	m := &metrics{
		registry:      prometheus.NewRegistry(),
		registrations: regmetrics.New(),
		sipResponses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_sip_responses_total",
			Help: "Final responses the P-CSCF sent to UEs, by method and status class.",
		}, []string{"method", "class"}),
		peerRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_peer_requests_total",
			Help: "Requests to the HSS (cx) and to the policy function (rx, n5), by interface and result: success, " +
				"failure (answered with an error), error (not answered) and timeout.",
		}, []string{"interface", "result"}),
		peerDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "ellaims_peer_request_duration_seconds",
			Help: "Duration of requests to the HSS and to the policy function, by interface.",
			// Up past the 3 s policy timeout, so that the requests that time out are in a bucket.
			Buckets:                         []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
			NativeHistogramBucketFactor:     1.1,
			NativeHistogramMaxBucketNumber:  100,
			NativeHistogramMinResetDuration: time.Hour,
		}, []string{"interface"}),
	}

	for _, method := range append(sipMethods, sipOtherMethod) {
		for class := 2; class <= 6; class++ {
			m.sipResponses.WithLabelValues(method, strconv.Itoa(class)+"xx")
		}
	}

	for _, iface := range []string{interfaceCx, interfaceRx, interfaceN5} {
		m.peerDuration.WithLabelValues(iface)

		for _, result := range []string{peerSuccess, peerFailure, peerError, peerTimeout} {
			m.peerRequests.WithLabelValues(iface, result)
		}
	}

	// The build's labels are those of Prometheus's own build_info, with the IMS's version and, when the build sets it,
	// its revision.
	v := version.Get()

	build := prometheus.Labels{"version": v.Version}
	if v.Revision != "" {
		build["revision"] = v.Revision
	}

	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		versioncollector.NewCollector("ellaims", versioncollector.WithExtraConstLabels(build)),
		registeredCollector{database},
		diameterPeersCollector{peers},
		m.sipResponses,
		m.peerRequests,
		m.peerDuration,
	)
	m.registry.MustRegister(database.Collectors()...)
	m.registry.MustRegister(records.Collectors()...)
	m.registry.MustRegister(m.registrations.Collectors()...)

	return m
}

var registeredDesc = prometheus.NewDesc(
	"ellaims_registered_subscribers",
	"Subscribers registered now: the private identities with a binding that has not expired.",
	nil, nil,
)

// registeredCollector counts the registered subscribers in the database when scraped, so that the count is that
// of the registrations the API lists.
type registeredCollector struct {
	database *db.DB
}

func (c registeredCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- registeredDesc
}

func (c registeredCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), registeredTimeout)
	defer cancel()

	n, err := c.database.CountRegisteredIMPIs(ctx, time.Now())
	if err != nil {
		ch <- prometheus.NewInvalidMetric(registeredDesc, err)
		return
	}

	ch <- prometheus.MustNewConstMetric(registeredDesc, prometheus.GaugeValue, float64(n))
}

// sipResponse counts the final response that the P-CSCF sent to a UE.
func (m *metrics) sipResponse(req *sip.Request, res *sip.Response) {
	if res == nil || res.StatusCode < 200 || res.StatusCode > 699 {
		return
	}

	method := sipOtherMethod
	if slices.Contains(sipMethods, req.Method) {
		method = req.Method
	}

	m.sipResponses.WithLabelValues(method, strconv.Itoa(res.StatusCode/100)+"xx").Inc()
}

// peerRequest counts a request to a peer, of a result, and times it.
func (m *metrics) peerRequest(iface, result string, elapsed time.Duration) {
	m.peerRequests.WithLabelValues(iface, result).Inc()
	m.peerDuration.WithLabelValues(iface).Observe(elapsed.Seconds())
}

// observeN5 counts a request to the PCF, answered with a status, or not with an error.
func (m *metrics) observeN5(status int, err error, elapsed time.Duration) {
	result := peerSuccess

	switch {
	case status != 0 && (status < 200 || status >= 300):
		result = peerFailure
	case status == 0 && errors.Is(err, context.DeadlineExceeded):
		result = peerTimeout
	case status == 0:
		result = peerError
	}

	m.peerRequest(interfaceN5, result, elapsed)
}

// observedDiameter is a Diameter node whose requests to the HSS and the PCRF are counted and timed.
type observedDiameter struct {
	*diameter.Node

	m *metrics
}

func (d observedDiameter) Send(ctx context.Context, req *diameter.Message, opts ...diameter.RequestOption,
) (*diameter.Message, error) {
	var iface string

	switch req.ApplicationID {
	case cx.ApplicationID:
		iface = interfaceCx
	case rx.ApplicationID:
		iface = interfaceRx
	default:
		return d.Node.Send(ctx, req, opts...)
	}

	start := time.Now()
	ans, err := d.Node.Send(ctx, req, opts...)
	d.m.peerRequest(iface, diameterResult(ans, err), time.Since(start))

	return ans, err
}

func diameterResult(ans *diameter.Message, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return peerTimeout
	case err != nil:
		return peerError
	}

	r, err := tgpp.ParseResult(ans)

	switch {
	case err != nil:
		return peerError
	case r.Success():
		return peerSuccess
	default:
		return peerFailure
	}
}

var diameterPeerUpDesc = prometheus.NewDesc(
	"ellaims_diameter_peer_up",
	"Whether the connection to a Diameter peer is open (1) or not (0), by peer and application.",
	[]string{"peer", "application"}, nil,
)

// diameterPeersCollector reports the state of the Diameter peers of the settings when scraped.
type diameterPeersCollector struct {
	peers func() ([]settings.Peer, []diameter.PeerStatus)
}

func (c diameterPeersCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- diameterPeerUpDesc
}

func (c diameterPeersCollector) Collect(ch chan<- prometheus.Metric) {
	peers, statuses := c.peers()

	open := map[string]bool{}
	for _, st := range statuses {
		open[st.ID] = st.State == diameter.PeerOpen
	}

	for _, p := range peers {
		up := 0.0
		if open[p.ID] {
			up = 1
		}

		for _, app := range p.Applications {
			ch <- prometheus.MustNewConstMetric(diameterPeerUpDesc, prometheus.GaugeValue, up, p.ID, string(app))
		}
	}
}
