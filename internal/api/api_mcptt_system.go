package api

import (
	"net/http"
	"strings"
	"time"

	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

// MCPTTServiceConfiguration is the service configuration of the organisation (TS 24.484 §8.4). An absent optional field
// is of its default.
type MCPTTServiceConfiguration struct {
	Domain                     string                `json:"domain"`
	NumLevelsPriorityHierarchy *int                  `json:"num_levels_priority_hierarchy,omitempty"`
	TransmitTimeLimitMS        *int64                `json:"transmit_time_limit_ms,omitempty"`
	FCTimersCounters           MCPTTFCTimersCounters `json:"fc_timers_counters"`
}

type MCPTTFCTimersCounters struct {
	T1EndOfRTPMediaMS    *int64 `json:"t1_end_of_rtp_media_ms,omitempty"`
	T3StopTalkingGraceMS *int64 `json:"t3_stop_talking_grace_ms,omitempty"`
	T7FloorIdleMS        *int64 `json:"t7_floor_idle_ms,omitempty"`
	T8FloorRevokeMS      *int64 `json:"t8_floor_revoke_ms,omitempty"`
	C7FloorIdle          *int   `json:"c7_floor_idle,omitempty"`
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
		on := &c.OnNetwork
		fc := &on.FCTimersCounters
		on.NumLevelsPriorityHierarchy, fc.C7 = p.NumLevelsPriorityHierarchy, p.FCTimersCounters.C7FloorIdle

		var problems []string

		for _, d := range []struct {
			name string
			ms   *int64
			to   **time.Duration
		}{
			{"transmit_time_limit_ms", p.TransmitTimeLimitMS, &on.TransmitTime.TimeLimit},
			{"fc_timers_counters.t1_end_of_rtp_media_ms", p.FCTimersCounters.T1EndOfRTPMediaMS, &fc.T1},
			{"fc_timers_counters.t3_stop_talking_grace_ms", p.FCTimersCounters.T3StopTalkingGraceMS, &fc.T3},
			{"fc_timers_counters.t7_floor_idle_ms", p.FCTimersCounters.T7FloorIdleMS, &fc.T7},
			{"fc_timers_counters.t8_floor_revoke_ms", p.FCTimersCounters.T8FloorRevokeMS, &fc.T8},
		} {
			v, problem := optionalDuration(d.name, d.ms)
			if problem != "" {
				problems = append(problems, problem)
			}

			*d.to = v
		}

		if len(problems) > 0 {
			invalidMCPTT(w, strings.Join(problems, "; "), cfg.Logger)
			return
		}

		if err := cfg.MCPTT.UpdateServiceConfiguration(r.Context(), c); err != nil {
			writeMCPTTSettingsError(w, err, "Failed to update the service configuration", mcpttField, cfg.Logger)
			return
		}

		writeResponse(w, mcpttServiceConfigurationResponse(cfg.MCPTT.Get().ServiceConfiguration), http.StatusOK, cfg.Logger)
	})
}

func mcpttServiceConfigurationResponse(c mcpttsettings.ServiceConfiguration) MCPTTServiceConfiguration {
	on := c.OnNetwork
	fc := on.FCTimersCounters

	return MCPTTServiceConfiguration{
		Domain:                     c.Domain,
		NumLevelsPriorityHierarchy: on.NumLevelsPriorityHierarchy,
		TransmitTimeLimitMS:        optionalMilliseconds(on.TransmitTime.TimeLimit),
		FCTimersCounters: MCPTTFCTimersCounters{
			T1EndOfRTPMediaMS:    optionalMilliseconds(fc.T1),
			T3StopTalkingGraceMS: optionalMilliseconds(fc.T3),
			T7FloorIdleMS:        optionalMilliseconds(fc.T7),
			T8FloorRevokeMS:      optionalMilliseconds(fc.T8),
			C7FloorIdle:          fc.C7,
		},
	}
}
