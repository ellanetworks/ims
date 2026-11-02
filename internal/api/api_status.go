package api

import (
	"net/http"

	"github.com/ellanetworks/ims/version"
)

type Status struct {
	Version     string `json:"version"`
	Revision    string `json:"revision"`
	Initialized bool   `json:"initialized"`
}

func GetStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := cfg.Auth.CountUsers(r.Context())
		if err != nil {
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to get status", err, cfg.Logger)
			return
		}

		v := version.Get()
		writeResponse(r.Context(), w, Status{Version: v.Version, Revision: v.Revision, Initialized: n > 0}, http.StatusOK, cfg.Logger)
	})
}
