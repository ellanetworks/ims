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

type SMSRouteParams struct {
	ServiceCentre string   `json:"service_centre"`
	Peers         []string `json:"peers"`
}

type SMSRoute struct {
	ID string `json:"id"`
	SMSRouteParams
}

type SMSRouteList struct {
	Items []SMSRoute `json:"items"`
}

func ListSMSRoutes(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := SMSRouteList{Items: []SMSRoute{}}
		for _, route := range cfg.Settings.Get().SMSRoutes {
			resp.Items = append(resp.Items, smsRouteResponse(route))
		}

		writeResponse(r.Context(), w, resp, http.StatusOK, cfg.Logger)
	})
}

func GetSMSRoute(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := cfg.Settings.Get().SMSRoute(r.PathValue("id"))
		if !ok {
			writeError(r.Context(), w, http.StatusNotFound, "SMS route not found", nil, cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, smsRouteResponse(route), http.StatusOK, cfg.Logger)
	})
}

func CreateSMSRoute(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params SMSRouteParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		route, err := cfg.Settings.CreateSMSRoute(r.Context(), settings.SMSRoute{
			ServiceCentre: params.ServiceCentre, Peers: params.Peers,
		})
		if err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to create SMS route", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, smsRouteResponse(route), http.StatusCreated, cfg.Logger)
	})
}

func UpdateSMSRoute(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params SMSRouteParams
		if !decodeStrictly(w, r, &params, cfg.Logger) {
			return
		}

		route := settings.SMSRoute{ID: r.PathValue("id"), ServiceCentre: params.ServiceCentre, Peers: params.Peers}

		if err := cfg.Settings.UpdateSMSRoute(r.Context(), route); err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to update SMS route", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, smsRouteResponse(route), http.StatusOK, cfg.Logger)
	})
}

func DeleteSMSRoute(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := cfg.Settings.DeleteSMSRoute(r.Context(), r.PathValue("id")); err != nil {
			writeSettingsError(r.Context(), w, err, "Failed to delete SMS route", cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, Message{Message: "SMS route deleted"}, http.StatusOK, cfg.Logger)
	})
}

func smsRouteResponse(r settings.SMSRoute) SMSRoute {
	resp := SMSRoute{ID: r.ID, SMSRouteParams: SMSRouteParams{ServiceCentre: r.ServiceCentre, Peers: []string{}}}
	resp.Peers = append(resp.Peers, r.Peers...)

	return resp
}
