package api

import (
	"log/slog"
	"net/http"
)

type Config struct {
	Version  string
	Diameter Diameter
	Logger   *slog.Logger
}

func NewHandler(cfg Config) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /api/v1/status", GetStatus(cfg))
	mux.Handle("GET /api/v1/diameter", GetDiameterStatus(cfg))

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
