package api

import (
	"net/http"

	"github.com/ellanetworks/ims/version"
)

// Status is the version of the IMS, and the git commit it was built from, empty in development builds; and what its
// MCPTT AS runs, if it runs one.
type Status struct {
	Version  string       `json:"version"`
	Revision string       `json:"revision"`
	MCPTT    *MCPTTStatus `json:"mcptt,omitempty"`
}

func GetStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		v := version.Get()
		s := Status{Version: v.Version, Revision: v.Revision}

		if cfg.MCPTTStatus != nil {
			m := cfg.MCPTTStatus()
			s.MCPTT = &m
		}

		writeResponse(w, s, http.StatusOK, cfg.Logger)
	})
}
