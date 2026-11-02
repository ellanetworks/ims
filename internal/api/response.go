package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

type Response struct {
	Result any `json:"result,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func writeResponse(ctx context.Context, w http.ResponseWriter, v any, status int, logger *slog.Logger) {
	writeJSON(ctx, w, Response{Result: v}, status, logger)
}

func writeError(ctx context.Context, w http.ResponseWriter, status int, message string, err error, logger *slog.Logger) {
	if status >= http.StatusInternalServerError {
		logger.ErrorContext(ctx, message, slog.Any("error", err))
	} else {
		logger.DebugContext(ctx, message, slog.Any("error", err))
	}

	writeJSON(ctx, w, ErrorResponse{Error: message}, status, logger)
}

func writeJSON(ctx context.Context, w http.ResponseWriter, v any, status int, logger *slog.Logger) {
	b, err := json.Marshal(v)
	if err != nil {
		logger.ErrorContext(ctx, "failed to encode API response", slog.Any("error", err))
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if _, err := w.Write(b); err != nil {
		logger.DebugContext(ctx, "failed to write API response", slog.Any("error", err))
	}
}
