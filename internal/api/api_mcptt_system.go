package api

import (
	"net/http"

	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

// TS 24.484 clause 8.4
type MCPTTServiceConfiguration struct {
	Domain              string `json:"domain"`
	TransmitTimeLimitMS *int64 `json:"transmit_time_limit_ms,omitempty"`
}

func GetMCPTTServiceConfiguration(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponse(r.Context(), w, mcpttServiceConfigurationResponse(cfg.MCPTT.Get().ServiceConfiguration), http.StatusOK, cfg.Logger)
	})
}

func UpdateMCPTTServiceConfiguration(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p MCPTTServiceConfiguration
		if !decodeStrictly(w, r, &p, cfg.Logger) {
			return
		}

		c := mcpttsettings.ServiceConfiguration{Domain: p.Domain}

		limit, problem := optionalDuration("transmit_time_limit_ms", p.TransmitTimeLimitMS)
		if problem != "" {
			invalidMCPTT(r.Context(), w, problem, cfg.Logger)
			return
		}

		c.OnNetwork.TransmitTime.TimeLimit = limit

		if err := cfg.MCPTT.UpdateServiceConfiguration(r.Context(), c); err != nil {
			writeMCPTTSettingsError(r.Context(), w, err, "Failed to update the service configuration", mcpttField, cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, mcpttServiceConfigurationResponse(cfg.MCPTT.Get().ServiceConfiguration), http.StatusOK, cfg.Logger)
	})
}

func mcpttServiceConfigurationResponse(c mcpttsettings.ServiceConfiguration) MCPTTServiceConfiguration {
	return MCPTTServiceConfiguration{
		Domain: c.Domain, TransmitTimeLimitMS: optionalMilliseconds(c.OnNetwork.TransmitTime.TimeLimit),
	}
}
