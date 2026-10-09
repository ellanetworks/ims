package server

import (
	"context"
	"time"

	"github.com/ellanetworks/ims/internal/callrecords"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/scscf"
	"github.com/ellanetworks/ims/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
)

// registeredTimeout bounds counting the registered subscribers for a scrape.
const registeredTimeout = 2 * time.Second

// metrics are those the API serves. They outlive the cores, so that a restart keeps the counts.
type metrics struct {
	registry *prometheus.Registry
	scscf    *scscf.Metrics
}

// newMetrics registers the metrics of the Go runtime, the process, the build, the database, the registrations and
// the calls.
func newMetrics(database *db.DB, records *callrecords.Recorder) *metrics {
	m := &metrics{registry: prometheus.NewRegistry(), scscf: scscf.NewMetrics()}

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
	)
	m.registry.MustRegister(database.Collectors()...)
	m.registry.MustRegister(records.Collectors()...)
	m.registry.MustRegister(m.scscf.Collectors()...)

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
