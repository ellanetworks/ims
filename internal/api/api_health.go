package api

import (
	"net/http"
	"strconv"

	"github.com/ellanetworks/ims/internal/cluster"
)

type Readiness struct {
	Ready  bool            `json:"ready"`
	Checks []cluster.Check `json:"checks"`
}

func Livez() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func Readyz(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		withCluster := false

		if v := r.URL.Query().Get("cluster"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				writeError(r.Context(), w, http.StatusBadRequest, "cluster must be true or false", err, cfg.Logger)
				return
			}

			withCluster = b
		}

		res := Readiness{Ready: true, Checks: []cluster.Check{}}

		if cfg.Ready != nil {
			res.Checks = cfg.Ready(r.Context(), withCluster)
		}

		for _, c := range res.Checks {
			res.Ready = res.Ready && c.OK
		}

		status := http.StatusOK
		if !res.Ready {
			status = http.StatusServiceUnavailable
		}

		w.Header().Set("Cache-Control", "no-store")
		writeResponse(r.Context(), w, res, status, cfg.Logger)
	})
}
