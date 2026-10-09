package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	metricsScrapeTimeout       = 8 * time.Second
	metricsMaxRequestsInFlight = 32
)

// GetMetrics serves the metrics in the Prometheus formats. Without a Metrics registry, it serves none.
func GetMetrics(cfg Config) http.Handler {
	g := cfg.Metrics
	if g == nil {
		g = prometheus.NewRegistry()
	}

	return promhttp.HandlerFor(g, promhttp.HandlerOpts{
		Timeout:             metricsScrapeTimeout,
		MaxRequestsInFlight: metricsMaxRequestsInFlight,
		ErrorHandling:       promhttp.ContinueOnError,
		ErrorLog:            metricsErrorLog{cfg.Logger},
	})
}

// metricsErrorLog logs the metrics that could not be gathered, which a scrape leaves out.
type metricsErrorLog struct {
	logger *slog.Logger
}

func (l metricsErrorLog) Println(v ...any) {
	if l.logger != nil {
		l.logger.Warn("failed to gather metrics", slog.String("error", fmt.Sprint(v...)))
	}
}
