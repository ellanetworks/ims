package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ellanetworks/ims/internal/settings"
)

type Message struct {
	Message string `json:"message"`
}

// decodeStrictly decodes a request body that must hold only the fields of v, and answers 400 if it does not.
func decodeStrictly(w http.ResponseWriter, r *http.Request, v any, logger *slog.Logger) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request data", err, logger)
		return false
	}

	return true
}

// writeSettingsError answers a failed change of settings: with its message for the client's mistakes, or with a
// generic one if it could not be saved.
func writeSettingsError(w http.ResponseWriter, err error, failed string, logger *slog.Logger) {
	switch {
	case errors.Is(err, settings.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error(), nil, logger)
	case errors.Is(err, settings.ErrConflict):
		writeError(w, http.StatusConflict, err.Error(), nil, logger)
	case errors.Is(err, settings.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error(), nil, logger)
	default:
		writeError(w, http.StatusInternalServerError, failed, err, logger)
	}
}
