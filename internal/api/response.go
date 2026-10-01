package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type Response struct {
	Result any `json:"result,omitempty"`
}

func writeResponse(w http.ResponseWriter, v any, status int, logger *slog.Logger) {
	writeJSON(w, Response{Result: v}, status, logger)
}

func writeJSON(w http.ResponseWriter, v any, status int, logger *slog.Logger) {
	b, err := json.Marshal(v)
	if err != nil {
		logger.Error("failed to encode API response", slog.Any("error", err))
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if _, err := w.Write(b); err != nil {
		logger.Debug("failed to write API response", slog.Any("error", err))
	}
}
