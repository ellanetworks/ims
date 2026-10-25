package api

import (
	"net/http"

	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

// MCPTTServiceConfiguration is the service configuration of the organisation (TS 24.484 §8.4) that an operator sets:
// its domain, the IMS home domain's unless set, and the transmit time limit, of its default if absent. The server
// fixes the rest to the defaults of TS 24.380 Table 11.1.3-1 and Table 11.2.3-1 (mcpttsettings.DefaultT7 for T7).
type MCPTTServiceConfiguration struct {
	Domain              string `json:"domain"`
	TransmitTimeLimitMS *int64 `json:"transmit_time_limit_ms,omitempty"`
}

func GetMCPTTServiceConfiguration(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponse(w, mcpttServiceConfigurationResponse(cfg.MCPTT.Get().ServiceConfiguration), http.StatusOK, cfg.Logger)
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
			invalidMCPTT(w, problem, cfg.Logger)
			return
		}

		c.OnNetwork.TransmitTime.TimeLimit = limit

		if err := cfg.MCPTT.UpdateServiceConfiguration(r.Context(), c); err != nil {
			writeMCPTTSettingsError(w, err, "Failed to update the service configuration", mcpttField, cfg.Logger)
			return
		}

		writeResponse(w, mcpttServiceConfigurationResponse(cfg.MCPTT.Get().ServiceConfiguration), http.StatusOK, cfg.Logger)
	})
}

func mcpttServiceConfigurationResponse(c mcpttsettings.ServiceConfiguration) MCPTTServiceConfiguration {
	return MCPTTServiceConfiguration{
		Domain: c.Domain, TransmitTimeLimitMS: optionalMilliseconds(c.OnNetwork.TransmitTime.TimeLimit),
	}
}
