package api

import (
	"log/slog"
	"net/http"
)

type Config struct {
	Version       string
	Diameter      Diameter
	SIP           SIP
	Registrations Registrations
	Policy        Policy
	HomeDomain    string
	SIPAliases    []string
	Logger        *slog.Logger
}

func NewHandler(cfg Config) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /api/v1/status", GetStatus(cfg))
	mux.Handle("GET /api/v1/diameter", GetDiameterStatus(cfg))
	mux.Handle("GET /api/v1/sip", GetSIPStatus(cfg))
	mux.Handle("GET /api/v1/policy", GetPolicyStatus(cfg))
	mux.Handle("POST /api/v1/registrations/{impi}/reauthenticate", PostReauthentication(cfg))

	return mux
}

type Status struct {
	Version string `json:"version"`
}

func GetStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponse(w, Status{Version: cfg.Version}, http.StatusOK, cfg.Logger)
	})
}
