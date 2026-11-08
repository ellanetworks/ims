package server

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	CheckShutdown = "shutdown"
	CheckDatabase = "database"

	readinessTick = time.Second
)

type readinessMetrics struct {
	checks *prometheus.GaugeVec
}

func newReadinessMetrics() *readinessMetrics {
	return &readinessMetrics{
		checks: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ellaims_ready_check",
			Help: "1 when the readiness check passes, 0 when it fails.",
		}, []string{"check"}),
	}
}

func (m *readinessMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{m.checks}
}

type readiness struct {
	ping     func(context.Context) error
	stopping func() bool
	metrics  *readinessMetrics
	logger   *slog.Logger

	mu       sync.Mutex
	failing  string
	observed bool
}

func (r *readiness) checks(ctx context.Context) []api.Check {
	shutdown := api.Check{Name: CheckShutdown, OK: true}
	if r.stopping() {
		shutdown = api.Check{Name: CheckShutdown, Detail: "the IMS is shutting down"}
	}

	database := api.Check{Name: CheckDatabase, OK: true}
	if err := r.ping(ctx); err != nil {
		database = api.Check{Name: CheckDatabase, Detail: err.Error()}
	}

	return []api.Check{shutdown, database}
}

func (r *readiness) run(ctx context.Context) {
	ticker := time.NewTicker(readinessTick)
	defer ticker.Stop()

	for {
		r.observe(ctx, r.checks(ctx))

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *readiness) observe(ctx context.Context, checks []api.Check) {
	var failing, details []string

	for _, c := range checks {
		v := 0.0
		if c.OK {
			v = 1
		} else {
			failing = append(failing, c.Name)
			details = append(details, c.Name+": "+c.Detail)
		}

		r.metrics.checks.WithLabelValues(c.Name).Set(v)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	summary := strings.Join(failing, ",")

	if r.observed && summary == r.failing {
		return
	}

	switch {
	case len(failing) > 0:
		r.logger.WarnContext(ctx, "not ready", slog.Any("failing", details))
	case r.observed:
		r.logger.InfoContext(ctx, "ready")
	}

	r.failing, r.observed = summary, true
}
