package api

import (
	"net/http"

	"github.com/ellanetworks/ims/internal/ipsmgw"
	"github.com/ellanetworks/ims/internal/settings"
)

// IPSMGWParams are what an operator sets of the built-in IP-SM-GW: its E.164 number, empty until set.
type IPSMGWParams struct {
	Number string `json:"number"`
}

// IPSMGW is the built-in IP-SM-GW: its settings, and its name, the SIP URI the iFCs of the HSS route to it by.
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
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponse(w, ipsmgwResponse(cfg.Settings.Get()), http.StatusOK, cfg.Logger)
	})
}

// UpdateIPSMGW sets the IP-SM-GW number, which the IP-SM-GW registers at the HSS for its users at once.
func UpdateIPSMGW(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params IPSMGWParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		if err := cfg.Settings.UpdateIPSMGW(r.Context(), settings.IPSMGW{Number: params.Number}); err != nil {
			writeSettingsError(w, err, "Failed to update the IP-SM-GW", cfg.Logger)
			return
		}

		writeResponse(w, ipsmgwResponse(cfg.Settings.Get()), http.StatusOK, cfg.Logger)
	})
}
