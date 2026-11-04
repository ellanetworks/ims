package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type RequestMetrics struct {
	duration   *prometheus.HistogramVec
	oidcLogins *prometheus.CounterVec
}

func NewRequestMetrics() *RequestMetrics {
	return &RequestMetrics{
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "ellaims_api_request_duration_seconds",
			Help: "Duration of API requests, by method, route and status code. Profiles under /api/v1/pprof/ " +
				"are left out.",
			Buckets:                         []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
			NativeHistogramBucketFactor:     1.1,
			NativeHistogramMaxBucketNumber:  100,
			NativeHistogramMinResetDuration: time.Hour,
		}, []string{"method", "route", "code"}),
		oidcLogins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellaims_oidc_logins_total",
			Help: "OIDC logins, by outcome.",
		}, []string{"outcome"}),
	}
}

func (m *RequestMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.duration, m.oidcLogins}
}

func (m *RequestMetrics) oidcLogin(outcome string) {
	if m != nil {
		m.oidcLogins.WithLabelValues(outcome).Inc()
	}
}

func (m *RequestMetrics) instrument(pattern string, next http.Handler) http.Handler {
	route := pattern
	if _, path, ok := strings.Cut(pattern, " "); ok {
		route = path
	}

	if m == nil || !strings.HasPrefix(route, "/api/") || strings.HasPrefix(route, "/api/v1/pprof/") {
		return next
	}

	return promhttp.InstrumentHandlerDuration(m.duration.MustCurryWith(prometheus.Labels{"route": route}), next)
}
