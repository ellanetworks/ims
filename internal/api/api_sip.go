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
	TLS     bool
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := cfg.Settings.Get().Operator

		resp := SIPStatus{
			HomeDomain: op.HomeDomain(),
			Aliases:    op.SIPAliases(),
			Listeners:  []SIPListener{},
		}

		for _, l := range cfg.SIP.Listeners() {
			transports := []string{"udp", "tcp"}
			if l.TLS {
				transports = []string{"tls"}
			}

			resp.Listeners = append(resp.Listeners, SIPListener{Role: l.Role, Address: l.Address.String(), Transports: transports})
		}

		writeResponse(r.Context(), w, resp, http.StatusOK, cfg.Logger)
	})
}
