package api

import (
	"net/http"

	"github.com/ellanetworks/ims/internal/ipsmgw"
	"github.com/ellanetworks/ims/internal/settings"
)

type IPSMGWParams struct {
	Number string `json:"number"`
}

type IPSMGW struct {
	IPSMGWParams
	Name string `json:"name"`
}

func ipsmgwResponse(s settings.Settings) IPSMGW {
	return IPSMGW{
		IPSMGWParams: IPSMGWParams{Number: s.IPSMGW.Number},
		Name:         "sip:" + ipsmgw.Host(s.Operator.HomeDomain()),
	}
}

func GetIPSMGW(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponse(r.Context(), w, ipsmgwResponse(cfg.Settings.Get()), http.StatusOK, cfg.Logger)
	})
}

func UpdateIPSMGW(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params IPSMGWParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		if err := cfg.Settings.UpdateIPSMGW(r.Context(), settings.IPSMGW{Number: params.Number}); err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to update the IP-SM-GW", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, ipsmgwResponse(cfg.Settings.Get()), http.StatusOK, cfg.Logger)
	})
}
