package server

import (
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
)

// newMetrics is the registry of the metrics the API serves: the Go runtime's, the process's, the build's and the
// database's.
func newMetrics(database *db.DB) *prometheus.Registry {
	reg := prometheus.NewRegistry()

	// The build's labels are those of Prometheus's own build_info, with the IMS's version and, when the build sets it,
	// its revision.
	v := version.Get()

	build := prometheus.Labels{"version": v.Version}
	if v.Revision != "" {
		build["revision"] = v.Revision
	}

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		versioncollector.NewCollector("ellaims", versioncollector.WithExtraConstLabels(build)),
	)
	reg.MustRegister(database.Collectors()...)

	return reg
}
