package api

import (
	"log/slog"
	"net/http"
)

func Livez() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func Readyz(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Ready != nil {
			if err := cfg.Ready(r.Context()); err != nil {
				cfg.Logger.WarnContext(r.Context(), "not ready", slog.Any("error", err))
				writeJSON(r.Context(), w, ErrorResponse{Error: "Not ready"}, http.StatusServiceUnavailable, cfg.Logger)

				return
			}
		}

		w.WriteHeader(http.StatusNoContent)
	})
}
