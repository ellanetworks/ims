package api

import (
	"net/http"
	"time"
)

type Policy interface {
	PolicyStatus() PolicyStatus
}

// PolicyStatus is the policy function of the P-CSCF. Interface is empty without one. The state of an Rx peer is
// in the Diameter status; over N5, Last is the outcome of the last request to the PCF.
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

type policyResponse struct {
	Interface string              `json:"interface"`
	Endpoint  string              `json:"endpoint,omitempty"`
	Notify    string              `json:"notify,omitempty"`
	Last      *policyLastResponse `json:"last,omitempty"`
}

type policyLastResponse struct {
	At        string `json:"at"`
	Reachable bool   `json:"reachable"`
	Result    string `json:"result"`
}

func GetPolicyStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var st PolicyStatus
		if cfg.Policy != nil {
			st = cfg.Policy.PolicyStatus()
		}

		resp := policyResponse{Interface: st.Interface, Endpoint: st.Endpoint, Notify: st.Notify}
		if resp.Interface == "" {
			resp.Interface = "none"
		}

		if st.Last != nil {
			resp.Last = &policyLastResponse{At: formatTime(st.Last.At), Reachable: st.Last.Reachable, Result: st.Last.Result}
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}
