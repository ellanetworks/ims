package api

import (
	"log/slog"
	"net/http"
)

// Livez tells the kubelet that the process serves HTTP. It checks nothing else, so that a slow dependency fails
// readiness and does not restart the IMS.
func Livez() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

// Readyz tells whether the IMS can serve requests: 204 when cfg.Ready finds nothing wrong, 503 otherwise.
func Readyz(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Ready != nil {
			if err := cfg.Ready(r.Context()); err != nil {
				cfg.Logger.Warn("not ready", slog.Any("error", err))
				writeJSON(w, ErrorResponse{Error: "Not ready"}, http.StatusServiceUnavailable, cfg.Logger)

				return
			}
		}

		w.WriteHeader(http.StatusNoContent)
	})
}
