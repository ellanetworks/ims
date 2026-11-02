package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/ellanetworks/ims/internal/settings"
)

type Message struct {
	Message string `json:"message"`
}

// maxBody bounds a request body: an MCPTT group of its 100 members is a few kilobytes.
const maxBody = 1 << 20

// decodeStrictly decodes a request body that must hold one JSON value of only the fields of v, of at most maxBody
// bytes, and answers 400 if it does not.
func decodeStrictly(w http.ResponseWriter, r *http.Request, v any, logger *slog.Logger) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()

	err := dec.Decode(v)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("data after the JSON value")
	}

	if err != nil {
		writeError(r.Context(), w, http.StatusBadRequest, "Invalid request data", err, logger)
		return false
	}

	return true
}

// writeSettingsError answers a failed change of settings: with its message for the client's mistakes, or with a
// generic one if it could not be saved.
func writeSettingsError(ctx context.Context, w http.ResponseWriter, err error, failed string, logger *slog.Logger) {
	switch {
	case errors.Is(err, settings.ErrInvalid):
		writeError(ctx, w, http.StatusBadRequest, err.Error(), nil, logger)
	case errors.Is(err, settings.ErrConflict):
		writeError(ctx, w, http.StatusConflict, err.Error(), nil, logger)
	case errors.Is(err, settings.ErrNotFound):
		writeError(ctx, w, http.StatusNotFound, err.Error(), nil, logger)
	default:
		writeError(ctx, w, http.StatusInternalServerError, failed, err, logger)
	}
}
