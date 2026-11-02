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

const maxBody = 1 << 20

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
