package db

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// The pools a method runs its statements on, which label its metrics: conn, the writer that also serves the reads
// on the call path, and read, the readers of the long searches.
const (
	poolWrite = "write"
	poolRead  = "read"
)

type metrics struct {
	duration *prometheus.HistogramVec
	errors   *prometheus.CounterVec
}

func newMetrics() metrics {
	m := metrics{
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "ellaims_database_query_duration_seconds",
			Help: "Duration of database calls, statements, transaction and row scanning included, by connection pool.",
			// Most statements take well under a millisecond, and a statement waits up to the 5 s busy timeout.
			Buckets:                         []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 5},
			NativeHistogramBucketFactor:     1.1,
			NativeHistogramMaxBucketNumber:  100,
			NativeHistogramMinResetDuration: time.Hour,
		}, []string{"pool"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_database_query_errors_total",
			Help: "Database calls that failed, by connection pool. A record not found or in conflict is a result, not a failure.",
		}, []string{"pool"}),
	}

	for _, pool := range []string{poolWrite, poolRead} {
		m.duration.WithLabelValues(pool)
		m.errors.WithLabelValues(pool)
	}

	return m
}

// observe starts timing a call on the pool, and returns the function that ends it, which counts *err if it is a
// failure: defer d.observe(poolWrite, &err)().
func (d *DB) observe(pool string, err *error) func() {
	start := time.Now()

	return func() {
		d.metrics.duration.WithLabelValues(pool).Observe(time.Since(start).Seconds())

		if failed(*err) {
			d.metrics.errors.WithLabelValues(pool).Inc()
		}
	}
}

// failed reports whether err is a failure of the database rather than an answer to the call.
func failed(err error) bool {
	return err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrIdentityConflict) &&
		!errors.Is(err, ErrSubscriptionExists) && !errors.Is(err, ErrDuplicateICID)
}

// Collectors are the metrics of the database: its calls, its connection pools and its size on disk.
func (d *DB) Collectors() []prometheus.Collector {
	return []prometheus.Collector{
		d.metrics.duration,
		d.metrics.errors,
		collectors.NewDBStatsCollector(d.conn, poolWrite),
		collectors.NewDBStatsCollector(d.read, poolRead),
		storageCollector{path: d.path},
	}
}

var storageDesc = prometheus.NewDesc(
	"ellaims_database_storage_bytes",
	"Size of the database on disk, by file: the main file and its write-ahead log.",
	[]string{"file"}, nil,
)

type storageCollector struct {
	path string
}

func (c storageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- storageDesc
}

func (c storageCollector) Collect(ch chan<- prometheus.Metric) {
	for _, f := range []struct{ label, path string }{{"main", c.path}, {"wal", c.path + "-wal"}} {
		size, err := fileSize(f.path)
		if err != nil {
			ch <- prometheus.NewInvalidMetric(storageDesc, err)
			continue
		}

		ch <- prometheus.MustNewConstMetric(storageDesc, prometheus.GaugeValue, float64(size), f.label)
	}
}

// fileSize is the size of the file, and 0 if there is none: SQLite removes the write-ahead log when the last
// connection closes.
func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}

	return fi.Size(), nil
}
