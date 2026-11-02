package api

import (
	"net/http"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
)

type Policy interface {
	PolicyStatus() PolicyStatus
}

type PolicyStatus struct {
	Interface string
	Endpoint  string
	Notify    string
	Last      *PolicyResult
}

type PolicyResult struct {
	At        time.Time
	Reachable bool
	Result    string
}

type PolicyParams struct {
	Interface string    `json:"interface"`
	N5        *PolicyN5 `json:"n5,omitempty"`
}

type PolicyN5 struct {
	PCFURI string `json:"pcf_uri"`
}

type PolicyResponse struct {
	PolicyParams
	Status PolicyStatusResponse `json:"status"`
}

type PolicyStatusResponse struct {
	Interface string              `json:"interface"`
	Endpoint  string              `json:"endpoint,omitempty"`
	Notify    string              `json:"notify,omitempty"`
	Last      *PolicyLastResponse `json:"last,omitempty"`
}

type PolicyLastResponse struct {
	At        string `json:"at"`
	Reachable bool   `json:"reachable"`
	Result    string `json:"result"`
}

func GetPolicy(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponse(r.Context(), w, policyResponse(cfg), http.StatusOK, cfg.Logger)
	})
}

func UpdatePolicy(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params PolicyParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		p := settings.Policy{Interface: settings.PolicyInterface(params.Interface)}

		if params.N5 != nil {
			if p.Interface != settings.PolicyN5 {
				writeError(r.Context(), w, http.StatusBadRequest, "n5 is only for the n5 interface", nil, cfg.Logger)
				return
			}

			p.PCFURI = params.N5.PCFURI
		}

		if err := cfg.Settings.UpdatePolicy(r.Context(), p); err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to update policy", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, policyResponse(cfg), http.StatusOK, cfg.Logger)
	})
}

func policyResponse(cfg Config) PolicyResponse {
	p := cfg.Settings.Get().Policy

	resp := PolicyResponse{PolicyParams: PolicyParams{Interface: string(p.Interface)}}
	if p.Interface == settings.PolicyN5 {
		resp.N5 = &PolicyN5{PCFURI: p.PCFURI}
	}

	var st PolicyStatus
	if cfg.Policy != nil {
		st = cfg.Policy.PolicyStatus()
	}

	resp.Status = PolicyStatusResponse{Interface: st.Interface, Endpoint: st.Endpoint, Notify: st.Notify}
	if resp.Status.Interface == "" {
		resp.Status.Interface = string(settings.PolicyNone)
	}

	if st.Last != nil {
		resp.Status.Last = &PolicyLastResponse{At: formatTime(st.Last.At), Reachable: st.Last.Reachable, Result: st.Last.Result}
	}

	return resp
}
