package api

import "net/http"

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type Readiness struct {
	Ready  bool    `json:"ready"`
	Checks []Check `json:"checks"`
}

func Livez() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func Readyz(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res := Readiness{Ready: true, Checks: []Check{}}

		if cfg.Ready != nil {
			res.Checks = cfg.Ready(r.Context())
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
