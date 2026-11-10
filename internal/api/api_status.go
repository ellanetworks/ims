package api

import (
	"net/http"

	"github.com/ellanetworks/ims/internal/cluster"
	"github.com/ellanetworks/ims/version"
)

type Status struct {
	Version       string            `json:"version"`
	Revision      string            `json:"revision"`
	Initialized   bool              `json:"initialized"`
	Clustered     bool              `json:"clustered"`
	NodeID        string            `json:"node_id"`
	DisplayName   string            `json:"display_name"`
	DiameterLabel string            `json:"diameter_label"`
	DiameterHost  string            `json:"diameter_host"`
	Generated     bool              `json:"label_generated"`
	Formation     cluster.Formation `json:"formation"`
}

func GetStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := cfg.Auth.CountUsers(r.Context())
		if err != nil {
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to get status", err, cfg.Logger)
			return
		}

		v := version.Get()
		st := Status{
			Version: v.Version, Revision: v.Revision, Initialized: n > 0,
			Clustered: cfg.Cluster != nil && cfg.Cluster.Clustered(),
		}

		if cfg.Cluster != nil {
			st.NodeID = cfg.Cluster.ID()
			st.DisplayName = cfg.Cluster.DisplayName(r.Context())
			st.DiameterLabel, st.Generated = cfg.Cluster.DiameterLabel(), cfg.Cluster.LabelGenerated()
			st.Formation = cfg.Cluster.Formation()

			if cfg.Settings != nil {
				st.DiameterHost = cfg.Settings.Get().Operator.DiameterHost(st.DiameterLabel)
			}
		}

		writeResponse(r.Context(), w, st, http.StatusOK, cfg.Logger)
	})
}
