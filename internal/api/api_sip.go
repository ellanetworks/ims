package api

import (
	"net/http"
	"net/netip"
)

type SIP interface {
	Listeners() []SIPEndpoint
}

type SIPEndpoint struct {
	Role    string
	Address netip.AddrPort
}

type SIPListener struct {
	Role       string   `json:"role"`
	Address    string   `json:"address"`
	Transports []string `json:"transports"`
}

type SIPStatus struct {
	HomeDomain string        `json:"home_domain"`
	Aliases    []string      `json:"aliases"`
	Listeners  []SIPListener `json:"listeners"`
}

func GetSIPStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := SIPStatus{
			HomeDomain: cfg.HomeDomain,
			Aliases:    append([]string{}, cfg.SIPAliases...),
			Listeners:  []SIPListener{},
		}

		for _, l := range cfg.SIP.Listeners() {
			resp.Listeners = append(resp.Listeners, SIPListener{Role: l.Role, Address: l.Address.String(), Transports: []string{"udp", "tcp"}})
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}
